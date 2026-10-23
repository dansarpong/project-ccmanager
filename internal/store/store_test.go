package store

import (
	"os"
	"path/filepath"
	"testing"
)

func assertPrivate(t *testing.T, path string) {
	t.Helper()
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(p), got)
		}
	}
}

func TestOpenCreatesPrivateDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if _, err := st.InsertTrash(TrashEntry{Kind: "session", OriginalPath: "/a", TrashPath: "/b"}); err != nil {
		t.Fatal(err)
	}
	assertPrivate(t, path)
}

func TestOpenTightensExistingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// An earlier install left the database and its WAL files readable by
	// everyone; the WAL files outlive a crash.
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer again.Close()
	assertPrivate(t, path)
}
