package safeio

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// TestAtomicWriteStaysInItsDirectory swaps the file's directory for a symlink
// to another one just before the rename, as a racing process might: the temp
// file, the rename and the cleanup must all stay in the directory opened.
func TestAtomicWriteStaysInItsDirectory(t *testing.T) {
	prev := rename
	t.Cleanup(func() { rename = prev })
	for _, fail := range []bool{false, true} {
		base := t.TempDir()
		dir := filepath.Join(base, "work")
		moved := filepath.Join(base, "moved")
		elsewhere := filepath.Join(base, "elsewhere")
		for _, d := range []string{dir, elsewhere} {
			if err := os.Mkdir(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}

		var tmpName string
		rename = func(root *os.Root, oldname, newname string) error {
			tmpName = oldname
			if err := os.Rename(dir, moved); err != nil {
				return err
			}
			if err := os.Symlink(elsewhere, dir); err != nil {
				return err
			}
			if fail {
				return errors.New("rename refused")
			}
			return prev(root, oldname, newname)
		}

		err := AtomicWrite(filepath.Join(dir, "CLAUDE.md"), []byte("new"), 0o644)
		want := "new"
		if fail {
			want = "old"
			if err == nil {
				t.Error("AtomicWrite reported success for a failed rename")
			}
		} else if err != nil {
			t.Fatalf("AtomicWrite: %v", err)
		}
		if !strings.HasPrefix(tmpName, ".ccmanager-") || strings.ContainsRune(tmpName, filepath.Separator) {
			t.Errorf("temp file %q is not a bare name in the directory", tmpName)
		}
		if got, _ := os.ReadFile(filepath.Join(moved, "CLAUDE.md")); string(got) != want {
			t.Errorf("fail=%v: file in the opened directory = %q, want %q", fail, got, want)
		}
		if got := names(t, moved); len(got) != 1 {
			t.Errorf("fail=%v: opened directory holds %v, want just CLAUDE.md (temp left behind?)", fail, got)
		}
		if got := names(t, elsewhere); len(got) != 0 {
			t.Errorf("fail=%v: the write leaked into the swapped-in directory: %v", fail, got)
		}
	}
}

func TestAtomicWriteCreatesAMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	if err := AtomicWrite(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Errorf("created %v, want a regular 0600 file", info.Mode())
	}
}
