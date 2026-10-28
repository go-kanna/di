package gen_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/go-kanna/internal/packages"

	"github.com/go-kanna/di/internal/gen"
)

// embedTypes declares what the cases below embed and need.
const embedTypes = `package test

type (
	DB     struct{}
	Cache  struct{}
	Users  struct{}
	Env    string
	Repo   interface{ Find() }
	impl   struct{}
	Common struct{ DB *DB }
)

func (*impl) Find() {}
`

func TestPlans_Embedded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		src   string
		plans map[string][]string
	}{
		{
			name: "a struct taken as an argument",
			src: `
type Deps struct {
	DB    *DB
	Cache *Cache
}

func newUsers(db *DB, c *Cache) *Users { return nil }

//kanna:container
type App struct {
	Deps ` + "`di:\"arg\"`" + `

	Users *Users
}
`,
			plans: map[string][]string{"App": {
				"NewApp(deps Deps) *App",
				"db := deps.DB",
				"cache := deps.Cache",
				"users := newUsers(db, cache)",
				"App{Deps: deps, Users: users}",
			}},
		},
		{
			// The struct is chosen, and its fields serve the container, but it
			// is not stored: App gets no field or method of Deps.
			name: "a blank struct chosen with with=",
			src: `
type Deps struct {
	DB    *DB
	Cache *Cache
}

func newDeps() (*Deps, func())         { return nil, nil }
func newUsers(db *DB, c *Cache) *Users { return nil }

//kanna:container
type App struct {
	_ *Deps ` + "`di:\"with=newDeps\"`" + `

	Users *Users
}
`,
			plans: map[string][]string{"App": {
				"NewApp() (*App, func())",
				"deps := newDeps() cleanup",
				"db := deps.DB",
				"cache := deps.Cache",
				"users := newUsers(db, cache)",
				"App{Users: users}",
			}},
		},
		{
			name: "a blank struct taken as an argument",
			src: `
type Deps struct {
	DB    *DB
	Cache *Cache
}

func newUsers(db *DB, c *Cache) *Users { return nil }

//kanna:container
type App struct {
	_ Deps ` + "`di:\"arg\"`" + `

	Users *Users
}
`,
			plans: map[string][]string{"App": {
				"NewApp(deps Deps) *App",
				"db := deps.DB",
				"cache := deps.Cache",
				"users := newUsers(db, cache)",
				"App{Users: users}",
			}},
		},
		{
			// What the container built in place draws on serves the one
			// building it, as what it embeds does.
			name: "a blank struct of a container built in place",
			src: `
type Deps struct{ DB *DB }

func newUsers(db *DB) *Users { return nil }

//kanna:container
type server struct {
	_ Deps ` + "`di:\"arg\"`" + `

	users *Users
}

//kanna:container
type wrapped struct {
	*server

	db *DB
}
`,
			plans: map[string][]string{"wrapped": {
				"newWrapped(deps Deps) *wrapped",
				"db := deps.DB",
				"users := newUsers(db)",
				"server := &server{users: users}",
				"wrapped{server: server, db: db}",
			}},
		},
		{
			// Deps is built from a *DB, which its own field cannot be.
			name: "a struct from a provider",
			src: `
type Deps struct{ DB *DB }

func newDB() *DB              { return nil }
func newDeps(db *DB) Deps     { return Deps{} }
func newUsers(db *DB) *Users  { return nil }

//kanna:container
type App struct {
	Deps
	Users *Users
}
`,
			plans: map[string][]string{"App": {
				"NewApp() *App",
				"db := newDB()",
				"deps := newDeps(db)",
				"db2 := deps.DB",
				"users := newUsers(db2)",
				"App{Deps: deps, Users: users}",
			}},
		},
		{
			name: "a field promoted from deeper",
			src: `
type Deps struct{ Common }

func newUsers(db *DB) *Users { return nil }

//kanna:container
type App struct {
	Deps ` + "`di:\"arg\"`" + `

	Users *Users
}
`,
			plans: map[string][]string{"App": {
				"NewApp(deps Deps) *App",
				"db := deps.Common.DB",
				"users := newUsers(db)",
				"App{Deps: deps, Users: users}",
			}},
		},
		{
			// A selector of DB alone would reach the string.
			name: "a shallower field of another type with the name",
			src: `
type Deps struct {
	DB string
	Common
}

func newUsers(db *DB) *Users { return nil }

//kanna:container
type App struct {
	Deps ` + "`di:\"arg\"`" + `

	Users *Users
}
`,
			plans: map[string][]string{"App": {
				"NewApp(deps Deps) *App",
				"db := deps.Common.DB",
				"users := newUsers(db)",
				"App{Deps: deps, Users: users}",
			}},
		},
		{
			name: "a shallower field hiding a deeper one",
			src: `
type Deps struct {
	Common
	DB *DB
}

func newUsers(db *DB) *Users { return nil }

//kanna:container
type App struct {
	Deps ` + "`di:\"arg\"`" + `

	Users *Users
}
`,
			plans: map[string][]string{"App": {
				"NewApp(deps Deps) *App",
				"db := deps.DB",
				"users := newUsers(db)",
				"App{Deps: deps, Users: users}",
			}},
		},
		{
			name: "an interface chosen with with=",
			src: `
func newImpl() *impl { return nil }

//kanna:container
type App struct {
	Repo ` + "`di:\"with=newImpl\"`" + `
}
`,
			plans: map[string][]string{"App": {
				"NewApp() *App",
				"repo := newImpl()",
				"App{Repo: repo}",
			}},
		},
		{
			// The test server is the server, built with the repository it
			// takes instead of the one the server chooses.
			name: "a container built in place",
			src: `
func newImpl() *impl         { return nil }
func newUsers(repo Repo) *Users { return nil }

//kanna:container
type server struct {
	_ Repo ` + "`di:\"with=newImpl\"`" + `

	users *Users
}

//kanna:container
type testServer struct {
	_ Repo ` + "`di:\"arg\"`" + `

	*server
}
`,
			plans: map[string][]string{
				"server": {
					"newServer() *server",
					"repo := newImpl()",
					"users := newUsers(repo)",
					"server{users: users}",
				},
				"testServer": {
					"newTestServer(repo Repo) *testServer",
					"users := newUsers(repo)",
					"server := &server{users: users}",
					"testServer{server: server}",
				},
			},
		},
		{
			name: "the arguments and with= of a container built in place",
			src: `
func newImpl() *impl                    { return nil }
func newUsers(repo Repo, env Env) *Users { return nil }

//kanna:container
type server struct {
	_ Repo ` + "`di:\"with=test.newImpl\"`" + `
	_ Env  ` + "`di:\"arg\"`" + `

	users *Users
}

//kanna:container
type wrapped struct {
	server
}
`,
			plans: map[string][]string{
				"server": {
					"newServer(env Env) *server",
					"repo := newImpl()",
					"users := newUsers(repo, env)",
					"server{users: users}",
				},
				"wrapped": {
					"newWrapped(env Env) *wrapped",
					"repo := newImpl()",
					"users := newUsers(repo, env)",
					"server := server{users: users}",
					"wrapped{server: server}",
				},
			},
		},
		{
			name: "two containers built in place choosing one provider",
			src: `
func newImpl() *impl            { return nil }
func newUsers(repo Repo) *Users { return nil }

//kanna:container
type a struct {
	_ Repo ` + "`di:\"with=newImpl\"`" + `

	users *Users
}

//kanna:container
type b struct {
	_ Repo ` + "`di:\"with=newImpl\"`" + `

	users *Users
}

//kanna:container
type both struct {
	*a
	*b
}
`,
			plans: map[string][]string{"both": {
				"newBoth() *both",
				"repo := newImpl()",
				"users := newUsers(repo)",
				"a := &a{users: users}",
				"b := &b{users: users}",
				"both{a: a, b: b}",
			}},
		},
		{
			name: "an argument overriding one of a container built in place",
			src: `
func newUsers(env Env) *Users { return nil }

//kanna:container
type server struct {
	_ Env ` + "`di:\"arg\"`" + `

	users *Users
}

//kanna:container
type wrapped struct {
	_ Env ` + "`di:\"arg=environment\"`" + `

	*server
}
`,
			plans: map[string][]string{
				"server": {"newServer(env Env) *server", "users := newUsers(env)", "server{users: users}"},
				"wrapped": {
					"newWrapped(environment Env) *wrapped",
					"users := newUsers(environment)",
					"server := &server{users: users}",
					"wrapped{server: server}",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, diags := plans(t, map[string]string{"x.go": embedTypes + tt.src})
			for name, want := range tt.plans {
				if !slices.Equal(got[name], want) {
					t.Errorf("%s =\n\t%s\nwant\n\t%s", name, strings.Join(got[name], "\n\t"), strings.Join(want, "\n\t"))
				}
			}
			if len(diags) > 0 {
				t.Errorf("diagnostics = %q, want none", diags)
			}
		})
	}
}

func TestPlans_EmbeddedDiagnostics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, src, want string
	}{
		{
			name: "two embedded structs with one type",
			src: `
type (
	A struct{ DB *DB }
	B struct{ DB *DB }
)

func newUsers(db *DB) *Users { return nil }

//kanna:container
type App struct {
	A ` + "`di:\"arg\"`" + `
	B ` + "`di:\"arg\"`" + `

	Users *Users
}
`,
			want: "error: 2 of what App embeds provide *DB\n  App.A.DB\n  App.B.DB\n  via App.Users → newUsers\n" +
				"  choose a provider with _ *DB `di:\"with=...\"`, or take it with di:\"arg\"",
		},
		{
			name: "one type twice at one depth",
			src: `
type (
	A    struct{ DB *DB }
	B    struct{ DB *DB }
	Deps struct {
		A
		B
	}
)

//kanna:container
type App struct {
	Deps ` + "`di:\"arg\"`" + `

	DB *DB
}
`,
			want: "error: 2 of what App embeds provide *DB\n  App.Deps.A.DB\n  App.Deps.B.DB\n",
		},
		{
			name: "an embedded and a blank struct with one type",
			src: `
type (
	A struct{ DB *DB }
	B struct{ DB *DB }
)

func newUsers(db *DB) *Users { return nil }

//kanna:container
type App struct {
	A   ` + "`di:\"arg\"`" + `
	_ B ` + "`di:\"arg\"`" + `

	Users *Users
}
`,
			want: "error: 2 of what App embeds provide *DB\n  App.A.DB\n  DB of App._ B\n  via App.Users → newUsers\n",
		},
		{
			// Deps is built from a *DB, which its own field cannot be, and
			// the error is reported at the field declaring it.
			name: "a blank struct does not serve what builds it",
			src: `
type Deps struct{ DB *DB }

func newDeps(db *DB) *Deps { return nil }

//kanna:container
type App struct {
	_ *Deps ` + "`di:\"with=newDeps\"`" + `

	db *DB
}
`,
			want: "x.go:21:2: error: nothing provides *DB, which newDeps needs\n  via App._ *Deps → newDeps\n",
		},
		{
			name: "an embedded struct nothing provides",
			src: `
type Deps struct{ DB *DB }

func newUsers(db *DB) *Users { return nil }

//kanna:container
type App struct {
	Users *Users
	Deps
}
`,
			want: "x.go:22:2: error: nothing provides Deps\n",
		},
		{
			name: "a container embedding itself",
			src: `
//kanna:container
type loop struct {
	*loop
}
`,
			want: "error: loop cannot be built in place: it embeds itself (field loop)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, diags := plans(t, map[string]string{"x.go": embedTypes + tt.src})
			if !strings.Contains(strings.Join(diags, "\n")+"\n", tt.want) {
				t.Errorf("diagnostics =\n%s\nwant one to say\n%s", strings.Join(diags, "\n"), tt.want)
			}
		})
	}
}

func TestPlans_AWithOfAContainerBuiltInPlaceIsReportedOnce(t *testing.T) {
	t.Parallel()

	_, diags := plans(t, map[string]string{"x.go": embedTypes + `
func newUsers(repo Repo) *Users { return nil }

//kanna:container
type server struct {
	_ Repo ` + "`di:\"with=nope\"`" + `

	users *Users
}

//kanna:container
type testServer struct {
	*server
}
`})
	if len(diags) != 1 || !strings.Contains(diags[0], "package test has no function nope") {
		t.Errorf("diagnostics = %q, want the with= of server alone", diags)
	}
}

func TestPlans_AContainerOfAnotherPackage(t *testing.T) {
	t.Parallel()

	set, err := packages.Load([]string{"./testdata/embed/infra", "./testdata/embed/app"}, packages.Config{})
	if err != nil || len(set.Diags) > 0 {
		t.Fatalf("load: %v %v", err, set.Diags)
	}

	// infra.Deps cannot be built in place, its fields being out of reach, so
	// its constructor builds it.
	got, diags := gen.Plans(set.Fset, set.Packages, module)
	want := []string{
		"NewApp() *App",
		"deps := infra.NewDeps()",
		"db := deps.DB",
		"users := newUsers(db)",
		"App{Deps: deps, Users: users}",
	}
	if !slices.Equal(got["App"], want) || len(diags) > 0 {
		t.Errorf("App =\n\t%s\nwant\n\t%s\ndiagnostics %q",
			strings.Join(got["App"], "\n\t"), strings.Join(want, "\n\t"), diags)
	}
}
