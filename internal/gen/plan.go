package gen

import (
	"errors"
	"fmt"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/go-kanna/internal/diag"
	"github.com/go-kanna/internal/packages"
	"github.com/go-kanna/internal/qualifier"
	"github.com/go-kanna/internal/scan"
)

// world is what the containers of a run look their providers up in.
type world struct {
	x *index

	// local reports whether a package is part of the user's module, the
	// packages kanna-di looks in on its own.
	local func(*types.Package) bool

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
	// spelled reports a name the tag spells out, di:"arg=<name>", rather than
	// one made from the type.
	spelled bool
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

	// zeroStep is the zero value of a struct without fields, the only value
	// it has.
	zeroStep
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

	// zeros holds the structs without fields that the container, or one it
	// builds in place, has a field of, by type.
	zeros map[string]bool

	// root is the field whose value is being worked out, where a diagnostic
	// about any of its dependencies is reported.
	root scan.Field

	used  map[string]bool
	diags []diag.Diag

	// waits are the needs set aside for constructors of the run not planned
	// yet.
	waits []wait
}

// callFrame is a call being worked out, and the path that led to it. key is
// the function's full name, and name how the container's source names it.
type callFrame struct {
	key, name string
	pos       token.Position
	via       []string
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
		zeros:       make(map[string]bool),
		used:        make(map[string]bool),
	}

	pl.collect(c, true, []string{c.Name}, map[string]bool{})
	pl.pl.fields = pl.fill(c, c.Name, true, []string{c.Name})

	// A need cut short by an error, or set aside, leaves what it would have
	// used unmarked.
	if !diag.HasErrors(pl.diags) && len(pl.waits) == 0 {
		pl.warnUnused()
	}
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

	p, err := pl.w.x.resolve(f.ref, f.Type, pl.own, pl.c.test, qualifier.Of(pl.c.File, pl.c.Named.Obj().Pkg()))
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
	pl.used[k] = true
	if s, ok := pl.byType[k]; ok {
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
		return pl.add(step{kind: paramStep, typ: t, name: pl.pl.params[i].name, param: i})
	}
	if p, ok := pl.chosen[k]; ok {
		if p == nil {
			return -1 // reported at the with=
		}
		return pl.call(*p, via)
	}
	if s, ok := pl.fromEmbedded(t, k, via); ok {
		return s
	}
	if fieldless(t) {
		return pl.add(step{kind: zeroStep, typ: t})
	}

	// The functions of both packages count together, so that one added to
	// either cannot take the place of the other without a word.
	ps, near := pl.candidates(pl.own, t, true)
	if home := homeOf(t); home != nil && home.Path() != pl.own.Path() && pl.w.local(home) {
		hps, n := pl.candidates(home, t, false)
		ps = append(ps, hps...)
		near = append(near, n...)
	}

	switch len(ps) {
	case 0:
		pl.missing(t, via, append(near, pl.nearly(t)...))
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
	if i := slices.IndexFunc(pl.calling, func(f callFrame) bool { return f.key == key }); i >= 0 {
		var cycle []string
		for _, f := range pl.calling[i:] {
			cycle = append(cycle, f.name)
		}
		d := diag.Errorf(pl.root.Pos, "dependency cycle: %s", strings.Join(append(cycle, name), " → "))
		for _, f := range pl.calling[i:] {
			pos := f.pos
			pos.Filename = diag.ShortPath(pl.w.dir, pos.Filename)
			d = d.WithHints(fmt.Sprintf("%s (%s)", f.name, pos))
		}
		if outer := pl.calling[i].via; len(outer) > 1 {
			d = d.WithHints("via " + strings.Join(outer, " → "))
		}
		pl.diags = append(pl.diags, d)
		return -1
	}

	pl.calling = append(pl.calling, callFrame{key: key, name: name, pos: p.pos, via: via})
	defer func() { pl.calling = pl.calling[:len(pl.calling)-1] }()

	via = append(slices.Clip(via), name)
	args := make([]int, 0, len(p.params)+1)
	for _, t := range p.params {
		s := pl.need(t, via)
		if s < 0 {
			return -1
		}
		args = append(args, s)
	}
	if p.variadic != nil && pl.declares(p.variadic) {
		s := pl.need(p.variadic, via)
		if s < 0 {
			return -1
		}
		args = append(args, s)
	}

	s := pl.add(step{kind: callStep, typ: p.result, p: p, args: args})
	pl.byCall[key] = s
	return s
}

// declares reports whether the container says where a value of t comes from:
// it takes or chooses one, what it embeds holds one, or it has a field of t,
// a struct without fields. A variadic parameter is passed only such a value,
// so that what a call gets is read off the container, and a function found
// never fills one in.
func (pl *planner) declares(t types.Type) bool {
	k := typeKey(t)
	return hasKey(pl.args, k) || hasKey(pl.chosen, k) || len(pl.innerChosen[k]) > 0 || pl.zeros[k] ||
		slices.ContainsFunc(pl.embeds, func(e *embedded) bool { return hasKey(e.byType, k) })
}

func (pl *planner) add(s step) int {
	pl.pl.steps = append(pl.pl.steps, s)
	return len(pl.pl.steps) - 1
}

// missing reports that nothing provides t. A field needing t itself may be
// one the program sets later, which di:"-" says, but not a blank one, which
// holds no value. The hint names the field: it may be one of a container built
// in place, reported at the field embedding it.
func (pl *planner) missing(t types.Type, via []string, near []string) {
	if packages.Invalid(t) {
		pl.diags = append(pl.diags, packages.NotTypeChecked(pl.root.Pos))
		return
	}
	msg := "nothing provides " + pl.typeString(t)
	if len(via) > 1 {
		msg += ", which " + via[len(via)-1] + " needs"
	}
	d := diag.Errorf(pl.root.Pos, "%s", msg)
	if len(via) > 1 {
		d = d.WithHints("via " + strings.Join(via, " → "))
	}
	d = d.WithHints(near...)
	switch home := homeOf(t); {
	case !providable(t):
		d = d.WithHints(pl.typeString(t) + " has no name of its own, so kanna-di does not hand it out by type")
		if len(via) > 1 {
			d = d.WithHints(fmt.Sprintf(
				"call %s from a function in package %s that passes it, or have it take a type of its own, such as type DSN string",
				via[len(via)-1], pl.own.Name()))
		} else {
			d = d.WithHints(ownTypeHint)
		}
	default:
		if home != nil && home.Path() != pl.own.Path() && !pl.w.local(home) {
			d = d.WithHints(home.Path() + " is not in your module, so kanna-di does not look there")
			// A variadic one, io.MultiWriter, would be called with nothing.
			ps, _ := pl.candidates(home, t, false)
			ps = slices.DeleteFunc(ps, func(p provider) bool { return p.fn.Signature().Variadic() })
			if len(ps) == 1 {
				d = d.WithHints(fmt.Sprintf("%s returns it: choose it with _ %s `di:\"with=%s\"`",
					pl.nameOf(ps[0]), pl.typeString(t), pl.nameOf(ps[0])))
			}
		}
		d = d.WithHints(fmt.Sprintf(
			`return one from a function in package %s, take it with di:"arg", or choose a provider with di:"with=..."`,
			pl.own.Name()))
	}
	if len(via) == 1 && pl.root.Name != "_" {
		d = d.WithHints(fmt.Sprintf(`or leave %s at its zero value with di:"-"`, via[0]))
	}
	pl.diags = append(pl.diags, d)
}

// nearly lists, for a hint, the providers in reach of *t for a t, or of t for
// a *t: a pointer-or-value mismatch is the likely reason nothing provides t.
func (pl *planner) nearly(t types.Type) []string {
	var other types.Type
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		other = p.Elem()
	} else {
		other = types.NewPointer(t)
	}
	if !providable(other) {
		return nil
	}

	pkgs := []*types.Package{pl.own}
	if home := homeOf(t); home != nil && home.Path() != pl.own.Path() && pl.w.local(home) {
		pkgs = append(pkgs, home)
	}
	var hints []string
	for _, pkg := range pkgs {
		ps, _ := pl.candidates(pkg, other, pkg == pl.own)
		for _, p := range ps {
			hints = append(hints, fmt.Sprintf("%s returns %s, not %s", pl.nameOf(p), pl.typeString(other), pl.typeString(t)))
		}
	}
	return hints
}

// ambiguous reports that more than one provider of t is in reach.
func (pl *planner) ambiguous(t types.Type, via []string, ps []provider) {
	where := "package " + ps[0].fn.Pkg().Name()
	if last := ps[len(ps)-1].fn.Pkg(); last.Path() != ps[0].fn.Pkg().Path() {
		where = "packages " + ps[0].fn.Pkg().Name() + " and " + last.Name()
	}
	d := diag.Errorf(pl.root.Pos, "%d functions in %s return %s", len(ps), where, pl.typeString(t))
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
// it. A named field needs its own value, so only a blank one can go unused;
// a blank struct is used, too, when one of its fields is.
func (pl *planner) warnUnused() {
	for _, f := range pl.c.fields {
		if !f.blank() || pl.used[typeKey(f.Type)] {
			continue
		}
		var ofFields, norFields string
		if len(promotedOf(f.Type, pl.own)) > 0 {
			ofFields, norFields = " or any of its fields", ", nor any of its fields"
		}
		switch f.source {
		case fromArg:
			pl.diags = append(pl.diags, diag.Warningf(f.Pos, "nothing in %s needs the %s it takes with di:\"arg\"%s",
				pl.c.Name, pl.typeString(f.Type), norFields))
		case fromRef:
			if pl.chosen[typeKey(f.Type)] != nil {
				pl.diags = append(pl.diags, diag.Warningf(f.Pos, "nothing in %s needs %s%s, so its with= chooses nothing",
					pl.c.Name, pl.typeString(f.Type), ofFields))
			}
		case fromType:
			// Only a struct with fields to serve gets here; see fieldsOf.
			pl.diags = append(pl.diags, diag.Warningf(f.Pos, "nothing in %s needs %s%s",
				pl.c.Name, pl.typeString(f.Type), ofFields))
		case fromNowhere:
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

// fieldless reports whether t is a named struct type without fields, not a
// pointer to one, whose zero value is the only value it has. A field of the
// type is left at it, and so is a parameter, rather than calling a function.
func fieldless(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	st, ok := n.Underlying().(*types.Struct)
	return ok && st.NumFields() == 0
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
