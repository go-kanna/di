package gen

import (
	"cmp"
	"errors"
	"fmt"
	"go/token"
	"go/types"
	"reflect"
	"slices"
	"strings"

	"github.com/go-kanna/internal/packages"
	"github.com/go-kanna/internal/scan"
)

// ParseTag is parseTag spelled out: "type", "-", "arg", "arg=<param>", or
// "with <pkg> <fn>", with "." as the package for a function of the
// container's package.
func ParseTag(st reflect.StructTag) (string, error) {
	t, err := parseTag(st)
	if err != nil {
		return "", err
	}
	return spell(t, t.param), nil
}

// Container is a container spelled out. Returns is written as in the
// container's package, and each of Fields as "<name>: <tag>", with the tag as
// ParseTag spells it, an argument always with its parameter's name, and
// "(embedded) " before an embedded field's name.
type Container struct {
	Name, Ctor, Must, Returns string
	Test                      bool
	Fields                    []string
}

// Containers is containers on the structs of pkgs, spelled out, with the
// diagnostics as strings.
func Containers(fset *token.FileSet, pkgs []*packages.Package) ([]Container, []string) {
	cs, ds := containers(fset, scan.Structs(pkgs))

	spelled := make([]Container, len(cs))
	for i, c := range cs {
		fields := make([]string, len(c.fields))
		for j, f := range c.fields {
			name := f.Name
			if f.Embedded {
				name = "(embedded) " + name
			}
			param := ""
			if f.source == fromArg {
				param = f.paramName()
			}
			fields[j] = name + ": " + spell(f.tag, param)
		}
		spelled[i] = Container{
			Name:    c.Name,
			Ctor:    c.ctor,
			Must:    c.must,
			Returns: typeString(c.returns, c.Named.Obj().Pkg()),
			Test:    c.test,
			Fields:  fields,
		}
	}

	diags := make([]string, len(ds))
	for i, d := range ds {
		diags[i] = d.String()
	}
	return spelled, diags
}

// Providers is every function of pkg that lookup can find, in the order they
// are declared, each spelled as its signature would be with the variadic
// parameter left out, such as "openDB(Config) *DB, func(), error", or as
// "openDB: <why it is bad>", and followed by " (test)" for one declared in a
// test file.
func Providers(pkg *packages.Package) []string {
	x := newIndex(pkg.Fset)
	var all []provider
	for _, ps := range x.read(pkg.Types) {
		all = append(all, ps...)
	}
	slices.SortFunc(all, func(a, b provider) int {
		return cmp.Or(cmp.Compare(a.pos.Filename, b.pos.Filename), cmp.Compare(a.pos.Offset, b.pos.Offset))
	})

	spelled := make([]string, len(all))
	for i, p := range all {
		s := p.fn.Name()
		if p.bad != "" {
			s += ": " + p.bad
		} else {
			params := make([]string, len(p.params))
			for j, t := range p.params {
				params[j] = typeString(t, pkg.Types)
			}
			s += "(" + strings.Join(params, ", ") + ") " + typeString(p.result, pkg.Types)
			if p.cleanup {
				s += ", func()"
			}
			if p.errs {
				s += ", error"
			}
		}
		if p.test {
			s += " (test)"
		}
		spelled[i] = s
	}
	return spelled
}

// Lookup is lookup of the type expr names in pkg, the functions it returns
// spelled by their names.
func Lookup(pkg *packages.Package, expr string) ([]string, error) {
	t, err := evalType(pkg, expr)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range newIndex(pkg.Fset).lookup(pkg.Types, t) {
		names = append(names, p.fn.Name())
	}
	return names, nil
}

// Resolve is resolve of written, a with= ref, for a value of the type expr
// names in pkg, with pkg, the packages it imports, and others as those a with=
// may name. It spells the provider found as "<path>.<name>", and follows the
// message of an error with its hint after "; ".
func Resolve(pkg *packages.Package, expr, written string, others ...*packages.Package) (string, error) {
	t, err := evalType(pkg, expr)
	if err != nil {
		return "", err
	}
	r, err := parseRef(written)
	if err != nil {
		return "", err
	}

	pkgs := append([]*types.Package{pkg.Types}, pkg.Types.Imports()...)
	for _, o := range others {
		pkgs = append(pkgs, o.Types)
	}
	p, err := newIndex(pkg.Fset).resolve(r, t, pkg.Types, pkgs)
	if e, ok := errors.AsType[*refError](err); ok && e.hint != "" {
		return "", fmt.Errorf("%s; %s", e.msg, e.hint)
	} else if err != nil {
		return "", err
	}
	return p.fn.Pkg().Path() + "." + p.fn.Name(), nil
}

// Plans plans the containers of pkgs, each spelled out by describe and keyed by
// the struct's name, with the diagnostics as strings. A package is part of the
// user's module when its import path is module or starts with module and a
// slash, and every such package of pkgs is one a with= may name.
func Plans(fset *token.FileSet, pkgs []*packages.Package, module string) (map[string][]string, []string) {
	local := func(p *types.Package) bool {
		return p.Path() == module || strings.HasPrefix(p.Path(), module+"/")
	}
	w := &world{x: newIndex(fset), local: local}
	for _, p := range pkgs {
		if local(p.Types) {
			w.refs = append(w.refs, p.Types)
		}
	}

	cs, ds := containers(fset, scan.Structs(pkgs))
	w.containers = make(map[string]container, len(cs))
	for _, c := range cs {
		w.containers[typeKey(c.Named)] = c
	}
	plans := make(map[string][]string)
	for _, c := range cs {
		p, pds := planOf(w, c)
		ds = append(ds, pds...)
		plans[c.Name] = describe(p)
	}

	diags := make([]string, len(ds))
	for i, d := range ds {
		diags[i] = d.String()
	}
	return plans, diags
}

// describe spells p out a line for each step but a parameter, between its
// signature and the struct it returns:
//
//	newServer() (*server, func(), error)
//	cfg := config.Load() error
//	db := openDB(cfg) cleanup error
//	deps := infra.NewDeps()
//	cache := deps.Cache
//	base := &base{cache: cache}
//	server{users: users}
//
// Names are made unique as emit would, by a number from 2.
func describe(p plan) []string {
	own := p.c.Named.Obj().Pkg()
	seen := make(map[string]int)
	unique := func(n string) string {
		if seen[n]++; seen[n] > 1 {
			return fmt.Sprintf("%s%d", n, seen[n])
		}
		return n
	}

	params := make([]string, len(p.params))
	for i, prm := range p.params {
		params[i] = unique(prm.name) + " " + typeString(prm.typ, own)
	}
	results := []string{typeString(p.c.returns, own)}
	if p.cleanup {
		results = append(results, "func()")
	}
	if p.errs {
		results = append(results, "error")
	}
	sig := p.c.ctor + "(" + strings.Join(params, ", ") + ") " + strings.Join(results, ", ")
	if len(results) > 1 {
		sig = p.c.ctor + "(" + strings.Join(params, ", ") + ") (" + strings.Join(results, ", ") + ")"
	}
	lines := []string{sig}

	names := make([]string, len(p.steps))
	literal := func(c container, as []assign) string {
		fields := make([]string, len(as))
		for i, a := range as {
			fields[i] = a.field + ": " + names[a.step]
		}
		return c.Name + "{" + strings.Join(fields, ", ") + "}"
	}
	for i, s := range p.steps {
		if s.kind == paramStep {
			names[i] = strings.Fields(params[s.param])[0]
			continue
		}
		names[i] = unique(s.name)
		line := names[i] + " := "
		switch s.kind {
		case callStep:
			args := make([]string, len(s.args))
			for j, a := range s.args {
				args[j] = names[a]
			}
			fn := s.p.fn.Name()
			if s.p.fn.Pkg().Path() != own.Path() {
				fn = s.p.fn.Pkg().Name() + "." + fn
			}
			line += fn + "(" + strings.Join(args, ", ") + ")"
			if s.p.cleanup {
				line += " cleanup"
			}
			if s.p.errs {
				line += " error"
			}
		case fieldStep:
			line += names[s.of] + "." + strings.Join(s.path, ".")
		case literalStep:
			if s.lit.pointer {
				line += "&"
			}
			line += literal(s.lit.c, s.lit.fields)
		case paramStep:
		}
		lines = append(lines, line)
	}
	return append(lines, literal(p.c, p.fields))
}

// evalType reads the type expr names in the scope of the first file of pkg, so
// that its imports are in scope.
func evalType(pkg *packages.Package, expr string) (types.Type, error) {
	file := pkg.Syntax[0]
	tv, err := types.Eval(pkg.Fset, pkg.Types, file.Decls[len(file.Decls)-1].Pos(), expr)
	if err != nil {
		return nil, fmt.Errorf("eval %s: %w", expr, err)
	}
	return tv.Type, nil
}

// spell writes t as ParseTag does, with param as the name of an argument.
func spell(t tag, param string) string {
	switch t.source {
	case fromType:
		return "type"
	case fromNowhere:
		return "-"
	case fromArg:
		if param == "" {
			return "arg"
		}
		return "arg=" + param
	case fromRef:
		pkg := t.ref.pkg
		if pkg == "" {
			pkg = "."
		}
		return "with " + pkg + " " + t.ref.fn
	default:
		return fmt.Sprintf("source %d", t.source)
	}
}
