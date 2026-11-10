package gen_test

import (
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/go-kanna/internal/packages"
	"github.com/go-kanna/internal/pkgtest"

	"github.com/go-kanna/di/internal/gen"
)

// module is the import path of this module, of which testdata is part.
const module = "github.com/go-kanna/di"

// plans loads files as one package and plans its containers, as gen.Plans
// spells them, with the package alone as the user's module.
func plans(t *testing.T, files map[string]string) (map[string][]string, []string) {
	t.Helper()

	pkg := pkgtest.Load(t, files)
	return gen.Plans(pkg.Fset, []*packages.Package{pkg}, pkg.PkgPath)
}

func TestPlans_Shop(t *testing.T) {
	t.Parallel()

	set, err := packages.Load([]string{"./testdata/shop/config", "./testdata/shop/user", "./testdata/shop/usecase",
		"./testdata/shop/postgres", "./testdata/shop/handler", "./testdata/shop/app"}, packages.Config{})
	if err != nil || len(set.Diags) > 0 {
		t.Fatalf("load: %v %v", err, set.Diags)
	}

	got, diags := gen.Plans(set.Fset, set.Packages, module)
	want := map[string][]string{
		// The domain's interface takes the implementation the container
		// chooses, which both use cases share.
		"server": {
			"newServer() (*server, func(), error)",
			"cfg := config.Load() error",
			"db := openDB(cfg) cleanup error",
			"repo := postgres.NewUserRepository(db)",
			"log := newLogger()",
			"registerUser := usecase.NewRegisterUser(repo, log)",
			"getUser := usecase.NewGetUser(repo)",
			"users := handler.NewUser(registerUser, getUser)",
			"server{users: users}",
		},
		// Taking the repository, it needs no database, so neither a cleanup
		// nor an error.
		"testServer": {
			"newTestServer(repository user.Repository) *testServer",
			"log := newLogger()",
			"registerUser := usecase.NewRegisterUser(repository, log)",
			"getUser := usecase.NewGetUser(repository)",
			"users := handler.NewUser(registerUser, getUser)",
			"testServer{users: users}",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plans =\n%s\nwant\n%s", spellPlans(got), spellPlans(want))
	}
	if len(diags) > 0 {
		t.Errorf("diagnostics = %q, want none", diags)
	}
}

// TestPlans_FunctionsOfBothPackagesAreCountedTogether adds to the container's
// package a function returning what usecase.NewGetUser does, which would
// otherwise take its place without a word.
func TestPlans_FunctionsOfBothPackagesAreCountedTogether(t *testing.T) {
	t.Parallel()

	admin, err := filepath.Abs("testdata/shop/app/admin.go")
	if err != nil {
		t.Fatal(err)
	}
	set, err := packages.Load([]string{"./testdata/shop/config", "./testdata/shop/user", "./testdata/shop/usecase",
		"./testdata/shop/postgres", "./testdata/shop/handler", "./testdata/shop/app"}, packages.Config{
		Overlay: map[string][]byte{admin: []byte("package app\n\n" +
			"import \"github.com/go-kanna/di/internal/gen/testdata/shop/usecase\"\n\n" +
			"func adminGetUser() *usecase.GetUser { return nil }\n")},
	})
	if err != nil || len(set.Diags) > 0 {
		t.Fatalf("load: %v %v", err, set.Diags)
	}

	_, diags := gen.Plans(set.Fset, set.Packages, module)
	for _, want := range []string{
		"error: 2 functions in packages app and usecase return *usecase.GetUser",
		"  adminGetUser (",
		"  usecase.NewGetUser (",
		"  choose one with _ *usecase.GetUser `di:\"with=adminGetUser\"`",
	} {
		if !strings.Contains(strings.Join(diags, "\n"), want) {
			t.Errorf("diagnostics =\n%s\nwant one to say\n%s", strings.Join(diags, "\n"), want)
		}
	}
}

func TestPlans_NotExported(t *testing.T) {
	t.Parallel()

	// usecase is loaded from source, as export data leaves its unexported
	// functions out.
	set, err := packages.Load([]string{"./testdata/shop/broken", "./testdata/shop/usecase"}, packages.Config{})
	if err != nil || len(set.Diags) > 0 {
		t.Fatalf("load: %v %v", err, set.Diags)
	}

	_, diags := gen.Plans(set.Fset, set.Packages, module)
	want := "error: nothing provides *usecase.Audit\n" +
		"  usecase.newAudit returns *usecase.Audit but is not exported\n" +
		`  return one from a function in package broken, take it with di:"arg", or choose a provider with di:"with=..."` +
		"\n" + `  or leave auditing.audit at its zero value with di:"-"`
	if len(diags) != 1 || !strings.HasSuffix(diags[0], want) {
		t.Errorf("diagnostics = %q, want one ending in %q", diags, want)
	}
}

func TestPlans_Names(t *testing.T) {
	t.Parallel()

	got, diags := plans(t, map[string]string{"x.go": `package test

type (
	DB    struct{}
	Users struct{}
	Cache struct{}
	Env   string
)

func newDB() *DB                              { return nil }
func newCache(_ *DB) *Cache                   { return nil }
func newUsers(db *DB, _ *Cache, e Env) *Users { return nil }

//kanna:container
type c struct {
	_ Env ` + "`di:\"arg=environment\"`" + `

	users *Users
	store *DB
}
`})

	// The field a value goes into names it before the parameter of the first
	// call taking it, and the type names one that neither does.
	want := []string{
		"newC(environment Env) *c",
		"store := newDB()",
		"cache := newCache(store)",
		"users := newUsers(store, cache, environment)",
		"c{users: users, store: store}",
	}
	if !slices.Equal(got["c"], want) || len(diags) > 0 {
		t.Errorf("plan =\n%s\nwant\n%s\ndiagnostics %q", strings.Join(got["c"], "\n"), strings.Join(want, "\n"), diags)
	}
}

func TestPlans_PassTheZeroOfAStructWithoutFields(t *testing.T) {
	t.Parallel()

	got, diags := plans(t, map[string]string{"x.go": `package test

type (
	Clock struct{}
	Users struct{}
)

func newClock() Clock { return Clock{} }

func newUsers(Clock) *Users { return nil }

//kanna:container
type c struct {
	clock Clock
	users *Users
}
`})

	// Clock has one value, which the field and newUsers both get without
	// newClock.
	want := []string{"newC() *c", "users := newUsers(Clock{})", "c{users: users}"}
	if !slices.Equal(got["c"], want) || len(diags) > 0 {
		t.Errorf("plan =\n%s\nwant\n%s\ndiagnostics %q", strings.Join(got["c"], "\n"), strings.Join(want, "\n"), diags)
	}
}

func TestPlans_Diagnostics(t *testing.T) {
	t.Parallel()

	const hint = `  return one from a function in package test, take it with di:"arg", ` +
		`or choose a provider with di:"with=..."`

	// The container's first field is on line 13, and the declarations follow
	// the container, from line 16 for a body of one line.
	tests := []struct {
		name  string
		decls string
		body  string
		test  string
		want  string
	}{
		{
			name: "nothing provides a field",
			body: "users *Users",
			want: "x.go:13:2: error: nothing provides *Users\n" + hint,
		},
		{
			name:  "nothing provides a dependency",
			decls: "func newUsers(db *DB) *Users { return nil }",
			body:  "users *Users",
			want:  "x.go:13:2: error: nothing provides *DB, which newUsers needs\n  via c.users → newUsers\n" + hint,
		},
		{
			name: "a function that is not quite a provider",
			decls: "func newUsers(db *DB) *Users { return nil }\n" +
				"func openDB() (*DB, func() error, error) { return nil, nil, nil }",
			body: "users *Users",
			want: "  openDB returns *DB but cannot be a provider: " +
				"a cleanup must be a func(), with neither parameters nor results\n",
		},
		{
			name:  "a provider of the pointer",
			decls: "func newDB() *DB { return nil }",
			body:  "db DB",
			want:  "x.go:13:2: error: nothing provides DB\n  newDB returns *DB, not DB\n" + hint,
		},
		{
			name:  "a provider of the value",
			decls: "func newDB() DB { return DB{} }",
			body:  "db *DB",
			want:  "x.go:13:2: error: nothing provides *DB\n  newDB returns DB, not *DB\n" + hint,
		},
		{
			name: "a provider only tests compile",
			body: "users *Users",
			test: "func fakeUsers() *Users { return nil }",
			want: "  fakeUsers returns *Users but is declared in a _test.go file, which only tests compile\n",
		},
		{
			name: "a with= only tests compile",
			body: "_ *Users `di:\"with=fakeUsers\"`\nusers *Users",
			test: "func fakeUsers() *Users { return nil }",
			want: "x.go:13:2: error: fakeUsers is declared in a _test.go file, which only tests compile (field _)",
		},
		{
			name:  "a parameter of a type without a name",
			decls: "func newUsers(dsn string) *Users { return nil }",
			body:  "users *Users",
			want: "x.go:13:2: error: nothing provides string, which newUsers needs\n  via c.users → newUsers\n" +
				"  string has no name of its own, so kanna-di does not hand it out by type\n" +
				"  call newUsers from a function in package test that passes it, " +
				"or have it take a type of its own, such as type DSN string",
		},
		{
			name: "a type of another module",
			body: "log *slog.Logger",
			want: "x.go:13:2: error: nothing provides *slog.Logger\n" +
				"  log/slog is not in your module, so kanna-di does not look there\n" + hint,
		},
		{
			name:  "two providers",
			decls: "func newUsers() *Users { return nil }\nfunc otherUsers() *Users { return nil }",
			body:  "users *Users",
			want: "x.go:13:2: error: 2 functions in package test return *Users\n" +
				"  newUsers (x.go:16:6)\n  otherUsers (x.go:17:6)\n" +
				"  choose one with _ *Users `di:\"with=newUsers\"`",
		},
		{
			name:  "a cycle",
			decls: "func newUsers(db *DB) *Users { return nil }\nfunc newDB(u *Users) *DB { return nil }",
			body:  "users *Users",
			want: "x.go:13:2: error: dependency cycle: newUsers → newDB → newUsers\n" +
				"  newUsers (x.go:16:6)\n  newDB (x.go:17:6)",
		},
		{
			name: "a cycle further down",
			decls: "func newApp(u *Users) *App { return nil }\nfunc newUsers(db *DB) *Users { return nil }\n" +
				"func newDB(u *Users) *DB { return nil }",
			body: "app *App",
			want: "x.go:13:2: error: dependency cycle: newUsers → newDB → newUsers\n" +
				"  newUsers (x.go:17:6)\n  newDB (x.go:18:6)\n  via c.app → newApp",
		},
		{
			name:  "an argument nothing needs",
			decls: "func newUsers() *Users { return nil }",
			body:  "_ Env `di:\"arg\"`\nusers *Users",
			want:  "x.go:13:2: warning: nothing in c needs the Env it takes with di:\"arg\"",
		},
		{
			name:  "a struct taken that nothing draws on",
			decls: "type Deps struct{ DB *DB }",
			body:  "_ Deps `di:\"arg\"`",
			want:  "x.go:13:2: warning: nothing in c needs the Deps it takes with di:\"arg\", nor any of its fields",
		},
		{
			name:  "a struct chosen that nothing draws on",
			decls: "type Deps struct{ DB *DB }\nfunc newDeps() *Deps { return nil }",
			body:  "_ *Deps `di:\"with=newDeps\"`",
			want:  "x.go:13:2: warning: nothing in c needs *Deps or any of its fields, so its with= chooses nothing",
		},
		{
			name:  "a struct found that nothing draws on",
			decls: "type Deps struct{ DB *DB }\nfunc newDeps() Deps { return Deps{} }",
			body:  "_ Deps",
			want:  "x.go:13:2: warning: nothing in c needs Deps or any of its fields\n",
		},
		{
			name:  "a with= nothing needs",
			decls: "func newRepo() impl { return impl{} }",
			body:  "_ Repo `di:\"with=newRepo\"`",
			want:  "x.go:13:2: warning: nothing in c needs Repo, so its with= chooses nothing",
		},
		{
			name: "two providers further down",
			decls: "func newUsers(db *DB) *Users { return nil }\n" +
				"func newDB() *DB { return nil }\nfunc openDB() *DB { return nil }",
			body: "users *Users",
			want: "x.go:13:2: error: 2 functions in package test return *DB\n" +
				"  newDB (x.go:17:6)\n  openDB (x.go:18:6)\n" +
				"  via c.users → newUsers\n" +
				"  choose one with _ *DB `di:\"with=newDB\"`",
		},
		{
			name: "a type without a name",
			body: "names []string",
			want: "x.go:13:2: error: nothing provides []string\n" +
				"  []string has no name of its own, so kanna-di does not hand it out by type\n" +
				"  declare a type of its own for it, such as type DSN string\n" +
				"  or leave c.names at its zero value with di:\"-\"",
		},
		{
			name:  "a with= misspelled",
			decls: "func newRepo() impl { return impl{} }",
			body:  "_ Repo `di:\"with=newRep\"`\nrepo Repo",
			want:  "x.go:13:2: error: package test has no function newRep (field _)\n  did you mean newRepo?",
		},
		{
			name: "a with= that names nothing",
			body: "_ Repo `di:\"with=newRepo\"`\nrepo Repo",
			want: "x.go:13:2: error: package test has no function newRepo (field _)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			files := map[string]string{"x.go": `package test

import "log/slog"

type (DB struct{ dsn string }; Users struct{}; App struct{}; Env string; Repo interface{ Find() }; impl struct{})

func (impl) Find() {}

var _ *slog.Logger

//kanna:container
type c struct {
	` + strings.ReplaceAll(tt.body, "\n", "\n\t") + `
}

` + tt.decls + "\n"}
			if tt.test != "" {
				files["x_test.go"] = "package test\n\n" + tt.test + "\n"
			}
			_, diags := plans(t, files)
			if !strings.Contains(strings.Join(diags, "\n")+"\n", tt.want) {
				t.Errorf("diagnostics =\n%s\nwant one to say\n%s", strings.Join(diags, "\n"), tt.want)
			}
		})
	}
}

func TestPlans_HintToLeaveAFieldAtItsZeroValue(t *testing.T) {
	t.Parallel()

	_, diags := plans(t, map[string]string{"x.go": `package test

type (
	Mutex struct{ state int }
	Clock struct{ now int }
	DB    struct{ dsn string }
	Deps  struct{ DB *DB }
	Users struct{}
)

func newUsers(clock Clock) *Users { return nil }

//kanna:container
type base struct {
	mu    Mutex
	users *Users
}

//kanna:container
type c struct {
	*base

	_  Deps
	db *DB
}
`})

	// The hint is for a field needing the type itself, named as it may be a
	// field of a container built in place; neither a dependency of a field
	// nor a blank field is left at a zero value.
	const hint = `  return one from a function in package test, take it with di:"arg", ` +
		`or choose a provider with di:"with=..."`
	want := []string{
		"x.go:15:2: error: nothing provides Mutex\n" + hint + "\n" +
			`  or leave base.mu at its zero value with di:"-"`,
		"x.go:16:2: error: nothing provides Clock, which newUsers needs\n  via base.users → newUsers\n" + hint,
		"x.go:21:3: error: nothing provides Mutex\n" + hint + "\n" +
			`  or leave c.base.mu at its zero value with di:"-"`,
		"x.go:21:3: error: nothing provides Clock, which newUsers needs\n  via c.base.users → newUsers\n" + hint,
		"x.go:23:2: error: nothing provides Deps\n" + hint,
	}
	if !slices.Equal(diags, want) {
		t.Errorf("diagnostics =\n%s\nwant\n%s", strings.Join(diags, "\n"), strings.Join(want, "\n"))
	}
}

func TestPlans_ShareACall(t *testing.T) {
	t.Parallel()

	got, diags := plans(t, map[string]string{"x.go": `package test

type (
	Repo interface{ Find() }
	impl struct{}
)

func (*impl) Find() {}

func newRepo() *impl { return nil }

//kanna:container
type c struct {
	_ Repo ` + "`di:\"with=newRepo\"`" + `

	repo Repo
	impl *impl
}
`})

	// The Repo chosen and the *impl asked for come from one call.
	want := []string{"newC() *c", "repo := newRepo()", "c{repo: repo, impl: repo}"}
	if !slices.Equal(got["c"], want) || len(diags) > 0 {
		t.Errorf("plan =\n%s\nwant\n%s\ndiagnostics %q", strings.Join(got["c"], "\n"), strings.Join(want, "\n"), diags)
	}
}

func TestPlans_LeaveAStructWithoutFieldsAtItsZeroValue(t *testing.T) {
	t.Parallel()

	got, diags := plans(t, map[string]string{"x.go": `package test

type (
	UnimplementedServer struct{}
	Marker              struct{}
	Token               struct{}
	Users               struct{}
)

func newMarker() Marker { return Marker{} }
func newUsers() *Users  { return nil }

//kanna:container
type c struct {
	UnimplementedServer

	none   struct{}
	marker Marker ` + "`di:\"with=newMarker\"`" + `
	token  Token  ` + "`di:\"arg\"`" + `
	users  *Users
}
`})

	// Nothing provides UnimplementedServer or struct{}, and nothing has to:
	// their zero value is the only one they have. A tag still says where the
	// value comes from, and a pointer to such a struct may be nil.
	want := []string{
		"newC(token Token) *c",
		"marker := newMarker()",
		"users := newUsers()",
		"c{marker: marker, token: token, users: users}",
	}
	if !slices.Equal(got["c"], want) || len(diags) > 0 {
		t.Errorf("plan =\n%s\nwant\n%s\ndiagnostics %q", strings.Join(got["c"], "\n"), strings.Join(want, "\n"), diags)
	}
}

func TestPlans_ReportAWithOnce(t *testing.T) {
	t.Parallel()

	_, diags := plans(t, map[string]string{"x.go": `package test

type Repo interface{ Find() }

//kanna:container
type c struct {
	_    Repo ` + "`di:\"with=newRepo\"`" + `
	repo Repo
}
`})
	if len(diags) != 1 {
		t.Errorf("diagnostics = %q, want the with= alone", diags)
	}
}

func TestPlans_LeaveOutTheirOwnConstructor(t *testing.T) {
	t.Parallel()

	// newC, which an earlier run generated, returns the Greeter c holds, but
	// calling it would build c to build c.
	_, diags := plans(t, map[string]string{
		"x.go": `package test

type Greeter interface{ Greet() }

//kanna:container returns=Greeter
type c struct {
	g Greeter
}

func (*c) Greet() {}
`,
		"di_gen.go": `// Code generated by kanna-di. DO NOT EDIT.

package test

func newC() Greeter { return &c{} }
`,
	})
	if want := "error: nothing provides Greeter\n"; len(diags) != 1 || !strings.Contains(diags[0]+"\n", want) {
		t.Errorf("diagnostics = %q, want %q", diags, want)
	}
}

// spellPlans writes plans one line each, to show a difference.
func spellPlans(plans map[string][]string) string {
	var b strings.Builder
	for _, name := range slices.Sorted(maps.Keys(plans)) {
		b.WriteString(name + ":\n\t" + strings.Join(plans[name], "\n\t") + "\n")
	}
	return b.String()
}

func TestPlans_Variadic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		decls string
		body  string
		want  []string
		diag  string
	}{
		{
			name: "nothing provides the element, so the parameter is left empty",
			want: []string{"newC() *c", "repo := newRepo()", "c{repo: repo}"},
		},
		{
			name:  "with= chooses the element, and every call gets it",
			decls: "func clockOption() Option { return nil }\nfunc newStore(opts ...Option) *Store { return nil }",
			body:  "_ Option `di:\"with=clockOption\"`\nstore *Store",
			want: []string{
				"newC() *c", "opts := clockOption()", "repo := newRepo(opts)", "store := newStore(opts)",
				"c{repo: repo, store: store}",
			},
		},
		{
			name: "an argument",
			body: "_ Option `di:\"arg\"`",
			want: []string{"newC(option Option) *c", "repo := newRepo(option)", "c{repo: repo}"},
		},
		{
			name: "a field of an embedded struct",
			body: "Deps `di:\"arg\"`",
			want: []string{"newC(deps Deps) *c", "clock := deps.Clock", "repo := newRepo(clock)", "c{repo: repo, Deps: deps}"},
		},
		{
			name:  "a function found",
			decls: "func clockOption() Option { return nil }",
			want:  []string{"newC() *c", "opts := clockOption()", "repo := newRepo(opts)", "c{repo: repo}"},
		},
		{
			name:  "a type without a name of its own is not handed out",
			decls: "func newNames(names ...string) *Names { return nil }\nfunc name() string { return \"\" }",
			body:  "names *Names",
			want:  []string{"newC() *c", "repo := newRepo()", "names := newNames()", "c{repo: repo, names: names}"},
		},
		{
			name:  "a function found that cannot be called",
			decls: "func clockOption(c *Clock) Option { return nil }",
			diag:  "x.go:14:2: error: nothing provides *Clock, which clockOption needs\n  via c.repo → newRepo → clockOption",
		},
		{
			name:  "a function found that takes what it returns",
			decls: "func chain(opts ...Option) Option { return nil }",
			diag:  "x.go:14:2: error: dependency cycle: chain → chain",
		},
		{
			name:  "two functions found",
			decls: "func clockOption() Option { return nil }\nfunc hookOption() Option { return nil }",
			diag:  "x.go:14:2: error: 2 functions in package test return Option",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, diags := plans(t, map[string]string{"x.go": `package test

type (
	Option func(*Repo)
	Repo   struct{ _ int }
	Store  struct{ _ int }
	Names  struct{ _ int }
	Clock  struct{ _ int }
	Deps   struct{ Clock Option }
)

//kanna:container
type c struct {
	repo *Repo
	` + strings.ReplaceAll(tt.body, "\n", "\n\t") + `
}

func newRepo(opts ...Option) *Repo { return nil }

` + tt.decls + "\n"})
			if tt.diag != "" {
				if !strings.Contains(strings.Join(diags, "\n"), tt.diag) {
					t.Errorf("diagnostics =\n%s\nwant one to say\n%s", strings.Join(diags, "\n"), tt.diag)
				}
				return
			}
			if !slices.Equal(got["c"], tt.want) || len(diags) > 0 {
				t.Errorf("plan =\n%s\nwant\n%s\ndiagnostics %q", strings.Join(got["c"], "\n"), strings.Join(tt.want, "\n"), diags)
			}
		})
	}
}
