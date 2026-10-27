package gen

import (
	"cmp"
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
	tv, err := types.Eval(pkg.Fset, pkg.Types, token.NoPos, expr)
	if err != nil {
		return nil, fmt.Errorf("eval %s: %w", expr, err)
	}
	var names []string
	for _, p := range newIndex(pkg.Fset).lookup(pkg.Types, tv.Type) {
		names = append(names, p.fn.Name())
	}
	return names, nil
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
