package gen

import (
	"fmt"
	"go/types"
	"slices"
	"strings"

	"github.com/go-kanna/internal/gofile"
)

// generator names the tool in the header of the file it writes.
const generator = "kanna-di"

// keywords cannot name a variable, though a type such as Type would give one.
var keywords = strings.Fields(`break case chan const continue default defer else fallthrough for func go goto if
	import interface map package range return select struct switch type var`)

// emit renders the constructors of plans into a file of the package pkgName at
// pkgPath. declared are the top-level names the package declares outside the
// file, which nothing the file introduces may take.
func emit(pkgPath, pkgName string, plans []plan, declared []string) *gofile.File {
	f := gofile.New(generator, pkgPath, pkgName)
	f.Reserve(declared...)
	f.Reserve(keywords...)
	// A variable named nil or error would break the body, which writes both.
	f.Reserve(types.Universe.Names()...)
	for _, p := range plans {
		f.Reserve(p.c.ctor)
		if p.c.must != "" {
			f.Reserve(p.c.must)
		}
	}

	// Naming the imports first lets them keep the names of their packages, as
	// the variables give way to them.
	for _, p := range plans {
		importAll(f, p)
	}
	for _, p := range plans {
		e := emitter{f: f, p: p, own: p.c.Named.Obj().Pkg()}
		e.constructor()
		if p.c.must != "" {
			e.must()
		}
	}
	return f
}

// importAll imports every package the constructor of p refers to.
func importAll(f *gofile.File, p plan) {
	types.TypeString(p.c.returns, f.Qualifier)
	for _, prm := range p.params {
		types.TypeString(prm.typ, f.Qualifier)
	}
	for _, s := range p.steps {
		if s.kind == callStep {
			f.Import(s.p.fn.Pkg().Path(), s.p.fn.Pkg().Name())
		}
	}
}

// emitter writes the constructor of one plan, and its must variant.
type emitter struct {
	f   *gofile.File
	p   plan
	own *types.Package

	// params and names are what the constructor calls its parameters and the
	// value of each step.
	params, names []string
}

// constructor writes the constructor:
//
//	// newServer initializes dependencies and constructs server.
//	func newServer() (*server, error) {
//		db, err := openDB()
//		if err != nil {
//			return nil, err
//		}
//		users := newUsers(db)
//
//		return &server{
//			users: users,
//		}, nil
//	}
func (e *emitter) constructor() {
	scope := e.f.Scope()
	e.params = make([]string, len(e.p.params))
	for i, prm := range e.p.params {
		e.params[i] = scope.Name(prm.name)
	}
	errName := ""
	if e.p.errs {
		errName = scope.Name("err")
	}

	fmt.Fprintf(e.f, "// %s initializes dependencies and constructs %s.\n", e.p.c.ctor, e.p.c.Name)
	fmt.Fprintf(e.f, "func %s(%s) %s {\n", e.p.c.ctor, e.signature(), e.results(e.p.cleanup, e.p.errs))

	var cleanups []string
	uses := e.uses()
	e.names = make([]string, len(e.p.steps))
	wrote := false
	for i, s := range e.p.steps {
		switch {
		case s.kind == paramStep:
			e.names[i] = e.params[s.param]
			continue
		case s.kind == literalStep && uses[i] == 1:
			// Written where it goes, rather than as a variable named after
			// its type, which the package declares.
			e.names[i] = e.literal(s.lit.c, s.lit.pointer, s.lit.fields)
			continue
		}
		e.names[i] = scope.Name(s.name)
		wrote = true

		switch s.kind {
		case callStep:
			lhs := []string{e.names[i]}
			if s.p.cleanup {
				cleanups = append(cleanups, scope.Name("cleanup"))
				lhs = append(lhs, cleanups[len(cleanups)-1])
			}
			if s.p.errs {
				lhs = append(lhs, errName)
			}
			fmt.Fprintf(e.f, "\t%s := %s\n", strings.Join(lhs, ", "), e.call(s))
			if s.p.errs {
				// A provider failing returns no cleanup of its own.
				before := cleanups
				if s.p.cleanup {
					before = cleanups[:len(cleanups)-1]
				}
				e.unwind(before, errName)
			}
		case fieldStep:
			fmt.Fprintf(e.f, "\t%s := %s.%s\n", e.names[i], e.names[s.of], strings.Join(s.path, "."))
		case literalStep:
			fmt.Fprintf(e.f, "\t%s := %s\n", e.names[i], e.literal(s.lit.c, s.lit.pointer, s.lit.fields))
		case paramStep:
		}
	}

	_, pointer := types.Unalias(e.p.c.returns).(*types.Pointer)
	value := !pointer && !types.IsInterface(e.p.c.returns)
	results := []string{e.literal(e.p.c, !value, e.p.fields)}
	if e.p.cleanup {
		results = append(results, combined(cleanups))
	}
	if e.p.errs {
		results = append(results, "nil")
	}
	if wrote {
		fmt.Fprint(e.f, "\n")
	}
	fmt.Fprintf(e.f, "\treturn %s\n}\n\n", strings.Join(results, ", "))
}

// must writes the must variant, which panics where the constructor would
// return an error:
//
//	// mustNewServer initializes dependencies and constructs server or panics on failure.
//	func mustNewServer() (*server, func()) {
//		v, cleanup, err := newServer()
//		if err != nil {
//			panic(err)
//		}
//		return v, cleanup
//	}
func (e *emitter) must() {
	scope := e.f.Scope()
	args := make([]string, len(e.p.params))
	for i, prm := range e.p.params {
		args[i] = scope.Name(prm.name)
	}
	e.params = args

	fmt.Fprintf(e.f, "// %s initializes dependencies and constructs %s or panics on failure.\n", e.p.c.must, e.p.c.Name)
	fmt.Fprintf(e.f, "func %s(%s) %s {\n", e.p.c.must, e.signature(), e.results(e.p.cleanup, false))
	call := e.p.c.ctor + "(" + strings.Join(args, ", ") + ")"
	if !e.p.errs {
		fmt.Fprintf(e.f, "\treturn %s\n}\n\n", call)
		return
	}

	lhs := []string{scope.Name("v")}
	if e.p.cleanup {
		lhs = append(lhs, scope.Name("cleanup"))
	}
	errName := scope.Name("err")
	fmt.Fprintf(e.f, "\t%s, %s := %s\n", strings.Join(lhs, ", "), errName, call)
	fmt.Fprintf(e.f, "\tif %s != nil {\n\t\tpanic(%s)\n\t}\n", errName, errName)
	fmt.Fprintf(e.f, "\treturn %s\n}\n\n", strings.Join(lhs, ", "))
}

// uses counts where the value of each step goes: into a call, a field, or
// the value of another step.
func (e *emitter) uses() []int {
	n := make([]int, len(e.p.steps))
	for _, s := range e.p.steps {
		for _, a := range s.args {
			n[a]++
		}
		if s.kind == fieldStep {
			n[s.of]++
		}
		if s.lit != nil {
			for _, a := range s.lit.fields {
				n[a.step]++
			}
		}
	}
	for _, a := range e.p.fields {
		n[a.step]++
	}
	return n
}

// signature writes the parameters of the constructor, named as e.params.
func (e *emitter) signature() string {
	params := make([]string, len(e.p.params))
	for i, prm := range e.p.params {
		params[i] = e.params[i] + " " + e.typeString(prm.typ)
	}
	return strings.Join(params, ", ")
}

// results writes the results of a constructor that returns the container, and
// a cleanup and an error when it says so.
func (e *emitter) results(cleanup, errs bool) string {
	rs := []string{e.typeString(e.p.c.returns)}
	if cleanup {
		rs = append(rs, "func()")
	}
	if errs {
		rs = append(rs, "error")
	}
	if len(rs) == 1 {
		return rs[0]
	}
	return "(" + strings.Join(rs, ", ") + ")"
}

// call writes the call of the provider of s.
func (e *emitter) call(s step) string {
	args := make([]string, len(s.args))
	for i, a := range s.args {
		args[i] = e.names[a]
	}
	fn := s.p.fn.Name()
	if q := e.f.Import(s.p.fn.Pkg().Path(), s.p.fn.Pkg().Name()); q != "" {
		fn = q + "." + fn
	}
	return fn + "(" + strings.Join(args, ", ") + ")"
}

// unwind writes the return of err, after undoing what the cleanups so far
// release, the last first.
func (e *emitter) unwind(cleanups []string, errName string) {
	results := []string{e.zero()}
	if e.p.cleanup {
		results = append(results, "nil")
	}
	results = append(results, errName)

	fmt.Fprintf(e.f, "\tif %s != nil {\n", errName)
	for _, c := range slices.Backward(cleanups) {
		fmt.Fprintf(e.f, "\t\t%s()\n", c)
	}
	fmt.Fprintf(e.f, "\t\treturn %s\n\t}\n", strings.Join(results, ", "))
}

// zero writes the value the constructor returns along with an error.
func (e *emitter) zero() string {
	if _, pointer := types.Unalias(e.p.c.returns).(*types.Pointer); pointer || types.IsInterface(e.p.c.returns) {
		return "nil"
	}
	return e.typeString(e.p.c.returns) + "{}"
}

// literal writes the struct literal of c with fields, or its address.
func (e *emitter) literal(c container, pointer bool, fields []assign) string {
	var b strings.Builder
	if pointer {
		b.WriteString("&")
	}
	b.WriteString(e.typeString(c.Named))
	if len(fields) == 0 {
		b.WriteString("{}")
		return b.String()
	}
	b.WriteString("{\n")
	for _, a := range fields {
		fmt.Fprintf(&b, "%s: %s,\n", a.field, e.names[a.step])
	}
	b.WriteString("}")
	return b.String()
}

func (e *emitter) typeString(t types.Type) string {
	return types.TypeString(t, e.f.Qualifier)
}

// combined writes the cleanup releasing what cleanups do, the last first.
func combined(cleanups []string) string {
	if len(cleanups) == 1 {
		return cleanups[0]
	}
	var b strings.Builder
	b.WriteString("func() {\n")
	for _, c := range slices.Backward(cleanups) {
		fmt.Fprintf(&b, "%s()\n", c)
	}
	b.WriteString("}")
	return b.String()
}
