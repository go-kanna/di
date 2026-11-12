package gen

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-kanna/internal/deps"
	"github.com/go-kanna/internal/directive"
	"github.com/go-kanna/internal/packages"
	"github.com/go-kanna/internal/qualifier"
	"github.com/go-kanna/internal/scan"
)

// reportDeps tells what a run in dir reads and writes. A with= names a package
// that nothing imports before the generated code does.
//
// It reads the directives and tags alone, as a container whose types do not
// resolve yet, before another generator writes them, still has its file and
// constructor written.
func reportDeps(dir string, cfg config, patterns []string) (deps.Report, error) {
	set, err := packages.Load(patterns, packages.Config{
		Dir: dir, Tags: cfg.tags, Tests: true, Regenerated: []string{outputFile, testOutputFile},
	})
	if err != nil {
		return deps.Report{}, err //nolint:wrapcheck // it says what could not be loaded
	}

	r := deps.Report{Packages: set.Paths(), Tests: true}
	declares := make(map[string]bool)
	for _, s := range scan.Structs(set.Packages) {
		d, _ := directive.Find(set.Fset, s.Doc, directiveKey)
		if !d.Found {
			continue
		}
		name := outputFile
		if strings.HasSuffix(s.Pos.Filename, "_test.go") {
			name = testOutputFile
		}
		r.Writes = append(r.Writes, filepath.Join(filepath.Dir(s.Pos.Filename), name))

		ctor := defaultCtor(s.Name)
		declares[s.PkgPath+"."+ctor] = true
		d, _ = withoutName(d)
		if args, _ := d.Parse(options); slices.ContainsFunc(args, func(a directive.Arg) bool { return a.Name == "must" }) {
			declares[s.PkgPath+"."+mustOf(ctor)] = true
		}

		imports := qualifier.Of(s.File, s.Named.Obj().Pkg())
		for _, sf := range s.Fields {
			t, err := parseTag(sf.Tag)
			if err != nil || t.source != fromRef || t.ref.pkg == "" {
				continue
			}
			if pkg, err := imports.Lookup(t.ref.pkg); err == nil {
				r.Refs = append(r.Refs, deps.Ref{Package: pkg.Path(), Via: "with=" + t.ref.String()})
			}
		}
	}
	r.Declares = slices.Collect(maps.Keys(declares))
	return r, nil
}
