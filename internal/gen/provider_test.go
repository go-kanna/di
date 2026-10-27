package gen_test

import (
	"slices"
	"testing"

	"github.com/go-kanna/internal/pkgtest"

	"github.com/go-kanna/di/internal/gen"
)

func TestProviders(t *testing.T) {
	t.Parallel()

	pkg := pkgtest.Load(t, map[string]string{
		"infra.go": `package test

import (
	"errors"
	"io"
)

type (
	Config  struct{ DSN string }
	DB      struct{}
	Cache   struct{}
	Repo    interface{ Find() }
	Cleanup func()
	Alias   = DB
	Box[T any] struct{ v T }
	Name   string
)

func NewConfig() Config                        { return Config{} }
func openDB(c Config) (*DB, error)             { return nil, errors.New("closed") }
func OpenCache(c Config) (*Cache, func())      { return nil, func() {} }
func Dial(c Config) (*Cache, func(), error)    { return nil, nil, nil }
func NewRepo(db *DB, opts ...string) Repo      { return nil }
func Named(c Config) (*DB, Cleanup)            { return nil, nil }
func FromAlias() Alias                         { return DB{} }
func IntBox() *Box[int]                        { return nil }
func Reader() io.Reader                        { return nil }
func Generic[T any]() *Box[T]                  { return nil }
func Version() string                          { return "" }
func Bytes() []byte                            { return nil }
func Any() any                                 { return nil }
func Fail() error                              { return nil }
func Nothing()                                 {}
func Closer() (*DB, func() error, error)       { return nil, nil, nil }
func Twice() (*DB, error, func())              { return nil, nil, nil }
func Pair() (*DB, *Cache)                      { return nil, nil }
func Many() (*DB, func(), error, int)          { return nil, nil, nil, 0 }
func NameOf() Name                             { return "" }
`,
		"infra_test.go": `package test

func fakeRepo() Repo { return nil }
`,
	})

	want := []string{
		"NewConfig() Config",
		"openDB(Config) *DB, error",
		"OpenCache(Config) *Cache, func()",
		"Dial(Config) *Cache, func(), error",
		"NewRepo(*DB) Repo",
		"Named(Config) *DB, func()", // a cleanup of a type declared as func()
		"FromAlias() Alias",
		"IntBox() *Box[int]",
		"Reader() io.Reader",
		"Closer: a cleanup must be a func(), with neither parameters nor results",
		"Twice: what follows its first result must be a func() cleanup, an error, or both in that order",
		"Pair: what follows its first result must be a func() cleanup, an error, or both in that order",
		"Many: what follows its first result must be a func() cleanup, an error, or both in that order",
		"NameOf() Name",
		"fakeRepo() Repo (test)",
	}
	if got := gen.Providers(pkg); !slices.Equal(got, want) {
		t.Errorf("providers =\n%q\nwant\n%q", got, want)
	}
}

func TestProviders_LeaveOutMustVariants(t *testing.T) {
	t.Parallel()

	pkg := pkgtest.Load(t, map[string]string{
		"x.go": `package test

type (
	Deps   struct{}
	server struct{}
	Other  struct{}
)

func NewDeps() (Deps, error)          { return Deps{}, nil }
func MustNewDeps() Deps               { return Deps{} }
func newServer() (*server, error)     { return nil, nil }
func mustNewServer() *server          { return nil }
func MustLoad() Other                 { return Other{} }
func NewOther() Other                 { return Other{} }
func MustNewOther() *Other            { return nil }
`,
	})

	// MustLoad has no Load to stand for, and MustNewOther returns another type
	// than NewOther.
	want := []string{
		"NewDeps() Deps, error",
		"newServer() *server, error",
		"MustLoad() Other",
		"NewOther() Other",
		"MustNewOther() *Other",
	}
	if got := gen.Providers(pkg); !slices.Equal(got, want) {
		t.Errorf("providers =\n%q\nwant\n%q", got, want)
	}
}

func TestLookup(t *testing.T) {
	t.Parallel()

	pkg := pkgtest.Load(t, map[string]string{
		"x.go": `package test

type (
	DB    struct{}
	Alias = DB
)

func Open() *DB          { return nil }
func OpenAlias() *Alias  { return nil }
func Value() DB          { return DB{} }
`,
	})

	tests := []struct {
		expr string
		want []string
	}{
		// An alias names the same type, so both spellings find both.
		{expr: "*DB", want: []string{"Open", "OpenAlias"}},
		{expr: "*Alias", want: []string{"Open", "OpenAlias"}},
		{expr: "DB", want: []string{"Value"}},
		{expr: "int", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			t.Parallel()

			got, err := gen.Lookup(pkg, tt.expr)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("lookup(%s) = %q, want %q", tt.expr, got, tt.want)
			}
		})
	}
}
