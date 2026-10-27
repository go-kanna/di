package gen_test

import (
	"strings"
	"testing"

	"github.com/go-kanna/internal/pkgtest"

	"github.com/go-kanna/di/internal/gen"
)

// The container's package, importing what its with= refs name. A package
// imported only for its name is imported blank.
const refSource = `package test

import (
	"bytes"
	"io"
	"log/slog"
	"math/big"
	"math/rand"

	_ "crypto/rand"
	_ "math/rand/v2"
	_ "html/template"
	_ "text/template"
)

type (
	DB  struct{}
	Env string
)

var (
	_ io.Writer
	_ *slog.Logger
	_ *bytes.Buffer
	_ *big.Int
	_ = rand.Int
)

var Open = openDB

func openDB() *DB         { return nil }
func newDB() (*DB, error) { return nil, nil }
`

// postgres stands for a package of the user's module.
const postgresSource = `package postgres

type Repo struct{}

func NewRepo() *Repo                           { return nil }
func MustNewRepo() *Repo                       { return nil }
func newRepo() *Repo                           { return nil }
func Closer() (*Repo, func() error, error)     { return nil, nil, nil }
func Version() string                          { return "" }
func Make[T any]() *Repo                       { return nil }
`

func TestResolve(t *testing.T) {
	t.Parallel()

	pkg := pkgtest.Load(t, map[string]string{"x.go": refSource})
	tests := []struct {
		name, want, ref, provider string
	}{
		{name: "a function of the container's package", want: "*DB", ref: "openDB", provider: "test.openDB"},
		{name: "the container's package by name", want: "*DB", ref: "test.openDB", provider: "test.openDB"},
		{name: "one returning an error too", want: "*DB", ref: "newDB", provider: "test.newDB"},
		{name: "by package name", want: "*slog.Logger", ref: "slog.New", provider: "log/slog.New"},
		{name: "by full path", want: "*slog.Logger", ref: "log/slog.Default", provider: "log/slog.Default"},
		{name: "an implementation of an interface", want: "io.Writer", ref: "bytes.NewBuffer", provider: "bytes.NewBuffer"},
		{name: "by the end of the path", want: "*big.Int", ref: "crypto/rand.Int", provider: "crypto/rand.Int"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := gen.Resolve(pkg, tt.want, tt.ref)
			if err != nil || got != tt.provider {
				t.Errorf("resolve(%s for %s) = %q, %v; want %q", tt.ref, tt.want, got, err, tt.provider)
			}
		})
	}
}

func TestResolve_AnotherPackage(t *testing.T) {
	t.Parallel()

	pkg := pkgtest.Load(t, map[string]string{"x.go": refSource})
	postgres := pkgtest.Load(t, map[string]string{"postgres.go": postgresSource})

	// A must variant named on purpose is what the container asked for.
	for _, ref := range []string{"postgres.NewRepo", "postgres.MustNewRepo"} {
		got, err := gen.Resolve(pkg, "any", ref, postgres)
		if want := ref; err != nil || got != want {
			t.Errorf("resolve(%s) = %q, %v; want %q", ref, got, err, want)
		}
	}

	// A package of the module may also be the one declaring the type.
	got, err := gen.Resolve(pkg, "any", "postgres.NewRepo", postgres, postgres)
	if err != nil || got != "postgres.NewRepo" {
		t.Errorf("resolve with the package twice = %q, %v; want postgres.NewRepo", got, err)
	}
}

func TestResolve_Errors(t *testing.T) {
	t.Parallel()

	pkg := pkgtest.Load(t, map[string]string{"x.go": refSource})
	postgres := pkgtest.Load(t, map[string]string{"postgres.go": postgresSource})

	tests := []struct {
		name, want, ref, msg string
	}{
		{
			name: "no function of the container's package",
			want: "*DB", ref: "openDb",
			msg: "package test has no function openDb; did you mean openDB?",
		},
		{name: "not a function", want: "*DB", ref: "Open", msg: "Open is not a function"},
		{
			name: "no package",
			want: "*DB", ref: "postgre.NewRepo",
			msg: "no package postgre; with= looks in your module and in the package declaring *test.DB; did you mean postgres?",
		},
		{
			name: "two packages of one name",
			want: "any", ref: "template.New",
			msg: "template could be any of html/template, text/template; " +
				"write as much of the import path as it takes to tell them apart",
		},
		{
			// math/rand/v2 is named rand, although its path ends in v2.
			name: "packages by their name",
			want: "*big.Int", ref: "rand.Int",
			msg: "rand could be any of crypto/rand, math/rand, math/rand/v2",
		},
		{
			name: "no function of another package",
			want: "any", ref: "postgres.NewRepos",
			msg: "package postgres has no function NewRepos; did you mean NewRepo?",
		},
		{name: "unexported", want: "any", ref: "postgres.newRepo", msg: "postgres.newRepo is not exported"},
		{name: "a type", want: "any", ref: "postgres.Repo", msg: "postgres.Repo is not a function"},
		{
			name: "generic",
			want: "any", ref: "postgres.Make",
			msg: "postgres.Make cannot be a provider: it is generic, and nothing would choose its type arguments",
		},
		{
			name: "a bad cleanup",
			want: "any", ref: "postgres.Closer",
			msg: "postgres.Closer cannot be a provider: a cleanup must be a func(), with neither parameters nor results",
		},
		{
			name: "a result nothing is looked up by",
			want: "any", ref: "postgres.Version",
			msg: "postgres.Version cannot be a provider: its first result must be a type declared in a package",
		},
		{
			name: "an interface not implemented",
			want: "io.Closer", ref: "bytes.NewBuffer",
			msg: "bytes.NewBuffer returns *bytes.Buffer, which does not implement io.Closer (missing method Close)",
		},
		{
			name: "another type",
			want: "*slog.Logger", ref: "bytes.NewBuffer",
			msg: "bytes.NewBuffer returns *bytes.Buffer, not *slog.Logger",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := gen.Resolve(pkg, tt.want, tt.ref, postgres)
			if err == nil || !strings.HasPrefix(err.Error(), tt.msg) {
				t.Errorf("resolve(%s for %s) error = %v, want %q", tt.ref, tt.want, err, tt.msg)
			}
		})
	}
}
