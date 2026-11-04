package gen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/tools/go/packages"
)

// modulePackages names the packages of the user's module, by import path: the
// one whose go.mod is above dir. These are the packages kanna-di looks in on
// its own. A go.work does not widen them, so that what a run finds is the same
// in a checkout without one, as in CI.
//
// Only names are read, which the go command lists without compiling anything.
func modulePackages(dir, tags string) (map[string]string, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(context.Background(), "go", "list", "-m", "-f", "{{.Path}}")
	cmd.Dir, cmd.Stderr = dir, &stderr
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, fmt.Errorf("go list -m: %w", err)
	}
	mod := strings.TrimSpace(string(out))

	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedModule, Dir: dir}
	if tags != "" {
		cfg.BuildFlags = []string{"-tags=" + tags}
	}
	pkgs, err := packages.Load(cfg, mod+"/...")
	if err != nil {
		return nil, fmt.Errorf("list the packages of the module: %w", err)
	}

	names := make(map[string]string, len(pkgs))
	for _, p := range pkgs {
		// In a workspace, the pattern also matches the packages of a module
		// nested in this one.
		if p.Module != nil && p.Module.Path == mod {
			names[p.PkgPath] = p.Name
		}
	}
	return names, nil
}
