package gen

import (
	"fmt"
	"go/token"
	"go/types"
	"slices"
	"strings"

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
// of package own; test says the container is one of own's tests.
//
// A bare name is a function of own. A qualified one is looked for in pkgs, the
// packages with= may name: those of the user's module and the one declaring
// want. The function's result only has to be assignable to want, which is how
// an interface gets its implementation.
func (x *index) resolve(
	r ref, want types.Type, own *types.Package, test bool, pkgs []*types.Package,
) (provider, error) {
	pkg := own
	if r.pkg != "" {
		var err error
		if pkg, err = pick(r.pkg, want, pkgs); err != nil {
			return provider{}, err
		}
		// A package loaded with its tests comes in two copies, and own is the
		// one that holds the functions of its test files too.
		if pkg.Path() == own.Path() {
			pkg = own
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
		return provider{}, refError{msg: name +
			" cannot be a provider: it is generic, and nothing would choose its type arguments"}
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

// pick returns the one package of pkgs that qualifier names: by its full import
// path, by the end of it, or by its name.
func pick(qualifier string, want types.Type, pkgs []*types.Package) (*types.Package, error) {
	var (
		matched []*types.Package
		names   []string
	)
	for _, p := range pkgs {
		if slices.ContainsFunc(matched, func(m *types.Package) bool { return m.Path() == p.Path() }) {
			continue
		}
		if qualifies(qualifier, p.Path(), p.Name()) {
			matched = append(matched, p)
		}
		names = append(names, p.Name())
	}

	switch len(matched) {
	case 1:
		return matched[0], nil
	case 0:
		e := refError{msg: fmt.Sprintf("no package %s; with= looks in your module and in the package declaring %s",
			qualifier, typeString(want, nil))}
		if s, ok := suggest.Closest(qualifier, names); ok {
			e.hint = fmt.Sprintf("did you mean %s?", s)
		}
		return nil, e
	default:
		paths := make([]string, len(matched))
		for i, m := range matched {
			paths[i] = m.Path()
		}
		slices.Sort(paths)
		return nil, refError{
			msg:  fmt.Sprintf("%s could be any of %s", qualifier, strings.Join(paths, ", ")),
			hint: "write as much of the import path as it takes to tell them apart",
		}
	}
}

// qualifies reports whether qualifier, as written before the function of a
// with=, names the package at path declaring itself name.
func qualifies(qualifier, path, name string) bool {
	return path == qualifier || strings.HasSuffix(path, "/"+qualifier) || name == qualifier
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
