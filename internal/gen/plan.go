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

// step makes one value: a parameter of the constructor, or what a call of a
// provider returns.
type step struct {
	typ types.Type

	// name is what to call the value. Emit makes it unique.
	name string

	// param is the index of the parameter the value is, or -1 for a call.
	param int

	p    provider
	args []int
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
	calling []string

	// root is the field whose value is being worked out, where a diagnostic
	// about any of its dependencies is reported.
	root scan.Field

	used  map[string]bool
	diags []diag.Diag
}

// planOf works out how the constructor of c builds it, in w.
func planOf(w *world, c container) (plan, []diag.Diag) {
	pl := &planner{
		w:      w,
		c:      c,
		own:    c.Named.Obj().Pkg(),
		pl:     &plan{c: c},
		args:   make(map[string]int),
		chosen: make(map[string]*provider),
		byType: make(map[string]int),
		byCall: make(map[string]int),
		used:   make(map[string]bool),
	}

	for _, f := range c.fields {
		switch f.source {
		case fromArg:
			pl.args[typeKey(f.Type)] = len(pl.pl.params)
			pl.pl.params = append(pl.pl.params, param{name: f.paramName(), typ: f.Type})
		case fromRef:
			pl.choose(f)
		case fromType, fromNowhere:
		}
	}

	for _, f := range c.fields {
		if f.blank() || f.source == fromNowhere {
			continue
		}
		pl.root = f.Field
		if s := pl.need(f.Type, []string{c.Name + "." + f.Name}); s >= 0 {
			pl.pl.fields = append(pl.pl.fields, assign{field: f.Name, step: s})
		}
	}

	pl.warnUnused()
	pl.name()
	for _, s := range pl.pl.steps {
		pl.pl.errs = pl.pl.errs || s.p.errs
		pl.pl.cleanup = pl.pl.cleanup || s.p.cleanup
	}
	return *pl.pl, pl.diags
}

// choose resolves the with= of f, reporting at f what it names wrong.
func (pl *planner) choose(f field) {
	pl.chosen[typeKey(f.Type)] = nil

	refs := pl.w.refs
	if home := homeOf(f.Type); home != nil {
		refs = append(slices.Clip(refs), home)
	}
	p, err := pl.w.x.resolve(f.ref, f.Type, pl.own, refs)
	if err != nil {
		d := fieldDiag(f.Field, err)
		if e, ok := errors.AsType[*refError](err); ok && e.hint != "" {
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
	if s >= 0 {
		pl.byType[k] = s
	}
	return s
}

// find works out the step for t, looking in the order the README gives.
func (pl *planner) find(t types.Type, k string, via []string) int {
	if i, ok := pl.args[k]; ok {
		pl.used[k] = true
		return pl.add(step{typ: t, name: pl.pl.params[i].name, param: i})
	}
	if p, ok := pl.chosen[k]; ok {
		pl.used[k] = true
		if p == nil {
			return -1 // reported at the with=
		}
		return pl.call(*p, via)
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
	for _, p := range pl.w.x.lookup(pkg, t) {
		name := pl.nameOf(p)
		switch {
		case pl.isSelf(p):
		case p.bad != "":
			near = append(near, fmt.Sprintf("%s returns %s but cannot be a provider: %s", name, pl.typeString(t), p.bad))
		case !own && !p.fn.Exported():
			near = append(near, fmt.Sprintf("%s returns %s but is not exported", name, pl.typeString(t)))
		case p.test && !pl.c.test:
			near = append(near, fmt.Sprintf("%s returns %s but is declared in a _test.go file, which only tests compile",
				name, pl.typeString(t)))
		default:
			ps = append(ps, p)
		}
	}
	return ps, near
}

// isSelf reports whether p is the constructor, or its must variant, that an
// earlier run generated for this very container.
func (pl *planner) isSelf(p provider) bool {
	return p.fn.Pkg().Path() == pl.c.PkgPath && (p.fn.Name() == pl.c.ctor || p.fn.Name() == pl.c.must)
}

// call returns the step calling p, working out its arguments first.
func (pl *planner) call(p provider, via []string) int {
	key := p.fn.FullName()
	if s, ok := pl.byCall[key]; ok {
		return s
	}
	name := pl.nameOf(p)
	if i := slices.Index(pl.calling, name); i >= 0 {
		cycle := append(slices.Clone(pl.calling[i:]), name)
		d := diag.Errorf(pl.root.Pos, "dependency cycle: %s", strings.Join(cycle, " → "))
		if outer := via[:len(via)-len(pl.calling)+i]; len(outer) > 1 {
			d = d.WithHints("via " + strings.Join(outer, " → "))
		}
		pl.diags = append(pl.diags, d)
		return -1
	}

	pl.calling = append(pl.calling, name)
	defer func() { pl.calling = pl.calling[:len(pl.calling)-1] }()

	via = append(slices.Clip(via), name)
	args := make([]int, len(p.params))
	for i, t := range p.params {
		if args[i] = pl.need(t, via); args[i] < 0 {
			return -1
		}
	}

	s := pl.add(step{typ: p.result, param: -1, p: p, args: args})
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
	for _, a := range pl.pl.fields {
		if s := &steps[a.step]; s.param < 0 && s.name == "" {
			s.name = lowerFirst(a.field)
		}
	}
	for _, s := range steps {
		if s.param >= 0 {
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
