package gen

import (
	"cmp"
	"go/token"
	"go/types"
	"slices"
	"strings"
	"unicode"
)

// provider is a top-level function that returns a type kanna-di can look a
// provider up by.
type provider struct {
	fn *types.Func

	// result is the first result, the value the function provides.
	result types.Type

	// params are what a call passes, a variadic parameter left out: a call
	// may leave it empty, and nothing could provide its slice anyway.
	params []types.Type

	// errs and cleanup report that the function also returns an error, or a
	// func() that releases what it opened.
	errs, cleanup bool

	pos token.Position

	// test reports whether the function is declared in a _test.go file,
	// which compiles into its package's tests and nowhere else.
	test bool

	// bad, when not "", says why the function cannot be called as a
	// provider although it returns the type. It is kept to say so when
	// nothing else provides the type.
	bad string
}

// index finds the providers in a package by the type they provide, reading
// each package once, when it is first asked about.
type index struct {
	fset *token.FileSet
	pkgs map[*types.Package]map[string][]provider
}

func newIndex(fset *token.FileSet) *index {
	return &index{fset: fset, pkgs: make(map[*types.Package]map[string][]provider)}
}

// lookup returns the functions of pkg that return t, in the order they are
// declared, the bad ones included.
func (x *index) lookup(pkg *types.Package, t types.Type) []provider {
	byType, ok := x.pkgs[pkg]
	if !ok {
		byType = x.read(pkg)
		x.pkgs[pkg] = byType
	}
	return byType[typeKey(t)]
}

// read sorts the top-level functions of pkg by the type they return.
//
// A must variant is left out: MustNewDeps beside NewDeps returns the same type,
// and calling NewDeps instead lets its error reach the constructor rather than
// panic. So is a generic function, whose type arguments nothing could choose.
func (x *index) read(pkg *types.Package) map[string][]provider {
	scope := pkg.Scope()
	byType := make(map[string][]provider)
	for _, name := range scope.Names() {
		fn, ok := scope.Lookup(name).(*types.Func)
		if !ok || fn.Signature().TypeParams().Len() > 0 || isMustOf(scope, fn) {
			continue
		}
		if p, ok := x.providerOf(fn); ok {
			k := typeKey(p.result)
			byType[k] = append(byType[k], p)
		}
	}

	for _, ps := range byType {
		slices.SortFunc(ps, func(a, b provider) int {
			return cmp.Or(cmp.Compare(a.pos.Filename, b.pos.Filename), cmp.Compare(a.pos.Offset, b.pos.Offset))
		})
	}
	return byType
}

// providerOf reads fn as a provider, reporting false when its first result is
// not a type a provider can be looked up by.
func (x *index) providerOf(fn *types.Func) (provider, bool) {
	sig := fn.Signature()
	results := sig.Results()
	if results.Len() == 0 || !providable(results.At(0).Type()) {
		return provider{}, false
	}

	pos := x.fset.Position(fn.Pos())
	p := provider{
		fn:     fn,
		result: results.At(0).Type(),
		pos:    pos,
		test:   strings.HasSuffix(pos.Filename, "_test.go"),
	}

	params := sig.Params()
	for i := range params.Len() {
		if !sig.Variadic() || i < params.Len()-1 {
			p.params = append(p.params, params.At(i).Type())
		}
	}

	rest := make([]types.Type, results.Len()-1)
	for i := range rest {
		rest[i] = results.At(i + 1).Type()
	}
	switch {
	case len(rest) == 0:
	case len(rest) == 1 && isError(rest[0]):
		p.errs = true
	case len(rest) == 1 && isCleanup(rest[0]):
		p.cleanup = true
	case len(rest) == 2 && isCleanup(rest[0]) && isError(rest[1]):
		p.cleanup, p.errs = true, true
	case slices.ContainsFunc(rest, func(t types.Type) bool { return isFunc(t) && !isCleanup(t) }):
		p.bad = "a cleanup must be a func(), with neither parameters nor results"
	default:
		p.bad = "what follows its first result must be a func() cleanup, an error, or both in that order"
	}
	return p, true
}

// providable reports whether a provider can be looked up by t: a type declared
// in a package, or a pointer to one. A string, or a []byte, would match any
// function returning one, which a container's package may well hold for other
// reasons.
func providable(t types.Type) bool {
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := types.Unalias(t).(*types.Named)
	return ok && named.Obj().Pkg() != nil
}

// isMustOf reports whether fn is the must variant of another function in scope:
// MustNewServer of NewServer, or mustNewServer of newServer, returning the same
// type.
func isMustOf(scope *types.Scope, fn *types.Func) bool {
	var base string
	switch name := fn.Name(); {
	case strings.HasPrefix(name, "Must"):
		base = strings.TrimPrefix(name, "Must")
	case strings.HasPrefix(name, "must"):
		base = lowerFirstRune(strings.TrimPrefix(name, "must"))
	default:
		return false
	}

	other, ok := scope.Lookup(base).(*types.Func)
	if !ok {
		return false
	}
	a, b := fn.Signature().Results(), other.Signature().Results()
	return a.Len() > 0 && b.Len() > 0 && typeKey(a.At(0).Type()) == typeKey(b.At(0).Type())
}

func isError(t types.Type) bool {
	return types.Identical(t, types.Universe.Lookup("error").Type())
}

// isCleanup reports whether t is a func() or a type declared as one.
func isCleanup(t types.Type) bool {
	sig, ok := t.Underlying().(*types.Signature)
	return ok && sig.Params().Len() == 0 && sig.Results().Len() == 0 && sig.TypeParams().Len() == 0
}

func isFunc(t types.Type) bool {
	_, ok := t.Underlying().(*types.Signature)
	return ok
}

// typeKey spells t, aliases resolved, with packages written by their import
// paths, so that two spellings of one type share a key, and types of two
// packages of one name do not.
func typeKey(t types.Type) string {
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		return "*" + typeKey(p.Elem())
	}
	return types.TypeString(t, (*types.Package).Path)
}

// lowerFirstRune undoes upperFirst, as mustOf applied it.
func lowerFirstRune(s string) string {
	r := []rune(s)
	if len(r) > 0 {
		r[0] = unicode.ToLower(r[0])
	}
	return string(r)
}
