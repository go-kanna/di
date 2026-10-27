package gen

import (
	"fmt"
	"go/token"
	"reflect"

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
