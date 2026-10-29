package gen

import (
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/go-kanna/internal/diag"
	"github.com/go-kanna/internal/scan"
)

// ourCtor is a constructor the run generates, a provider for the other
// containers of the run.
type ourCtor struct {
	c container

	// p is the constructor as a provider: what it returns, and, once planned,
	// what it takes.
	p provider

	planned bool
}

// wait is a need set aside for a constructor of the run not planned yet.
type wait struct {
	root scan.Field
	t    types.Type
	via  []string
	on   *ourCtor
}

// plans works out the plans of cs, the containers of one run, in w, in the
// order of cs, for those that can be planned.
//
// A container may need what another of the run builds. The constructor of that
// one is a provider from the start, so that whether it makes a type ambiguous
// does not depend on the order the containers are planned in; but what it
// takes is known only once it is planned. A container needing it waits for the
// next round, and rounds go on until one plans nothing more. What an earlier
// run generated is passed over, as w.output says.
func plans(w *world, cs []container) ([]plan, []diag.Diag) {
	w.ours = make([]*ourCtor, len(cs))
	for i, c := range cs {
		pkg := c.Named.Obj().Pkg()
		results := types.NewTuple(types.NewVar(token.NoPos, pkg, "", c.returns))
		fn := types.NewFunc(token.NoPos, pkg, c.ctor, types.NewSignatureType(nil, nil, nil, nil, results, false))
		w.ours[i] = &ourCtor{c: c, p: provider{fn: fn, result: c.returns, pos: c.Pos, test: c.test}}
	}

	type state struct {
		p      plan
		diags  []diag.Diag
		waits  []wait
		failed bool
	}
	states := make([]state, len(cs))
	for progress := true; progress; {
		progress = false
		for i, c := range cs {
			if states[i].failed || w.ours[i].planned {
				continue
			}
			p, ds, ws := planOf(w, c)
			states[i] = state{p: p, diags: ds, waits: ws, failed: diag.HasErrors(ds)}
			if !states[i].failed && len(ws) == 0 {
				w.offer(w.ours[i], p)
				progress = true
			}
		}
	}

	var (
		planned []plan
		diags   []diag.Diag
	)
	for i, s := range states {
		diags = append(diags, s.diags...)
		switch {
		case w.ours[i].planned:
			planned = append(planned, s.p)
		case !s.failed:
			for _, wt := range s.waits {
				awaited := states[slices.Index(w.ours, wt.on)]
				diags = append(diags, waitDiag(wt, cs[i], awaited.failed, awaited.waits))
			}
		}
	}
	return planned, diags
}

// offer makes g, whose container p plans, a provider taking what p takes.
func (w *world) offer(g *ourCtor, p plan) {
	pkg := g.c.Named.Obj().Pkg()
	params := make([]*types.Var, len(p.params))
	g.p.params = make([]types.Type, len(p.params))
	for i, prm := range p.params {
		params[i] = types.NewVar(token.NoPos, pkg, prm.name, prm.typ)
		g.p.params[i] = prm.typ
	}
	results := []*types.Var{types.NewVar(token.NoPos, pkg, "", g.c.returns)}
	if p.cleanup {
		results = append(results, types.NewVar(token.NoPos, pkg, "", types.NewSignatureType(nil, nil, nil, nil, nil, false)))
	}
	if p.errs {
		results = append(results, types.NewVar(token.NoPos, nil, "", types.Universe.Lookup("error").Type()))
	}
	g.p.fn = types.NewFunc(token.NoPos, pkg, g.c.ctor,
		types.NewSignatureType(nil, nil, nil, types.NewTuple(params...), types.NewTuple(results...), false))
	g.p.errs, g.p.cleanup = p.errs, p.cleanup
	g.planned = true
}

// generatedIn returns the constructors of the run in pkg returning t, other
// than that of c itself.
func (w *world) generatedIn(pkg *types.Package, t types.Type, c container) []*ourCtor {
	var gs []*ourCtor
	for _, g := range w.ours {
		if g.c.PkgPath == pkg.Path() && typeKey(g.c.returns) == typeKey(t) && g.c.Pos != c.Pos {
			gs = append(gs, g)
		}
	}
	return gs
}

// waitDiag reports that c needed what a constructor of the run builds, which
// is not generated after all. awaitedFailed and awaitedWaits say why: the
// container of that constructor has errors of its own, or waits in turn.
func waitDiag(wt wait, c container, awaitedFailed bool, awaitedWaits []wait) diag.Diag {
	name := wt.on.p.fn.Name()
	if wt.on.c.PkgPath != c.PkgPath {
		name = wt.on.p.fn.Pkg().Name() + "." + name
	}
	what := typeString(wt.t, c.Named.Obj().Pkg())

	var why string
	switch {
	case awaitedFailed:
		why = "until the errors of " + wt.on.c.Name + " are fixed"
	case slices.ContainsFunc(awaitedWaits, func(w wait) bool { return w.on.c.Pos == c.Pos }):
		why = "as " + wt.on.c.Name + " needs what " + c.ctor + " builds in turn"
	default:
		why = "as it waits for another constructor in turn"
	}
	d := diag.Errorf(wt.root.Pos, "%s is to come from %s, which cannot be generated %s", what, name, why)
	if len(wt.via) > 1 {
		d = d.WithHints("via " + strings.Join(wt.via, " → "))
	}
	return d
}

// isOurs returns the constructor of the run p is, if it is one. It tells by
// the function itself, not by its name, which the constructor an earlier run
// generated shares.
func (w *world) isOurs(p provider) (*ourCtor, bool) {
	for _, g := range w.ours {
		if g.p.fn == p.fn {
			return g, true
		}
	}
	return nil, false
}
