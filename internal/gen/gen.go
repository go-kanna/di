package gen

import (
	"flag"
	"go/token"
	"go/types"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-kanna/internal/cli"
	"github.com/go-kanna/internal/diag"
	"github.com/go-kanna/internal/output"
	"github.com/go-kanna/internal/packages"
	"github.com/go-kanna/internal/scan"
)

// config holds what kanna-di's own flags set.
type config struct {
	tags string
}

// Command returns kanna-di.
func Command() cli.Command[config] {
	return cli.Command[config]{
		Name:    generator,
		Summary: "write the constructors of structs marked //kanna:" + directiveKey,
		Args:    "[packages]",
		Help: "The packages say where the containers are, not where providers come from,\n" +
			"and default to the one in the current directory, so that\n" +
			"//go:generate go tool " + generator + " needs no argument.",
		Flags: func(fs *flag.FlagSet, cfg *config) {
			fs.StringVar(&cfg.tags, "tags", "", "comma-separated build tags")
		},
		Run: run,
	}
}

func run(dir string, cfg config, patterns []string) ([]output.Change, []diag.Diag) {
	module, err := modulePackages(dir, cfg.tags)
	if err != nil {
		return nil, []diag.Diag{diag.Errorf(token.Position{}, "%v", err)}
	}
	load := func(patterns []string) (packages.Set, error) {
		return packages.Load(patterns, packages.Config{
			Dir:  dir,
			Tags: cfg.tags,
			// A container declared in a test file is generated into
			// testOutputFile, and a provider declared there serves it.
			Tests: true,
			// A provider may be in a package the containers reach only
			// through another, whose export data would leave its functions
			// out.
			Deps:        true,
			Regenerated: []string{outputFile, testOutputFile},
		})
	}

	set, err := load(patterns)
	if err != nil {
		return nil, []diag.Diag{diag.Errorf(token.Position{}, "%v", err)}
	}
	targets := make(map[string]bool, len(set.Packages))
	for _, p := range set.Packages {
		targets[p.PkgPath] = true
	}

	// A with= may name a package nothing loaded imports yet, as it is on the
	// first run, before the generated code does. Its types have to come from
	// the same load as the rest, or none of its types would be those the
	// containers know, so the packages are loaded again together.
	if extra := unloaded(set, module, dir); len(extra) > 0 {
		if len(patterns) == 0 {
			patterns = []string{"."}
		}
		if set, err = load(append(slices.Clip(patterns), extra...)); err != nil {
			return nil, []diag.Diag{diag.Errorf(token.Position{}, "%v", err)}
		}
	}

	changes, diags := generate(set, targets, module, dir)
	if !diag.HasErrors(diags) && !slices.ContainsFunc(changes, func(c output.Change) bool { return !c.Remove }) {
		// Nothing to stop for: a go:generate line left behind does no harm.
		diags = append(diags, diag.Warningf(token.Position{}, "no struct is marked //kanna:%s in %s",
			directiveKey, where(targets, patterns)))
	}
	return changes, diags
}

// where names the packages of targets for a message, by import path: under
// go generate ./..., each run is given "." alone. An external test package
// goes with the package it tests. A run that matched no package names its
// patterns instead.
func where(targets map[string]bool, patterns []string) string {
	var paths []string
	for _, path := range slices.Sorted(maps.Keys(targets)) {
		if tested, ok := strings.CutSuffix(path, "_test"); !ok || !targets[tested] {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		paths = patterns
	}
	return strings.Join(paths, ", ")
}

// generate works out the changes for the packages of set that targets names:
// the output of each that declares containers, and the removal of what an
// earlier run left in one that no longer does. module names the packages of
// the user's module, and dir is where the run works.
func generate(
	set packages.Set, targets map[string]bool, module map[string]string, dir string,
) ([]output.Change, []diag.Diag) {
	var roots []*packages.Package
	for _, p := range set.Packages {
		if targets[p.PkgPath] {
			roots = append(roots, p)
		}
	}

	// A package loaded for what a with= names comes with its tests, which a
	// run loads no more once the output imports the package. So no error of
	// them counts on the first run either.
	others := testsOutside(set, targets)
	set.Diags = slices.DeleteFunc(slices.Clone(set.Diags), others)

	// Code calling a constructor does not compile before the run writes it,
	// nor does code calling it as the run is about to change it. So a type
	// error of a package loaded for the containers does not stop the run here:
	// packages.Recheck tells, once the output is in place, whether it remains.
	blocking, deferred := set.Split()
	if diag.HasErrors(blocking) {
		return nil, append(blocking, set.SetAsideWarnings()...)
	}

	written := outputOf(roots)
	cs, diags := containers(set.Fset, scan.Structs(roots), dir, written)
	if diag.HasErrors(diags) {
		// What is wrong with a container may well be one of the type errors,
		// but for those of a call to its constructor.
		return nil, packages.Stopped(deferred, diags, set.Pending(constructors(cs)))
	}

	w := &world{
		x:          newIndex(set.Fset),
		local:      func(p *types.Package) bool { return hasKey(module, p.Path()) },
		dir:        dir,
		containers: make(map[string]container, len(cs)),
		output:     written,
	}
	for _, p := range reachable(set.Packages) {
		if w.local(p) {
			w.refs = append(w.refs, p)
		}
	}
	for _, c := range cs {
		w.containers[typeKey(c.Named)] = c
	}

	planned, ds := plans(w, cs)
	if diags = append(diags, ds...); diag.HasErrors(diags) {
		return nil, packages.Stopped(deferred, diags, set.Pending(constructors(cs)))
	}
	changes, ds := files(roots, planned)
	if diags = append(diags, ds...); diag.HasErrors(diags) {
		return nil, diags
	}
	if rds := slices.DeleteFunc(packages.Recheck(set, changes), others); len(rds) > 0 {
		return nil, slices.Concat(diags, rds, set.SetAsideWarnings())
	}
	return changes, diags
}

// testsOutside returns whether a diagnostic is about a test file of a package
// of set that targets does not name: one loaded for what a with= names, or
// the external test package of one.
func testsOutside(set packages.Set, targets map[string]bool) func(diag.Diag) bool {
	files := make(map[string]bool)
	for _, p := range set.Packages {
		if targets[p.PkgPath] {
			continue
		}
		for _, f := range p.CompiledGoFiles {
			if strings.HasSuffix(f, "_test.go") {
				files[f] = true
			}
		}
	}
	return func(d diag.Diag) bool { return files[d.Pos.Filename] }
}

// outputOf returns the files of roots that an earlier run wrote, by absolute
// path: those named as kanna-di names its output and carrying the generated
// header, which a hand-written file of the name does not.
func outputOf(roots []*packages.Package) map[string]bool {
	written := make(map[string]bool)
	for _, pkg := range roots {
		for path := range packages.GeneratedFiles(pkg) {
			if base := filepath.Base(path); base == outputFile || base == testOutputFile {
				written[path] = true
			}
		}
	}
	return written
}

// files renders the plans into the files they go to, and removes what an
// earlier run left in a package of roots that no longer has containers for
// it.
func files(roots []*packages.Package, planned []plan) ([]output.Change, []diag.Diag) {
	type file struct {
		pkgPath, pkgName string
		plans            []plan
	}
	byPath := make(map[string]*file)
	var diags []diag.Diag
	for _, p := range planned {
		name := outputFile
		if p.c.test {
			name = testOutputFile
		}
		path := filepath.Join(filepath.Dir(p.c.Pos.Filename), name)
		f, ok := byPath[path]
		switch {
		case !ok:
			f = &file{pkgPath: p.c.PkgPath, pkgName: p.c.PkgName}
			byPath[path] = f
		case f.pkgPath != p.c.PkgPath:
			// One file declares one package.
			diags = append(diags, diag.Errorf(p.c.Pos, "both %s and %s declare test containers in this directory, "+
				"which would share %s; keep them in one of the two", f.pkgName, p.c.PkgName, testOutputFile))
			continue
		}
		f.plans = append(f.plans, p)
	}
	if diag.HasErrors(diags) {
		return nil, diags
	}

	var changes []output.Change
	for _, path := range slices.Sorted(maps.Keys(byPath)) {
		f := byPath[path]
		declared := slices.Sorted(maps.Keys(output.DeclaredNames(filepath.Dir(path), filepath.Base(path), f.pkgName)))
		gf := emit(f.pkgPath, f.pkgName, f.plans, declared)
		src, err := gf.Bytes()
		if err != nil {
			return nil, append(diags, diag.Errorf(token.Position{Filename: path}, "generated code does not parse: %v", err))
		}
		changes = append(changes, output.Change{Path: path, Data: src})
		diags = append(diags, output.MissingRequires(path, gf.ModuleImports())...)
	}

	// A package whose containers have all gone keeps the constructors an
	// earlier run gave them unless the file goes as well.
	removed := make(map[string]bool)
	for _, pkg := range roots {
		if len(pkg.GoFiles) == 0 {
			continue
		}
		generated := packages.GeneratedFiles(pkg)
		for _, name := range []string{outputFile, testOutputFile} {
			path := filepath.Join(filepath.Dir(pkg.GoFiles[0]), name)
			if generated[path] && !hasKey(byPath, path) && !removed[path] {
				removed[path] = true
				changes = append(changes, output.Change{Path: path, Remove: true})
			}
		}
	}
	return changes, diags
}

// constructors returns the full names of the constructors of cs, which the
// run writes.
func constructors(cs []container) map[string]bool {
	names := make(map[string]bool, 2*len(cs))
	for _, c := range cs {
		names[c.PkgPath+"."+c.ctor] = true
		if c.must != "" {
			names[c.PkgPath+"."+c.must] = true
		}
	}
	return names
}

// unloaded returns the packages of the module that a with= of the containers
// of set may name, and that are not among the packages loaded with set, nor
// imported by them. dir is where the run works.
func unloaded(set packages.Set, module map[string]string, dir string) []string {
	cs, _ := containers(set.Fset, scan.Structs(set.Packages), dir, outputOf(set.Packages))
	loaded := make(map[string]bool)
	for _, p := range reachable(set.Packages) {
		loaded[p.Path()] = true
	}

	var extra []string
	for _, c := range cs {
		for _, f := range c.fields {
			if f.source != fromRef || f.ref.pkg == "" {
				continue
			}
			for path, name := range module {
				if qualifies(f.ref.pkg, path, name) && !loaded[path] && !slices.Contains(extra, path) {
					extra = append(extra, path)
				}
			}
		}
	}
	slices.Sort(extra)
	return extra
}

// reachable returns the packages of pkgs and every package they import, one
// for each import path, preferring those of pkgs, which for a package loaded
// with its tests holds the functions of its test files too.
func reachable(pkgs []*packages.Package) []*types.Package {
	seen := make(map[string]bool)
	var out []*types.Package
	var walk func(p *types.Package)
	walk = func(p *types.Package) {
		if p == nil || seen[p.Path()] {
			return
		}
		seen[p.Path()] = true
		out = append(out, p)
		for _, imp := range p.Imports() {
			walk(imp)
		}
	}
	for _, p := range pkgs {
		if p.Types != nil && !seen[p.PkgPath] {
			seen[p.PkgPath] = true
			out = append(out, p.Types)
		}
	}
	for _, p := range pkgs {
		if p.Types != nil {
			for _, imp := range p.Types.Imports() {
				walk(imp)
			}
		}
	}
	return out
}
