package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	dir := t.TempDir()
	if c := Detect(dir); c.Test != "" {
		t.Fatalf("empty folder: %+v", c)
	}
	_ = os.WriteFile(filepath.Join(dir, "Package.swift"), nil, 0o644)
	if c := Detect(dir); c.Test != "swift test" {
		t.Fatalf("swift: %+v", c)
	}
}

func TestNameFor(t *testing.T) {
	if got := NameFor("/x/My App:v2"); got != "My App-v2" {
		t.Fatalf("got %q", got)
	}
	if got := NameFor("/"); got == "" || strings.ContainsAny(got, `/\`) {
		t.Fatalf("got %q", got)
	}
}

func TestDocsNeverOverwrite(t *testing.T) {
	root := t.TempDir()
	made, err := Docs(root, "docs/decisions.md", "docs/handoff.md")
	if err != nil || len(made) != 2 {
		t.Fatalf("made %v err %v", made, err)
	}
	p := filepath.Join(root, "docs", "decisions.md")
	_ = os.WriteFile(p, []byte("mine"), 0o644)
	if made, _ := Docs(root, "docs/decisions.md", "docs/handoff.md"); len(made) != 0 {
		t.Fatal("second run created files again")
	}
	if b, _ := os.ReadFile(p); string(b) != "mine" {
		t.Fatal("existing decisions.md was overwritten")
	}
}

func TestVaultLinkLifecycle(t *testing.T) {
	root := t.TempDir()
	vault := filepath.Join(root, "vault")
	p := Project{Name: "Demo", Path: filepath.Join(root, "repo")}
	if err := Link(vault, p, "docs"); err != nil {
		t.Fatal(err)
	}
	if st := Inspect(vault, p, "docs"); st != LinkOK {
		t.Fatalf("state %s", st)
	}
	if err := Link(vault, p, "docs"); err != nil {
		t.Fatal("relinking must be a no-op:", err)
	}
	_ = os.WriteFile(filepath.Join(p.Path, "docs", "x.md"), []byte("hi"), 0o644)
	if b, err := os.ReadFile(filepath.Join(VaultLink(vault, "Demo"), "x.md")); err != nil || string(b) != "hi" {
		t.Fatal("vault does not show project docs")
	}
	if err := Unlink(vault, "Demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.Path, "docs", "x.md")); err != nil {
		t.Fatal("unlink removed project docs")
	}
	// A real folder in the vault is never replaced or removed.
	real := VaultLink(vault, "Demo")
	_ = os.MkdirAll(real, 0o755)
	if Link(vault, p, "docs") == nil || Unlink(vault, "Demo") == nil {
		t.Fatal("a real folder in the vault must be left alone")
	}
}
