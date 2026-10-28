package gen

import (
	"errors"
	"fmt"
	"go/scanner"
	"go/token"
	"go/types"
	"strings"
	"unicode"

	"github.com/go-kanna/internal/diag"
	"github.com/go-kanna/internal/directive"
	"github.com/go-kanna/internal/scan"
	"github.com/go-kanna/internal/suggest"
)

// directiveKey spells the directive //kanna:container.
const directiveKey = "container"

// The files generated next to the containers: one for the containers of a
// package, one for those of its tests.
const (
	outputFile     = "di_gen.go"
	testOutputFile = "di_gen_test.go"
)

// options are what a //kanna:container line can say.
var options = []string{"name", "returns", "must"}

// container is a struct marked //kanna:container.
type container struct {
	scan.Struct

	// test reports whether the struct is declared in a _test.go file, whose
	// constructor goes to testOutputFile.
	test bool

	// ctor names the constructor, and must its must variant, or is "".
	ctor, must string

	// returns is what the constructor returns: a pointer to the struct, the
	// struct itself, or an interface the pointer implements.
	returns types.Type

	fields []field
}

// field is a field of a container, with its di tag read.
type field struct {
	scan.Field
	tag
}

// blank reports whether the field has no name, so that it only says something
// about the container: what it takes, or which provider it chooses.
func (f field) blank() bool {
	return f.Name == "_"
}

// paramName is what the constructor calls the parameter of a fromArg field:
// the name its tag gives, or one made from its type, or "" when the type has
// no name to make one from.
func (f field) paramName() string {
	if f.param != "" {
		return f.param
	}
	return nameOf(f.Type)
}

// containers returns the structs marked //kanna:container that a constructor
// can be written for, in the order scan reports them, and says what is wrong
// with the rest. output holds the files an earlier run wrote, and dir is the
// directory positions in hints are written relative to; see clash.
//
// A struct carrying di tags without the directive is warned about: nothing
// would be generated for it, and nothing else would say so.
func containers(
	fset *token.FileSet, structs []scan.Struct, dir string, output map[string]bool,
) ([]container, []diag.Diag) {
	var (
		found []container
		diags []diag.Diag
	)
	for _, s := range structs {
		d, ds := directive.Find(fset, s.Doc, directiveKey)
		diags = append(diags, ds...)
		if !d.Found {
			if hasTag(s) {
				diags = append(diags, diag.Warningf(s.Pos,
					"%s has di tags but no //kanna:%s, so nothing is generated for it", s.Name, directiveKey))
			}
			continue
		}

		c, ds := containerOf(fset, s, d)
		if diags = append(diags, ds...); !diag.HasErrors(ds) {
			found = append(found, c)
		}
	}
	return clash(fset, found, diags, dir, output)
}

// containerOf reads s, marked by d, as a container.
func containerOf(fset *token.FileSet, s scan.Struct, d directive.Directive) (container, []diag.Diag) {
	if s.Named.TypeParams().Len() > 0 {
		return container{}, []diag.Diag{diag.Errorf(s.Pos,
			"generic struct %s cannot be a container: its constructor would have no way to choose the type arguments",
			s.Name)}
	}

	c := container{Struct: s, test: strings.HasSuffix(s.Pos.Filename, "_test.go")}
	diags := c.parseOptions(fset, d)
	fields, ds := fieldsOf(s)
	c.fields = fields
	return c, append(diags, ds...)
}

// parseOptions reads the options d gives into c, setting the defaults for those
// it leaves out.
func (c *container) parseOptions(fset *token.FileSet, d directive.Directive) []diag.Diag {
	var (
		diags  []diag.Diag
		must   bool
		given  = make(map[string]bool)
		errorf = func(format string, args ...any) {
			diags = append(diags, diag.Errorf(d.Pos, format, args...))
		}
	)
	for opt := range strings.FieldsSeq(d.Args) {
		key, value, hasValue := strings.Cut(opt, "=")
		if given[key] {
			errorf("%s is given twice", key)
			continue
		}
		given[key] = true

		switch key {
		case "name":
			switch {
			case value == "":
				errorf("name= needs the constructor's name")
			case value == "_" || !token.IsIdentifier(value):
				errorf("name=%s: %s is not a Go identifier", value, value)
			default:
				c.ctor = value
			}
		case "returns":
			if value == "" {
				errorf("returns= needs the type the constructor returns")
				continue
			}
			t, err := c.resolveReturns(fset, value)
			if err != nil {
				errorf("returns=%s: %v", value, err)
				continue
			}
			c.returns = t
		case "must":
			if hasValue {
				errorf("must takes no value")
				continue
			}
			must = true
		default:
			unknown := diag.Errorf(d.Pos, "unknown option %q of //kanna:%s", opt, directiveKey)
			if s, ok := suggest.Closest(key, options); ok {
				unknown = unknown.WithHints(fmt.Sprintf("did you mean %q?", s))
			}
			diags = append(diags, unknown)
		}
	}

	if c.ctor == "" {
		c.ctor = defaultCtor(c.Name)
	}
	if must {
		c.must = mustOf(c.ctor)
	}
	if c.returns == nil {
		c.returns = types.NewPointer(c.Named)
	}
	return diags
}

// resolveReturns reads the type expr names, in the scope of the file declaring
// the container, and checks that the constructor can return the container as
// one.
func (c *container) resolveReturns(fset *token.FileSet, expr string) (types.Type, error) {
	obj := c.Named.Obj()
	tv, err := types.Eval(fset, obj.Pkg(), obj.Pos(), expr)
	if err != nil {
		// Leave out the position within expr, such as "eval:1:1".
		if te, ok := errors.AsType[types.Error](err); ok {
			return nil, errors.New(te.Msg)
		}
		if se, ok := errors.AsType[scanner.ErrorList](err); ok && len(se) > 0 {
			return nil, errors.New(se[0].Msg)
		}
		return nil, fmt.Errorf("%w", err)
	}
	if !tv.IsType() {
		return nil, fmt.Errorf("%s is not a type", expr)
	}

	ptr := types.NewPointer(c.Named)
	switch t := tv.Type; {
	case types.Identical(t, c.Named), types.Identical(t, ptr):
		return t, nil
	case types.IsInterface(t):
		iface, _ := t.Underlying().(*types.Interface)
		if m, _ := types.MissingMethod(ptr, iface, true); m != nil {
			return nil, fmt.Errorf("*%s does not implement %s (missing method %s)", c.Name, expr, m.Name())
		}
		return t, nil
	default:
		return nil, fmt.Errorf("the constructor can return %s, *%s, or an interface *%s implements, not %s",
			c.Name, c.Name, c.Name, expr)
	}
}

// fieldsOf reads the di tags of the fields of s, and says which fields cannot
// mean what their tags say.
func fieldsOf(s scan.Struct) ([]field, []diag.Diag) {
	var (
		fields []field
		diags  []diag.Diag
	)
	for _, sf := range s.Fields {
		t, err := parseTag(sf.Tag)
		if err != nil {
			diags = append(diags, fieldDiag(sf, err))
			continue
		}
		f := field{Field: sf, tag: t}

		switch {
		case f.blank() && f.source == fromType && len(promotedOf(sf.Type, s.Named.Obj().Pkg())) == 0:
			// Resolved like any field, a blank struct serves the container
			// through its fields; anything else would be resolved for nothing.
			diags = append(diags, fieldDiag(sf, errors.New(
				`a blank field must say what it declares: di:"arg" or di:"with=<provider>"`)))
			continue
		case f.blank() && f.source == fromNowhere:
			diags = append(diags, fieldDiag(sf, errors.New(`di:"-" on a blank field says nothing; remove the field`)))
			continue
		case f.source == fromArg && f.paramName() == "":
			diags = append(diags, fieldDiag(sf, fmt.Errorf(
				`%s has no name to call the parameter by; write di:"arg=<name>"`, typeString(sf.Type, s.Named.Obj().Pkg()))))
			continue
		case f.source == fromArg && f.param != "" && types.Universe.Lookup(f.param) != nil:
			diags = append(diags, fieldDiag(sf, fmt.Errorf(
				`di:"arg=%s": a parameter named %s would hide Go's own %s`, f.param, f.param, f.param)))
			continue
		case f.source == fromArg && f.param != "" && s.Named.Obj().Pkg().Scope().Lookup(f.param) != nil:
			diags = append(diags, fieldDiag(sf, fmt.Errorf(
				`di:"arg=%s": a parameter named %s would hide the package's own %s`, f.param, f.param, f.param)))
			continue
		}
		fields = append(fields, f)
	}
	return fields, append(diags, conflicts(s, fields)...)
}

// conflicts says where fields contradict one another: a type taken twice, two
// parameters of one name, a type chosen twice, or a type both taken and
// chosen. Each is reported at the later field.
func conflicts(s scan.Struct, fields []field) []diag.Diag {
	pkg := s.Named.Obj().Pkg()
	var diags []diag.Diag
	for i, f := range fields {
		if f.source != fromArg && f.source != fromRef {
			continue
		}
		for _, prev := range fields[:i] {
			var err error
			sameType := types.Identical(f.Type, prev.Type)
			switch {
			case f.source == fromArg && prev.source == fromArg && sameType:
				err = fmt.Errorf("%s is taken twice (line %d)", typeString(f.Type, pkg), prev.Pos.Line)
			case f.source == fromArg && prev.source == fromArg && f.paramName() == prev.paramName():
				err = fmt.Errorf(`two parameters are named %s (line %d); name one with di:"arg=<name>"`,
					f.paramName(), prev.Pos.Line)
			case f.source == fromRef && prev.source == fromRef && sameType:
				err = fmt.Errorf("%s is chosen twice (line %d)", typeString(f.Type, pkg), prev.Pos.Line)
			case f.source != prev.source && (prev.source == fromArg || prev.source == fromRef) && sameType:
				err = fmt.Errorf("%s is both taken as an argument and chosen with with= (line %d)",
					typeString(f.Type, pkg), prev.Pos.Line)
			}
			if err != nil {
				diags = append(diags, fieldDiag(f.Field, err))
				break
			}
		}
	}
	return diags
}

// clash adds to diags a constructor name another one of its package takes, or
// one the package already declares, and leaves out the containers it applies
// to. What an earlier run generated, in the files of output, does not count,
// since it is about to be replaced. Positions in hints are written relative to
// dir, as diag.Write writes those of the diagnostics.
func clash(
	fset *token.FileSet, cs []container, diags []diag.Diag, dir string, output map[string]bool,
) ([]container, []diag.Diag) {
	type key struct{ pkg, name string }
	taken := make(map[key]container)
	var kept []container
	for _, c := range cs {
		ok := true
		for _, name := range []string{c.ctor, c.must} {
			if name == "" {
				continue
			}
			k := key{c.PkgPath, name}
			if other, dup := taken[k]; dup {
				diags = append(diags, diag.Errorf(c.Pos, "%s would be the constructor of both %s and %s (line %d)",
					name, c.Name, other.Name, other.Pos.Line))
				ok = false
				continue
			}
			taken[k] = c

			obj := c.Named.Obj().Pkg().Scope().Lookup(name)
			if obj == nil {
				continue
			}
			if pos := fset.Position(obj.Pos()); !output[pos.Filename] {
				pos.Filename = diag.ShortPath(dir, pos.Filename)
				diags = append(diags, diag.Errorf(c.Pos, "%s, the constructor of %s, is already declared", name, c.Name).
					WithHints(fmt.Sprintf("at %s; rename one, or give the constructor a name with name=", pos)))
				ok = false
			}
		}
		if ok {
			kept = append(kept, c)
		}
	}
	return kept, diags
}

// defaultCtor names the constructor of the struct called name: New and the
// name, or new and the name for an unexported struct, so that the constructor
// is exported exactly when the struct is.
func defaultCtor(name string) string {
	if token.IsExported(name) {
		return "New" + name
	}
	return "new" + upperFirst(name)
}

// mustOf names the must variant of the constructor ctor: MustNewServer, or
// mustNewServer for newServer.
func mustOf(ctor string) string {
	if token.IsExported(ctor) {
		return "Must" + ctor
	}
	return "must" + upperFirst(ctor)
}

// nameOf makes a name for a value of type t from the name of its type, as a
// person would: server for *Server, db for *sql.DB, httpClient for
// *HTTPClient. A type without a name, such as []string, gives "".
func nameOf(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	switch t := t.(type) {
	case *types.Alias:
		return lowerFirst(t.Obj().Name())
	case *types.Named:
		return lowerFirst(t.Obj().Name())
	default:
		return ""
	}
}

func upperFirst(s string) string {
	r := []rune(s)
	if len(r) > 0 {
		r[0] = unicode.ToUpper(r[0])
	}
	return string(r)
}

// lowerFirst lowers the leading run of capitals of s, keeping the last one
// when a lower-case letter follows it, as Go names do: URL becomes url and
// URLPath urlPath.
func lowerFirst(s string) string {
	r := []rune(s)
	n := 0
	for n < len(r) && unicode.IsUpper(r[n]) {
		n++
	}
	if n > 1 && n < len(r) && unicode.IsLower(r[n]) {
		n--
	}
	for i := range n {
		r[i] = unicode.ToLower(r[i])
	}
	return string(r)
}

// hasTag reports whether a field of s carries a di tag, readable or not.
func hasTag(s scan.Struct) bool {
	for _, f := range s.Fields {
		if _, ok := f.Tag.Lookup(tagKey); ok {
			return true
		}
		if _, err := lookup(f.Tag); err != nil {
			return true
		}
	}
	return false
}

// fieldDiag reports err at f, naming the field as in
// `di:"arg=1st": 1st is not a Go identifier (field env)`.
func fieldDiag(f scan.Field, err error) diag.Diag {
	return diag.Errorf(f.Pos, "%v (field %s)", err, f.Name)
}

// typeString renders t for a message, qualified by package names as source
// code is, the container's own package pkg aside.
func typeString(t types.Type, pkg *types.Package) string {
	return types.TypeString(t, func(p *types.Package) string {
		if p == pkg {
			return ""
		}
		return p.Name()
	})
}
