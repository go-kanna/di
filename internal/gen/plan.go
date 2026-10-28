package gen

import (
	"errors"
	"fmt"
	"go/types"
	"slices"
	"strings"

	"github.com/go-kanna/internal/diag"
	"github.com/go-kanna/internal/scan"
)

// world is what the containers of a run look their providers up in.
type world struct {
	x *index

	// local reports whether a package is part of the user's module, the
	// packages kanna-di looks in on its own.
	local func(*types.Package) bool

	// refs are the packages a with= may name besides the container's own:
	// those of the user's module.
	refs []*types.Package

	// dir is the directory positions in hints are written relative to, as
	// diag.Write writes those of the diagnostics.
	dir string

	// containers holds the containers of the run by the key of their type,
	// for a container embedding one to build it in place.
	containers map[string]container

	// ours are the constructors the run generates; see plans.
	ours []*ourCtor

	// output holds the files an earlier run wrote in the packages of the run.
	// What they declare is passed over, as it is about to be replaced.
	output map[string]bool
}

// plan is how the constructor of a container builds it.
type plan struct {
	c container

	// params are the constructor's parameters, in the order of the fields
	// that take them.
	params []param

	// steps make the values, each from the steps before it.
	steps []step

	// fields says which step each field of the struct literal gets.
	fields []assign

	// errs and cleanup report that a step returns an error, or a cleanup.
	errs, cleanup bool
}

// param is a parameter of the constructor.
type param struct {
	name string
	typ  types.Type
}

// stepKind says how a step makes its value.
type stepKind int

const (
	// paramStep is a parameter of the constructor.
	paramStep stepKind = iota

	// callStep is what a call of a provider returns.
	callStep

	// fieldStep is a field of the value of another step, a struct the
	// container embeds.
	fieldStep

	// literalStep is a container the container embeds, built in place.
	literalStep
)

// step makes one value.
type step struct {
	kind stepKind
	typ  types.Type

	// name is what to call the value. Emit makes it unique.
	name string

	// param is the index of the parameter, for a paramStep.
	param int

	// p and args are the provider and the steps passed to it, for a
	// callStep.
	p    provider
	args []int

	// of and path are the step whose value holds the field and the fields
	// leading to it, for a fieldStep.
	of   int
	path []string

	// lit is the container built in place, for a literalStep.
	lit *literal
}

// literal is a container built in place, as a struct literal or, when
// pointer, the address of one.
type literal struct {
	c       container
	pointer bool
	fields  []assign
}

// assign puts the value of a step into a field of the struct literal.
type assign struct {
	field string
	step  int
}

// planner works out the plan of one container.
type planner struct {
	w   *world
	c   container
	own *types.Package
	pl  *plan

	// args holds the parameter each type taken with di:"arg" is, and used
	// what was asked for.
	args map[string]int

	// chosen holds the provider each with= of the container chose, or nil for
	// one that names none.
	chosen map[string]*provider

	// byType and byCall hold the step made for a type and for a call, so that
	// everything needing one shares it.
	byType map[string]int
	byCall map[string]int

	// calling lists the calls being worked out, innermost last, to tell a
	// cycle.
	calling []callFrame

	// embeds are the structs the container embeds without building them in
	// place, its own and those of the containers it does build in place.
	embeds []*embedded

	// innerChosen holds what the with= of the containers built in place
	// chose, by type.
	innerChosen map[string][]innerChoice

	// root is the field whose value is being worked out, where a diagnostic
	// about any of its dependencies is reported.
	root scan.Field

	used  map[string]bool
	diags []diag.Diag

	// waits are the needs set aside for constructors of the run not planned
	// yet.
	waits []wait
}

// callFrame is a call being worked out, and the path that led to it.
type callFrame struct {
	name string
	via  []string
}

// planOf works out how the constructor of c builds it, in w. A plan that has
// to wait for another constructor of the run says what it waits for instead.
func planOf(w *world, c container) (plan, []diag.Diag, []wait) {
	pl := &planner{
		w:           w,
		c:           c,
		own:         c.Named.Obj().Pkg(),
		pl:          &plan{c: c},
		args:        make(map[string]int),
		chosen:      make(map[string]*provider),
		byType:      make(map[string]int),
		byCall:      make(map[string]int),
		innerChosen: make(map[string][]innerChoice),
		used:        make(map[string]bool),
	}

	pl.collect(c, true, []string{c.Name})
	pl.pl.fields = pl.fill(c, c.Name, true, []string{c.Name})

	pl.warnUnused()
	pl.name()
	for _, s := range pl.pl.steps {
		pl.pl.errs = pl.pl.errs || s.p.errs
		pl.pl.cleanup = pl.pl.cleanup || s.p.cleanup
	}
	return *pl.pl, pl.diags, pl.waits
}

// choose resolves the with= of f, reporting at f what it names wrong.
func (pl *planner) choose(f field) {
	pl.chosen[typeKey(f.Type)] = nil

	refs := pl.w.refs
	if home := homeOf(f.Type); home != nil {
		refs = append(slices.Clip(refs), home)
	}
	p, err := pl.w.x.resolve(f.ref, f.Type, pl.own, pl.c.test, refs)
	if err != nil {
		d := fieldDiag(f.Field, err)
		if e, ok := errors.AsType[refError](err); ok && e.hint != "" {
			d = d.WithHints(e.hint)
		}
		pl.diags = append(pl.diags, d)
		return
	}
	pl.chosen[typeKey(f.Type)] = &p
}

// need returns the step that makes a value of type t, or -1 when there is
// none, having said why. via is the path that led to the need, from the field
// on.
func (pl *planner) need(t types.Type, via []string) int {
	k := typeKey(t)
	if s, ok := pl.byType[k]; ok {
		pl.used[k] = true
		return s
	}
	s := pl.find(t, k, via)
	if s >= 0 && !pl.busyWith(k) {
		pl.byType[k] = s
	}
	return s
}

// busyWith reports whether a struct being worked out embeds a field of the type
// k keys. What its fields would serve is found elsewhere until it is done, and
// that is not what the type is to come from once they can.
func (pl *planner) busyWith(k string) bool {
	return slices.ContainsFunc(pl.embeds, func(e *embedded) bool { return e.busy && hasKey(e.byType, k) })
}

// find works out the step for t, looking in the order the README gives.
func (pl *planner) find(t types.Type, k string, via []string) int {
	if i, ok := pl.args[k]; ok {
		pl.used[k] = true
		return pl.add(step{kind: paramStep, typ: t, name: pl.pl.params[i].name, param: i})
	}
	if p, ok := pl.chosen[k]; ok {
		pl.used[k] = true
		if p == nil {
			return -1 // reported at the with=
		}
		return pl.call(*p, via)
	}
	if s, ok := pl.fromEmbedded(t, k, via); ok {
		return s
	}

	var near []string
	ps, n := pl.candidates(pl.own, t, true)
	near = append(near, n...)
	if len(ps) == 0 {
		if home := homeOf(t); home != nil && home.Path() != pl.own.Path() && pl.w.local(home) {
			ps, n = pl.candidates(home, t, false)
			near = append(near, n...)
		}
	}

	switch len(ps) {
	case 0:
		pl.missing(t, via, near)
		return -1
	case 1:
		return pl.call(ps[0], via)
	default:
		pl.ambiguous(t, via, ps)
		return -1
	}
}

// candidates returns the providers of t in pkg that the container can call,
// and, for a hint, why the other functions of pkg returning t cannot be
// called. own says whether pkg is the container's package, whose unexported
// functions it can call too.
func (pl *planner) candidates(pkg *types.Package, t types.Type, own bool) ([]provider, []string) {
	var (
		ps   []provider
		near []string
	)
	ours := pl.w.generatedIn(pkg, t, pl.c)
	all := slices.Clone(pl.w.x.lookup(pkg, t))
	for _, g := range ours {
		all = append(all, g.p)
	}
	for _, p := range all {
		name := pl.nameOf(p)
		switch {
		case pl.w.output[p.pos.Filename]:
		case p.bad != "":
			near = append(near, fmt.Sprintf("%s returns %s but cannot be a provider: %s", name, pl.typeString(t), p.bad))
		case !own && !p.fn.Exported():
			near = append(near, fmt.Sprintf("%s returns %s but is not exported", name, pl.typeString(t)))
		case !p.serves(pl.own, pl.c.test):
			near = append(near, fmt.Sprintf("%s returns %s but is declared in a _test.go file, which only tests compile",
				name, pl.typeString(t)))
		default:
			ps = append(ps, p)
		}
	}
	return ps, near
}

// call returns the step calling p, working out its arguments first.
func (pl *planner) call(p provider, via []string) int {
	key := p.fn.FullName()
	if s, ok := pl.byCall[key]; ok {
		return s
	}
	if g, ok := pl.w.isOurs(p); ok && !g.planned {
		pl.waits = append(pl.waits, wait{root: pl.root, t: p.result, via: via, on: g})
		return -1
	}
	name := pl.nameOf(p)
	if i := slices.IndexFunc(pl.calling, func(f callFrame) bool { return f.name == name }); i >= 0 {
		var cycle []string
		for _, f := range pl.calling[i:] {
			cycle = append(cycle, f.name)
		}
		d := diag.Errorf(pl.root.Pos, "dependency cycle: %s", strings.Join(append(cycle, name), " → "))
		if outer := pl.calling[i].via; len(outer) > 1 {
			d = d.WithHints("via " + strings.Join(outer, " → "))
		}
		pl.diags = append(pl.diags, d)
		return -1
	}

	pl.calling = append(pl.calling, callFrame{name: name, via: via})
	defer func() { pl.calling = pl.calling[:len(pl.calling)-1] }()

	via = append(slices.Clip(via), name)
	args := make([]int, len(p.params))
	for i, t := range p.params {
		if args[i] = pl.need(t, via); args[i] < 0 {
			return -1
		}
	}

	s := pl.add(step{kind: callStep, typ: p.result, p: p, args: args})
	pl.byCall[key] = s
	return s
}

func (pl *planner) add(s step) int {
	pl.pl.steps = append(pl.pl.steps, s)
	return len(pl.pl.steps) - 1
}

// missing reports that nothing provides t.
func (pl *planner) missing(t types.Type, via []string, near []string) {
	msg := "nothing provides " + pl.typeString(t)
	if len(via) > 1 {
		msg += ", which " + via[len(via)-1] + " needs"
	}
	d := diag.Errorf(pl.root.Pos, "%s", msg)
	if len(via) > 1 {
		d = d.WithHints("via " + strings.Join(via, " → "))
	}
	d = d.WithHints(near...)
	if home := homeOf(t); home != nil && home.Path() != pl.own.Path() && !pl.w.local(home) {
		d = d.WithHints(home.Path() + " is not in your module, so kanna-di does not look there")
	}
	pl.diags = append(pl.diags, d.WithHints(fmt.Sprintf(
		`return one from a function in package %s, take it with di:"arg", or choose a provider with di:"with=..."`,
		pl.own.Name())))
}

// ambiguous reports that more than one provider of t is in reach.
func (pl *planner) ambiguous(t types.Type, via []string, ps []provider) {
	d := diag.Errorf(pl.root.Pos, "%d functions in package %s return %s", len(ps), ps[0].fn.Pkg().Name(), pl.typeString(t))
	for _, p := range ps {
		pos := p.pos
		pos.Filename = diag.ShortPath(pl.w.dir, pos.Filename)
		d = d.WithHints(fmt.Sprintf("%s (%s)", pl.nameOf(p), pos))
	}
	if len(via) > 1 {
		d = d.WithHints("via " + strings.Join(via, " → "))
	}
	pl.diags = append(pl.diags, d.WithHints(fmt.Sprintf("choose one with _ %s `di:\"with=%s\"`",
		pl.typeString(t), pl.nameOf(ps[0]))))
}

// warnUnused warns about what the container takes or chooses without needing
// it. A named field needs its own value, so only a blank one can go unused.
func (pl *planner) warnUnused() {
	for _, f := range pl.c.fields {
		if !f.blank() || pl.used[typeKey(f.Type)] {
			continue
		}
		switch f.source {
		case fromArg:
			pl.diags = append(pl.diags, diag.Warningf(f.Pos, "nothing in %s needs the %s it takes with di:\"arg\"",
				pl.c.Name, pl.typeString(f.Type)))
		case fromRef:
			if pl.chosen[typeKey(f.Type)] != nil {
				pl.diags = append(pl.diags, diag.Warningf(f.Pos, "nothing in %s needs %s, so its with= chooses nothing",
					pl.c.Name, pl.typeString(f.Type)))
			}
		case fromType, fromNowhere:
		}
	}
}

// name gives each step the name of its variable, unique or not: the field it
// goes into, or else the parameter of the first call it is passed to, or else
// one made from its type.
func (pl *planner) name() {
	steps := pl.pl.steps
	for _, a := range pl.assigns() {
		if s := &steps[a.step]; s.name == "" {
			s.name = lowerFirst(a.field)
		}
	}
	for _, s := range steps {
		if s.kind != callStep {
			continue
		}
		params := s.p.fn.Signature().Params()
		for i, arg := range s.args {
			if a := &steps[arg]; a.name == "" && i < params.Len() {
				if n := params.At(i).Name(); n != "" && n != "_" {
					a.name = n
				}
			}
		}
	}
	for i := range steps {
		if steps[i].name == "" {
			steps[i].name = nameOf(steps[i].typ)
		}
	}
}

// nameOf names p as the container's source would: by itself in the
// container's package, qualified by its package's name elsewhere.
func (pl *planner) nameOf(p provider) string {
	if p.fn.Pkg().Path() == pl.own.Path() {
		return p.fn.Name()
	}
	return p.fn.Pkg().Name() + "." + p.fn.Name()
}

func (pl *planner) typeString(t types.Type) string {
	return typeString(t, pl.own)
}

// homeOf returns the package declaring t, or the type t points to, or nil for
// a type no package declares.
func homeOf(t types.Type) *types.Package {
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		t = p.Elem()
	}
	if named, ok := types.Unalias(t).(*types.Named); ok {
		return named.Obj().Pkg()
	}
	return nil
}
