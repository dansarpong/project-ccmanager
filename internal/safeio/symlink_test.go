package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "AGENTS.md")
	link := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("AGENTS.md", link); err != nil {
		t.Fatal(err)
	}

	if err := AtomicWrite(link, []byte("new"), 0o644); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("CLAUDE.md was replaced by a %v, want the symlink kept", info.Mode().Type())
	}
	if dest, _ := os.Readlink(link); dest != "AGENTS.md" {
		t.Errorf("link now points at %q", dest)
	}
	if got, _ := os.ReadFile(target); string(got) != "new" {
		t.Errorf("target content = %q, want %q", got, "new")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, found %d (temp left behind?)", len(entries))
	}
}
