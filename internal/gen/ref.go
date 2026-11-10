package gen

import (
	"errors"
	"fmt"
	"go/token"
	"go/types"
	"path"
	"strings"

	"github.com/go-kanna/internal/qualifier"
	"github.com/go-kanna/internal/suggest"
)

// refError says why a with= names no provider, and, when it can tell, what was
// probably meant.
type refError struct {
	msg, hint string
}

func (e refError) Error() string {
	return e.msg
}

// resolve finds the provider r names for a value of type want, in a container
// of package own declared in a file that imports packages as imports say; test
// says the container is one of own's tests.
//
// A bare name is a function of own. A qualified one, pkg.F, is a function of
// the package the file imports as pkg, as Go reads pkg.F in code: nothing else
// is searched. The function's result only has to be assignable to want, which
// is how an interface gets its implementation.
func (x *index) resolve(
	r ref, want types.Type, own *types.Package, test bool, imports qualifier.Scope,
) (provider, error) {
	pkg := own
	if r.pkg != "" {
		var err error
		if pkg, err = qualified(r, own, imports); err != nil {
			return provider{}, err
		}
	}
	name := r.fn
	if pkg != own {
		name = r.pkg + "." + r.fn
	}

	obj := pkg.Scope().Lookup(r.fn)
	if obj == nil {
		e := refError{msg: fmt.Sprintf("package %s has no function %s", pkg.Name(), r.fn)}
		if s, ok := suggest.Closest(r.fn, funcNames(pkg, pkg == own)); ok {
			e.hint = fmt.Sprintf("did you mean %s?", s)
		}
		return provider{}, e
	}
	fn, ok := obj.(*types.Func)
	switch {
	case !ok:
		return provider{}, refError{msg: name + " is not a function"}
	case pkg != own && !fn.Exported():
		return provider{}, refError{msg: name + " is not exported"}
	case fn.Signature().TypeParams().Len() > 0:
		return provider{}, refError{msg: name + " cannot be a provider: " + genericBad}
	}

	p, ok := x.providerOf(fn)
	switch {
	case !ok:
		return provider{}, refError{msg: name +
			" cannot be a provider: its first result must be a type declared in a package, or a pointer to one"}
	case p.bad != "":
		return provider{}, refError{msg: name + " cannot be a provider: " + p.bad}
	case !p.serves(own, test):
		return provider{}, refError{msg: name + " is declared in a _test.go file, which only tests compile"}
	}

	if typeKey(p.result) != typeKey(want) && !types.AssignableTo(p.result, want) {
		got, wanted := typeString(p.result, own), typeString(want, own)
		if iface, ok := want.Underlying().(*types.Interface); ok {
			if m, _ := types.MissingMethod(p.result, iface, true); m != nil {
				return provider{}, refError{msg: fmt.Sprintf("%s returns %s, which does not implement %s (missing method %s)",
					name, got, wanted, m.Name())}
			}
		}
		return provider{}, refError{msg: fmt.Sprintf("%s returns %s, not %s", name, got, wanted)}
	}
	return p, nil
}

// qualified returns the package r.pkg names: the one the container's file
// imports as r.pkg.
func qualified(r ref, own *types.Package, imports qualifier.Scope) (*types.Package, error) {
	if namesType(r.pkg, own, imports) {
		return nil, refError{
			msg:  fmt.Sprintf("with=%s names a method; with= names a top-level function, and a method cannot be a provider", r),
			hint: "write a function that calls the method, and choose that",
		}
	}
	if !token.IsIdentifier(r.pkg) {
		return nil, refError{
			msg:  fmt.Sprintf("with=%s names its package by its path; name it as the file imports it", r),
			hint: fmt.Sprintf("with=%s.%s, importing %s as _ if nothing else uses it", path.Base(r.pkg), r.fn, r.pkg),
		}
	}
	pkg, err := imports.Lookup(r.pkg)
	if err == nil {
		return pkg, nil
	}
	e := refError{msg: err.Error()}
	if qe, ok := errors.AsType[*qualifier.Error](err); ok {
		e.hint = qe.Hint
	}
	if r.pkg == own.Name() {
		e.hint = "a function of the container's own package goes without one: with=" + r.fn
	}
	return nil, e
}

// namesType reports whether s, what precedes the function's name, is the
// receiver of a method expression: (*T), T, or pkg.T.
func namesType(s string, own *types.Package, imports qualifier.Scope) bool {
	if strings.ContainsAny(s, "()*") {
		return true
	}
	pkg, name := own, s
	if q, n, ok := strings.Cut(s, "."); ok {
		p, err := imports.Lookup(q)
		if err != nil {
			return false
		}
		pkg, name = p, n
	}
	_, ok := pkg.Scope().Lookup(name).(*types.TypeName)
	return ok
}

// funcNames lists the functions of pkg that a with= could name: its exported
// ones, or all of them for the container's own package.
func funcNames(pkg *types.Package, own bool) []string {
	var names []string
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		if _, ok := scope.Lookup(name).(*types.Func); ok && (own || token.IsExported(name)) {
			names = append(names, name)
		}
	}
	return names
}
