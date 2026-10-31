package gen

import (
	"maps"
	"path/filepath"
	"slices"

	"github.com/go-kanna/internal/deps"
	"github.com/go-kanna/internal/packages"
	"github.com/go-kanna/internal/scan"
)

// reportDeps tells what a run in dir reads and writes. A with= names a package
// that nothing imports before the generated code does.
func reportDeps(dir string, cfg config, patterns []string) (deps.Report, error) {
	set, err := packages.Load(patterns, packages.Config{
		Dir: dir, Tags: cfg.tags, Tests: true, Regenerated: []string{outputFile, testOutputFile},
	})
	if err != nil {
		return deps.Report{}, err //nolint:wrapcheck // it says what could not be loaded
	}

	r := deps.Report{Packages: set.Paths(), Tests: true}
	cs, _ := containers(set.Fset, scan.Structs(set.Packages), dir, outputOf(set.Packages))
	r.Declares = slices.Collect(maps.Keys(constructors(cs)))
	for _, c := range cs {
		name := outputFile
		if c.test {
			name = testOutputFile
		}
		r.Writes = append(r.Writes, filepath.Join(filepath.Dir(c.Pos.Filename), name))
		for _, f := range c.fields {
			if f.source == fromRef && f.ref.pkg != "" {
				r.Refs = append(r.Refs, deps.Ref{Package: f.ref.pkg, Via: "with=" + f.ref.pkg + "." + f.ref.fn})
			}
		}
	}
	return r, nil
}
