package gen

import (
	"fmt"
	"go/token"
	"go/types"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/go-kanna/internal/diag"
	"github.com/go-kanna/internal/directive"
	"github.com/go-kanna/internal/packages"
	"github.com/go-kanna/internal/qualifier"
	"github.com/go-kanna/internal/scan"
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
// container being planned on, and outer holds the types the containers
// around c take, choose, or find in a struct they embed.
//
// A container built in place does not take a type the containers around it
// say where to get, so that what they take, choose, or embed overrides its
// arguments, as it overrides its with=.
func (pl *planner) collect(c container, top bool, path []string, outer map[string]bool) {
	for _, f := range c.fields {
		switch f.source {
		case fromArg:
			if k := typeKey(f.Type); !outer[k] && !hasKey(pl.args, k) {
				pl.args[k] = len(pl.pl.params)
				pl.pl.params = append(pl.pl.params, param{name: f.paramName(), typ: f.Type, spelled: f.param != ""})
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
			if f.source == fromType {
				pl.zeros[typeKey(f.Type)] = true
			}
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
				pl.collect(inner, false, append(slices.Clip(path), f.Name), pl.declaredBy(c, outer))
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

// declaredBy returns outer with the types c takes, chooses, or finds in a
// struct it embeds without building it in place.
func (pl *planner) declaredBy(c container, outer map[string]bool) map[string]bool {
	ks := maps.Clone(outer)
	for _, f := range c.fields {
		if f.zero() {
			continue
		}
		if f.source == fromArg || f.source == fromRef {
			ks[typeKey(f.Type)] = true
		}
		if _, inPlace := pl.inlined(f); f.Embedded && !inPlace || f.blank() {
			for k := range promotedOf(f.Type, pl.own) {
				ks[k] = true
			}
		}
	}
	return ks
}

// fill works out the field values of c, a container being planned or built in
// place, which path names. top says it is the one being planned, whose fields
// diagnostics are reported at; those of a container built in place go to the
// field embedding it.
//
// The containers built in place come first, so that each serves its type to
// the other fields whatever their order, as what a container embeds comes
// before any function.
func (pl *planner) fill(c container, owner string, top bool, path []string) []assign {
	steps := make([]int, len(c.fields))
	for _, inPlace := range []bool{true, false} {
		for i, f := range c.fields {
			inner, ok := pl.inlined(f)
			if ok != inPlace {
				continue
			}
			steps[i] = -1
			if f.blank() || f.zero() {
				continue
			}
			if top {
				pl.root = f.Field
			}
			via := owner + "." + f.Name

			switch e := pl.embedOf(via); {
			case ok && slices.Contains(path, inner.Name):
				// reported by collect
			case ok:
				steps[i] = pl.build(inner, f, via, append(slices.Clip(path), f.Name))
			case e != nil:
				steps[i] = pl.embedStep(e)
			default:
				steps[i] = pl.need(f.Type, []string{via})
			}
		}
	}

	var fields []assign
	for i, f := range c.fields {
		if steps[i] >= 0 {
			fields = append(fields, assign{field: f.Name, step: steps[i]})
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

// chooseInner records the with= f of inner, a container built in place, which
// names its provider as inner's own file and package would. What it names
// wrong is for the plan of inner itself to report.
func (pl *planner) chooseInner(f field, inner container) {
	own := inner.Named.Obj().Pkg()
	ch := innerChoice{via: inner.Name + "." + f.Name + " with=" + f.ref.String()}
	if p, err := pl.w.x.resolve(f.ref, f.Type, own, inner.test, qualifier.Of(inner.File, own)); err == nil {
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
// of one type at one depth are all kept, for the lookup to call ambiguous. A
// field of a type without a name of its own is left out, as nothing hands one
// out by type. An embedded struct is looked into only when from, the
// container's package, can reach it.
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
				if k := typeKey(v.Type()); v.Exported() && providable(v.Type()) && !hasKey(out, k) {
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

// hasExportedFields reports whether t is, or points to, a struct with an
// exported field of its own.
func hasExportedFields(t types.Type) bool {
	st := structOf(t)
	if st == nil {
		return false
	}
	for v := range st.Fields() {
		if v.Exported() {
			return true
		}
	}
	return false
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

// checkKept reports each container of another package the container embeds
// and has its constructor build, which keeps a value in a field the container
// cannot read while the container makes a value of that type itself. The two
// would be different values, where a container hands out one of a type. A
// value passed to the constructor is the container's own, and serves both.
func (pl *planner) checkKept() {
	for _, e := range pl.embeds {
		if !e.done || e.step < 0 {
			continue
		}
		s := pl.pl.steps[e.step]
		n := namedOf(e.f.Type)
		if s.kind != callStep || n == nil || !pl.w.marked(n) {
			continue
		}
		for _, t := range pl.w.kept(n, map[string]bool{}) {
			k := typeKey(t)
			o, ok := pl.byType[k]
			if hasKey(e.byType, k) || !ok || pl.pl.steps[o].kind == zeroStep || slices.Contains(s.args, o) {
				continue
			}
			pl.diags = append(pl.diags, diag.Errorf(e.f.Pos,
				"%s keeps a %s of its own that %s cannot read, so %s would make a second one",
				pl.typeString(n), pl.typeString(t), pl.c.Name, pl.c.ctor).WithHints(
				fmt.Sprintf("for one %s, have %s keep it in an exported field, which %s then takes it from",
					pl.typeString(t), pl.typeString(n), pl.c.Name),
				oneValueHint))
		}
	}
}

// kept returns the types of the values the container n keeps, in the order of
// its fields: those of its fields but blank ones and those it leaves at their
// zero value, of the containers it embeds, and of the exported fields of the
// other structs it embeds. seen holds the containers already looked into.
func (w *world) kept(n *types.Named, seen map[string]bool) []types.Type {
	seen[typeKey(n)] = true
	st, _ := n.Underlying().(*types.Struct)
	var ts []types.Type
	for i := range st.NumFields() {
		v := st.Field(i)
		if v.Name() == "_" || reflect.StructTag(st.Tag(i)).Get(tagKey) == "-" {
			continue
		}
		ts = append(ts, v.Type())
		if !v.Embedded() {
			continue
		}
		if sub := namedOf(v.Type()); sub != nil && w.marked(sub) {
			if !seen[typeKey(sub)] {
				ts = append(ts, w.kept(sub, seen)...)
			}
			continue
		}
		byType := promotedOf(v.Type(), n.Obj().Pkg())
		for _, k := range slices.Sorted(maps.Keys(byType)) {
			for _, pr := range byType[k] {
				ts = append(ts, pr.typ)
			}
		}
	}
	return ts
}

// namedOf returns the named type t is, or points to, or nil.
func namedOf(t types.Type) *types.Named {
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		t = p.Elem()
	}
	n, _ := types.Unalias(t).(*types.Named)
	return n
}

// markedIn returns whether a struct of pkgs, or of a package they import, is
// marked //kanna:container, as the source the load parsed says. A package
// read from export data alone has none.
func markedIn(fset *token.FileSet, pkgs []*packages.Package) func(*types.Named) bool {
	byPath := make(map[string]*packages.Package)
	var visit func(*packages.Package)
	visit = func(p *packages.Package) {
		if hasKey(byPath, p.PkgPath) {
			return
		}
		byPath[p.PkgPath] = p
		for _, q := range p.Imports {
			visit(q)
		}
	}
	for _, p := range pkgs {
		visit(p)
	}

	marked := make(map[string]map[string]bool)
	return func(n *types.Named) bool {
		if n.Obj().Pkg() == nil {
			return false
		}
		path := n.Obj().Pkg().Path()
		names, ok := marked[path]
		if !ok {
			names = make(map[string]bool)
			if p := byPath[path]; p != nil {
				for _, s := range scan.Structs([]*packages.Package{p}) {
					d, _ := directive.Find(fset, s.Doc, directiveKey)
					names[s.Name] = d.Found
				}
			}
			marked[path] = names
		}
		return names[n.Obj().Name()]
	}
}
