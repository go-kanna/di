package gen

import (
	"fmt"
	"go/types"
	"maps"
	"slices"
	"strings"

	"github.com/go-kanna/internal/diag"
)

// embedded is a struct a container embeds, by value or by pointer, without
// building it in place, or declares as a blank field, which is not stored. It
// is resolved like any field, and its exported fields then serve the
// container.
type embedded struct {
	f field

	// via names the field from the container on, as "app.Deps", or
	// "app._ *infra.Deps" for a blank one.
	via string

	// byType holds the fields serving the container, by the key of their
	// type. A type with more than one is held at a depth with more than one.
	byType map[string][]promoted

	step int

	// busy reports that the struct is being worked out, done that it has
	// been.
	busy, done bool
}

// promoted is a field of an embedded struct, reached along path.
type promoted struct {
	path []string
	typ  types.Type
}

// innerChoice is a with= of a container built in place.
type innerChoice struct {
	// p is what it chose, or nil when it names none, which the container's
	// own plan reports.
	p *provider

	// via names it, as "server.with=postgres.NewUserRepository".
	via string
}

// collect sets out what c and the containers it builds in place take and
// choose, and the structs they embed otherwise. path names c from the
// container being planned on.
//
// The container's own arguments and with= come before those of the containers
// it builds in place, so that a type it takes or chooses itself overrides
// theirs.
func (pl *planner) collect(c container, top bool, path []string) {
	for _, f := range c.fields {
		switch f.source {
		case fromArg:
			if k := typeKey(f.Type); !hasKey(pl.args, k) {
				pl.args[k] = len(pl.pl.params)
				pl.pl.params = append(pl.pl.params, param{name: f.paramName(), typ: f.Type})
			}
		case fromRef:
			if top {
				pl.choose(f)
			} else {
				pl.chooseInner(f, c)
			}
		case fromType, fromNowhere:
		}
	}

	for _, f := range c.fields {
		if f.zero() {
			continue
		}
		via := strings.Join(path, ".") + "." + f.Name
		switch {
		case f.Embedded:
			if inner, ok := pl.inlined(f); ok {
				if slices.Contains(path, inner.Name) {
					pl.diags = append(pl.diags, fieldDiag(f.Field,
						fmt.Errorf("%s cannot be built in place: it embeds itself", inner.Name)))
					continue
				}
				pl.collect(inner, false, append(slices.Clip(path), f.Name))
				continue
			}
			pl.embeds = append(pl.embeds, &embedded{f: f, via: via, byType: promotedOf(f.Type, pl.own)})
		case f.blank():
			// Nothing is stored in a blank field, so a struct there serves
			// through its fields alone, without them or its methods promoted
			// onto the container.
			if byType := promotedOf(f.Type, pl.own); len(byType) > 0 {
				pl.embeds = append(pl.embeds, &embedded{f: f, via: via + " " + pl.typeString(f.Type), byType: byType})
			}
		}
	}
}

// fill works out the field values of c, a container being planned or built in
// place, which path names. top says it is the one being planned, whose fields
// diagnostics are reported at; those of a container built in place go to the
// field embedding it.
func (pl *planner) fill(c container, owner string, top bool, path []string) []assign {
	var fields []assign
	for _, f := range c.fields {
		if f.blank() || f.zero() {
			continue
		}
		if top {
			pl.root = f.Field
		}
		via := owner + "." + f.Name

		var s int
		if inner, ok := pl.inlined(f); ok {
			if slices.Contains(path, inner.Name) {
				continue // reported by collect
			}
			s = pl.build(inner, f, via, append(slices.Clip(path), f.Name))
		} else if e := pl.embedOf(via); e != nil {
			s = pl.embedStep(e)
		} else {
			s = pl.need(f.Type, []string{via})
		}
		if s >= 0 {
			fields = append(fields, assign{field: f.Name, step: s})
		}
	}
	return fields
}

// build adds the step building inner in place, for the field f embedding it.
// The value also serves whatever else needs its type.
func (pl *planner) build(inner container, f field, via string, path []string) int {
	_, pointer := types.Unalias(f.Type).(*types.Pointer)
	lit := &literal{c: inner, pointer: pointer}
	lit.fields = pl.fill(inner, via, false, path)
	s := pl.add(step{kind: literalStep, typ: f.Type, name: lowerFirst(inner.Name), lit: lit})
	pl.byType[typeKey(f.Type)] = s
	return s
}

// inlined returns the container f embeds when it is to be built in place: a
// container of the same package, embedded without a di tag. One of another
// package is built by its own constructor, as its unexported fields could not
// be set from here.
func (pl *planner) inlined(f field) (container, bool) {
	if !f.Embedded || f.source != fromType {
		return container{}, false
	}
	t := types.Unalias(f.Type)
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	c, ok := pl.w.containers[typeKey(t)]
	return c, ok && c.PkgPath == pl.c.PkgPath
}

func (pl *planner) embedOf(via string) *embedded {
	for _, e := range pl.embeds {
		if e.via == via {
			return e
		}
	}
	return nil
}

// embedStep returns the step making the value of e. Its own fields serve no
// need while it is worked out, since they would come from it.
func (pl *planner) embedStep(e *embedded) int {
	if e.done {
		return e.step
	}
	root := pl.root
	pl.root, e.busy = e.f.Field, true
	e.step = pl.need(e.f.Type, []string{e.via})
	pl.root, e.busy, e.done = root, false, true
	return e.step
}

// chooseInner records the with= f of inner, a container built in place. What
// it names wrong is for the plan of inner itself to report.
func (pl *planner) chooseInner(f field, inner container) {
	refs := pl.w.refs
	if home := homeOf(f.Type); home != nil {
		refs = append(slices.Clip(refs), home)
	}
	ch := innerChoice{via: inner.Name + "." + f.Name + " with=" + f.ref.String()}
	if p, err := pl.w.x.resolve(f.ref, f.Type, pl.own, pl.c.test, refs); err == nil {
		ch.p = &p
	}
	k := typeKey(f.Type)
	pl.innerChosen[k] = append(pl.innerChosen[k], ch)
}

// fromEmbedded finds t in what the container embeds: a field of a struct it
// embeds, or the with= of a container it builds in place. It reports false
// when none has one, so that the search goes on. A with= that names nothing
// chooses nothing here, and is reported by the plan of its own container.
func (pl *planner) fromEmbedded(t types.Type, k string, via []string) (int, bool) {
	type candidate struct {
		desc string
		e    *embedded
		pr   promoted
		p    *provider
	}
	var cands []candidate
	for _, e := range pl.embeds {
		if e.busy {
			continue
		}
		for _, pr := range e.byType[k] {
			cands = append(cands, candidate{desc: e.desc(pr), e: e, pr: pr})
		}
	}
	named := false
	for _, ch := range pl.innerChosen[k] {
		if ch.p == nil {
			named = true
			continue
		}
		// Two containers built in place may choose the same provider.
		if !slices.ContainsFunc(cands, func(c candidate) bool {
			return c.p != nil && c.p.fn.FullName() == ch.p.fn.FullName()
		}) {
			cands = append(cands, candidate{desc: ch.via, p: ch.p})
		}
	}

	switch len(cands) {
	case 0:
		return -1, named
	case 1:
		c := cands[0]
		if c.e == nil {
			return pl.call(*c.p, via), true
		}
		of := pl.embedStep(c.e)
		if of < 0 {
			return -1, true
		}
		return pl.add(step{kind: fieldStep, typ: c.pr.typ, name: lowerFirst(c.pr.path[len(c.pr.path)-1]),
			of: of, path: c.pr.path}), true
	default:
		d := diag.Errorf(pl.root.Pos, "%d of what %s embeds provide %s", len(cands), pl.c.Name, pl.typeString(t))
		for _, c := range cands {
			d = d.WithHints(c.desc)
		}
		if len(via) > 1 {
			d = d.WithHints("via " + strings.Join(via, " → "))
		}
		pl.diags = append(pl.diags, d.WithHints(fmt.Sprintf(
			"choose a provider with _ %s `di:\"with=...\"`, or take it with di:\"arg\"", pl.typeString(t))))
		return -1, true
	}
}

// desc names pr for a hint: App.Deps.DB for a field of an embedded struct, and
// DB of App._ *Deps for one of a blank field, which has no name to select it
// by.
func (e *embedded) desc(pr promoted) string {
	if e.f.blank() {
		return strings.Join(pr.path, ".") + " of " + e.via
	}
	return e.via + "." + strings.Join(pr.path, ".")
}

// assigns lists every field the plan fills: those of the container, and those
// of the containers it builds in place.
func (pl *planner) assigns() []assign {
	as := slices.Clone(pl.pl.fields)
	for _, s := range pl.pl.steps {
		if s.lit != nil {
			as = append(as, s.lit.fields...)
		}
	}
	return as
}

// promotedOf returns the exported fields of the struct t is, or points to, by
// the key of their type, those promoted from structs it embeds included. A
// shallower field hides a deeper one of its type, as a selector would; fields
// of one type at one depth are all kept, for the lookup to call ambiguous. An
// embedded struct is looked into only when from, the container's package, can
// reach it.
func promotedOf(t types.Type, from *types.Package) map[string][]promoted {
	st := structOf(t)
	if st == nil {
		return nil
	}

	type frame struct {
		st   *types.Struct
		path []string
	}
	out := make(map[string][]promoted)
	visited := map[string]bool{typeKey(t): true}
	for level := []frame{{st: st}}; len(level) > 0; {
		found := make(map[string][]promoted)
		var next []frame
		for _, fr := range level {
			for v := range fr.st.Fields() {
				path := append(slices.Clip(fr.path), v.Name())
				if k := typeKey(v.Type()); v.Exported() && !hasKey(out, k) {
					found[k] = append(found[k], promoted{path: path, typ: v.Type()})
				}
				reachable := v.Exported() || v.Pkg().Path() == from.Path()
				if sub := structOf(v.Type()); v.Embedded() && reachable && sub != nil && !visited[typeKey(v.Type())] {
					visited[typeKey(v.Type())] = true
					next = append(next, frame{st: sub, path: path})
				}
			}
		}
		maps.Copy(out, found)
		level = next
	}
	return out
}

// structOf returns the struct t is, or points to, or nil.
func structOf(t types.Type) *types.Struct {
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		t = p.Elem()
	}
	st, _ := types.Unalias(t).Underlying().(*types.Struct)
	return st
}

func hasKey[V any](m map[string]V, k string) bool {
	_, ok := m[k]
	return ok
}
