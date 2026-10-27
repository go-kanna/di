package gen_test

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/go-kanna/internal/packages"
	"github.com/go-kanna/internal/pkgtest"

	"github.com/go-kanna/di/internal/gen"
	"github.com/go-kanna/di/internal/gen/testdata/emit/run"
	_ "github.com/go-kanna/di/internal/gen/testdata/emit/shapes" // compiled for its di_gen.go to be checked
)

// TestEmit regenerates the di_gen.go of packages of testdata and compares it
// with the one there, which the tests calling the constructors compile
// against. Run with -update to write them.
func TestEmit(t *testing.T) {
	t.Parallel()

	shop := []string{"config", "user", "usecase", "postgres", "handler", "app"}
	tests := []struct {
		dir      string
		patterns []string
	}{
		{dir: "testdata/emit/run"},
		{dir: "testdata/emit/shapes"},
		// The example of the README.
		{dir: "testdata/shop/app", patterns: shop},
	}
	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			t.Parallel()

			patterns := []string{"./" + tt.dir}
			for _, p := range tt.patterns {
				patterns = append(patterns, "./"+filepath.Join(filepath.Dir(tt.dir), p))
			}
			set, err := packages.Load(patterns, packages.Config{Regenerated: []string{"di_gen.go"}})
			if err != nil || len(set.Diags) > 0 {
				t.Fatalf("load: %v %v", err, set.Diags)
			}
			i := slices.IndexFunc(set.Packages, func(p *packages.Package) bool {
				return filepath.Base(filepath.Dir(p.GoFiles[0])) == filepath.Base(tt.dir)
			})

			got, diags := gen.Emit(set.Fset, set.Packages, module, set.Packages[i])
			if len(diags) > 0 {
				t.Fatalf("diagnostics = %q", diags)
			}
			pkgtest.Golden(t, filepath.Join(tt.dir, "di_gen.go"), got)
		})
	}
}

func TestGenerated_CleanUpTheLastFirst(t *testing.T) {
	t.Parallel()

	log := &run.Log{}
	app, cleanup, err := run.NewApp(log)
	if err != nil {
		t.Fatal(err)
	}
	if app.Service.Cache.DB != app.Service.DB {
		t.Error("the cache and the service got two databases, not one")
	}

	cleanup()
	want := []string{"open db", "open cache", "open queue", "close cache", "close db"}
	if !slices.Equal(log.Lines, want) {
		t.Errorf("lines = %q, want %q", log.Lines, want)
	}
}

func TestGenerated_UndoWhatOpenedBeforeAFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		fail string
		want []string
	}{
		{fail: "db", want: nil},
		{fail: "cache", want: []string{"open db", "close db"}},
		{fail: "queue", want: []string{"open db", "open cache", "close cache", "close db"}},
	}
	for _, tt := range tests {
		t.Run(tt.fail, func(t *testing.T) {
			t.Parallel()

			log := &run.Log{Fail: tt.fail}
			app, cleanup, err := run.NewApp(log)
			if err == nil || err.Error() != tt.fail+" failed" || app != nil || cleanup != nil {
				t.Errorf("NewApp = %v, %v, %v; want nil, nil, and the error of %s", app, cleanup != nil, err, tt.fail)
			}
			if !slices.Equal(log.Lines, tt.want) {
				t.Errorf("lines = %q, want %q", log.Lines, tt.want)
			}
		})
	}
}

func TestGenerated_Must(t *testing.T) {
	t.Parallel()

	log := &run.Log{}
	app, cleanup := run.MustNewApp(log)
	if app.Service == nil {
		t.Error("MustNewApp built no service")
	}
	cleanup()

	defer func() {
		if r := recover(); r == nil {
			t.Error("MustNewStats did not panic on a failing provider")
		}
	}()
	run.MustNewStats(&run.Log{Fail: "queue"})
}

func TestGenerated_ReturnValueAndInterface(t *testing.T) {
	t.Parallel()

	stats, err := run.NewStats(&run.Log{Fail: "queue"})
	if err == nil || stats != (run.Stats{}) {
		t.Errorf("NewStats = %+v, %v; want the zero Stats and an error", stats, err)
	}

	if got := run.MustNewRunner("alice").Start(); got != "started alice" {
		t.Errorf("Start() = %q, want %q", got, "started alice")
	}
}
