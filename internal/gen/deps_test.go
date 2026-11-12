package gen_test

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/go-kanna/internal/deps"
)

// Code calling what the run writes does not compile before it, which is when
// the kanna command asks.
func TestDeps(t *testing.T) {
	t.Parallel()

	dir := writeModule(t, map[string]string{
		"impl/impl.go": "package impl\n\ntype T struct{}\n\nfunc (T) M() {}\n\nfunc New() T { return T{} }\n",
		"p/p.go": "package p\n\nimport _ \"example.com/shop/impl\"\n\ntype I interface{ M() }\n\n" +
			"//kanna:container\ntype C struct {\n\ti I `di:\"with=impl.New\"`\n}\n\nvar _ = NewC\n",
		"p/p_test.go": "package p\n\n//kanna:container\ntype T struct{ c C }\n",
	})

	code, stdout, stderr := execute(t, filepath.Join(dir, "p"), "-deps")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var r deps.Report
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(r.Packages, []string{"example.com/shop/p"}) {
		t.Errorf("Packages = %q", r.Packages)
	}
	if !r.Tests {
		t.Error("Tests = false, want true: kanna-di loads them")
	}
	files := make([]string, len(r.Writes))
	for i, w := range r.Writes {
		files[i] = filepath.Base(w)
	}
	if want := []string{"di_gen.go", "di_gen_test.go"}; !slices.Equal(files, want) {
		t.Errorf("Writes = %q, want %q", files, want)
	}
	if want := []string{"example.com/shop/p.NewC", "example.com/shop/p.NewT"}; !slices.Equal(r.Declares, want) {
		t.Errorf("Declares = %q, want %q", r.Declares, want)
	}
	want := []deps.Ref{{Package: "example.com/shop/impl", Via: "with=impl.New"}}
	if !slices.Equal(r.Refs, want) {
		t.Errorf("Refs = %+v, want %+v", r.Refs, want)
	}
}

// Before another generator writes the types a container names, they do not
// resolve, and the run still has its file and constructor written.
func TestDeps_Unresolved(t *testing.T) {
	t.Parallel()

	dir := writeModule(t, map[string]string{
		"impl/impl.go": "package impl\n",
		"p/p.go": "package p\n\nimport \"example.com/shop/impl\"\n\n" +
			"//kanna:container must\ntype C struct {\n\t_ impl.Repo `di:\"with=impl.NewRepo\"`\n}\n",
	})

	code, stdout, stderr := execute(t, filepath.Join(dir, "p"), "-deps")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var r deps.Report
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatal(err)
	}

	if want := []string{filepath.Join(dir, "p", "di_gen.go")}; !slices.Equal(r.Writes, want) {
		t.Errorf("Writes = %q, want %q", r.Writes, want)
	}
	if want := []string{"example.com/shop/p.MustNewC", "example.com/shop/p.NewC"}; !slices.Equal(r.Declares, want) {
		t.Errorf("Declares = %q, want %q", r.Declares, want)
	}
	want := []deps.Ref{{Package: "example.com/shop/impl", Via: "with=impl.NewRepo"}}
	if !slices.Equal(r.Refs, want) {
		t.Errorf("Refs = %+v, want %+v", r.Refs, want)
	}
}
