package gen

import (
	"flag"
	"go/token"
	"go/types"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
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
		in := strings.Join(patterns, " ")
		if in == "" {
			in = "."
		}
		diags = append(diags, diag.Warningf(token.Position{}, "no struct is marked //kanna:%s in %s", directiveKey, in))
	}
	return changes, diags
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

	written := outputOf(roots)
	cs, diags := containers(set.Fset, scan.Structs(roots), dir, written)
	loadDiags := withoutPendingCalls(set, cs)
	if diag.HasErrors(loadDiags) {
		for _, path := range set.SetAside {
			loadDiags = append(loadDiags, diag.Warningf(token.Position{Filename: path},
				"set aside because it no longer compiles; it is regenerated once the errors above are fixed"))
		}
		return nil, loadDiags
	}
	if diags = append(loadDiags, diags...); diag.HasErrors(diags) {
		return nil, diags
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
		return nil, diags
	}
	changes, ds := files(roots, planned)
	return changes, append(diags, ds...)
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

// withoutPendingCalls returns the diagnostics of loading set but those of a
// call to a constructor the run is about to generate. A test calling its
// container's constructor, for one, is in the package the container is, and
// cannot compile before the constructor exists; nor can a caller that passes
// and takes what the constructor is about to, while the output of the last
// run still declares it as it was.
func withoutPendingCalls(set packages.Set, cs []container) []diag.Diag {
	pending := make(map[string]bool, 2*len(cs))
	for _, c := range cs {
		pending[c.PkgPath+"."+c.ctor] = true
		if c.must != "" {
			pending[c.PkgPath+"."+c.must] = true
		}
	}

	byFile := make(map[string]*packages.Package)
	for _, pkg := range set.Packages {
		for _, f := range pkg.CompiledGoFiles {
			byFile[f] = pkg
		}
	}
	return slices.DeleteFunc(slices.Clone(set.Diags), func(d diag.Diag) bool {
		expr, ok := called(d.Message)
		pkg := byFile[d.Pos.Filename]
		return ok && pkg != nil && pending[referenced(pkg, d.Pos.Filename, expr)]
	})
}

// called returns the function a load error is about when it is one a
// constructor's signature settles: the function is undefined, or called with
// the wrong number of arguments, or assigned to the wrong number of variables.
// The message's first line names it; the have/want lines below say no more.
func called(msg string) (string, bool) {
	first, _, _ := strings.Cut(msg, "\n")
	for _, prefix := range []string{"undefined: ", "too many arguments in call to ", "not enough arguments in call to "} {
		if expr, ok := strings.CutPrefix(first, prefix); ok {
			return expr, true
		}
	}
	// "assignment mismatch: 2 variables but newServer returns 1 value"
	if rest, ok := strings.CutPrefix(first, "assignment mismatch: "); ok {
		if _, call, ok := strings.Cut(rest, " but "); ok {
			if expr, _, ok := strings.Cut(call, " returns "); ok {
				return expr, true
			}
		}
	}
	return "", false
}

// referenced returns the full name, "<import path>.<name>", of what expr refers
// to in the file filename of pkg: a name of pkg itself, or, qualified, one of
// the package the file imports under the qualifier.
func referenced(pkg *packages.Package, filename, expr string) string {
	qualifier, name, qualified := strings.Cut(expr, ".")
	if !qualified {
		return pkg.PkgPath + "." + expr
	}
	for _, file := range pkg.Syntax {
		if pkg.Fset.Position(file.Pos()).Filename != filename {
			continue
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			local := ""
			if spec.Name != nil {
				local = spec.Name.Name
			} else if imp := imported(pkg.Types, path); imp != nil {
				local = imp.Name()
			}
			if local == qualifier {
				return path + "." + name
			}
		}
	}
	return ""
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

// imported returns the package pkg imports from path, or nil.
func imported(pkg *types.Package, path string) *types.Package {
	if pkg == nil {
		return nil
	}
	for _, imp := range pkg.Imports() {
		if imp.Path() == path {
			return imp
		}
	}
	return nil
}
