package gen

import (
	"errors"
	"fmt"
	"go/token"
	"reflect"
	"slices"
	"strings"
)

// tagKey is the struct tag that says where a field of a container gets its
// value.
const tagKey = "di"

// forms lists what a di tag can say, for the errors that reject the rest.
const forms = `di:"-", di:"arg", di:"arg=<name>", or di:"with=<provider>"`

// source says where a field of a container gets its value.
type source int

const (
	// fromType finds a provider of the field's type. A field without a di
	// tag is filled this way.
	fromType source = iota

	// fromNowhere leaves the field alone: di:"-".
	fromNowhere

	// fromArg takes the value as a parameter of the constructor: di:"arg",
	// or di:"arg=<name>".
	fromArg

	// fromRef calls the provider the tag names: di:"with=<ref>".
	fromRef
)

// tag is a di tag, read for its syntax alone. What it means for the field, a
// blank one say, is for the container to decide.
type tag struct {
	source source

	// param names the constructor's parameter for fromArg, or is "" to name
	// it after the field's type.
	param string

	// ref is the provider named for fromRef.
	ref ref
}

// ref names a provider, as written after with=.
type ref struct {
	// pkg is "" for a function of the container's package. Otherwise it is
	// as much of the end of an import path as it takes to tell the package
	// apart: its name, such as "postgres", or "infra/postgres", or the full
	// path.
	pkg string

	// fn is the function's name.
	fn string
}

// String spells r as it is written after with=.
func (r ref) String() string {
	if r.pkg == "" {
		return r.fn
	}
	return r.pkg + "." + r.fn
}

// parseTag reads the di tag in st. A field without one is filled by its type,
// as is one whose tag is empty.
func parseTag(st reflect.StructTag) (tag, error) {
	s, err := lookup(st)
	if err != nil || s == "" {
		return tag{}, err
	}

	switch {
	case strings.Contains(s, ","):
		return tag{}, fmt.Errorf("di tag %q says more than one thing; write one of %s", s, forms)
	case strings.ContainsAny(s, " \t"):
		return tag{}, fmt.Errorf("di tag %q contains a space", s)
	}

	key, value, hasValue := strings.Cut(s, "=")
	switch key {
	case "-":
		if hasValue {
			return tag{}, errors.New(`di:"-" takes no value`)
		}
		return tag{source: fromNowhere}, nil

	case "arg":
		switch {
		case !hasValue:
			return tag{source: fromArg}, nil
		case value == "":
			return tag{}, errors.New(`di:"arg=" names no parameter; write di:"arg" to name it after its type`)
		case value == "_":
			return tag{}, errors.New(`di:"arg=_" names a parameter nothing can use; write di:"arg" to name it after its type`)
		case !token.IsIdentifier(value):
			return tag{}, fmt.Errorf(`di:"arg=%s": %s is not a Go identifier`, value, value)
		}
		return tag{source: fromArg, param: value}, nil

	case "with":
		if value == "" {
			return tag{}, errors.New(`di:"with" names no provider; write one such as di:"with=postgres.NewUserRepository"`)
		}
		r, err := parseRef(value)
		if err != nil {
			return tag{}, fmt.Errorf(`di:"with=%s": %w`, value, err)
		}
		return tag{source: fromRef, ref: r}, nil

	default:
		return tag{}, fmt.Errorf("unknown di tag %q; write one of %s", s, forms)
	}
}

// parseRef reads s, what follows with=. The function's name follows the last
// dot, since an import path may hold dots of its own.
func parseRef(s string) (ref, error) {
	pkg, fn := "", s
	if i := strings.LastIndex(s, "."); i >= 0 {
		pkg, fn = s[:i], s[i+1:]
		switch {
		case fn == "":
			return ref{}, errors.New("the function's name is missing")
		case pkg == "":
			return ref{}, errors.New("the package is missing")
		case slices.Contains(strings.Split(pkg, "/"), ""):
			return ref{}, fmt.Errorf("%s is not an import path", pkg)
		}
	}
	if fn == "_" || !token.IsIdentifier(fn) {
		return ref{}, fmt.Errorf("%s cannot name a function", fn)
	}
	return ref{pkg: pkg, fn: fn}, nil
}

// lookup returns the value of the di tag in st, or "" when there is none.
//
// A di tag reflect cannot read is an error. reflect treats it as absent, which
// would quietly fill the field by its type instead of taking it as an argument
// or from the provider it names.
func lookup(st reflect.StructTag) (string, error) {
	if s, ok := st.Lookup(tagKey); ok {
		return s, nil
	}
	if key := tagKey + `:"`; strings.HasPrefix(string(st), key) || strings.Contains(string(st), " "+key) {
		return "", errors.New("cannot read the di tag; check the quotes of the struct tag")
	}
	return "", nil
}
