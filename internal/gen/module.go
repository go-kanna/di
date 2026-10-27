package gen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"golang.org/x/tools/go/packages"
)

// modulePackages names the packages of the user's module, by import path: the
// module dir is in, or every module of the workspace it is in. These are the
// packages kanna-di looks in on its own, and those a with= may name.
//
// Only names are read, which the go command lists without compiling anything.
func modulePackages(dir, tags string) (map[string]string, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(context.Background(), "go", "list", "-m", "-f", "{{.Path}}")
	cmd.Dir, cmd.Stderr = dir, &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, fmt.Errorf("go list -m: %w", err)
	}

	var patterns []string
	for mod := range strings.FieldsSeq(string(out)) {
		patterns = append(patterns, mod+"/...")
	}
	cfg := &packages.Config{Mode: packages.NeedName, Dir: dir}
	if tags != "" {
		cfg.BuildFlags = []string{"-tags=" + tags}
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("list the packages of the module: %w", err)
	}

	names := make(map[string]string, len(pkgs))
	for _, p := range pkgs {
		names[p.PkgPath] = p.Name
	}
	return names, nil
}
