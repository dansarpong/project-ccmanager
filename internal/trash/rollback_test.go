package trash

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile is subject to the umask and leaves an existing file's mode alone.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(got) != body {
		t.Errorf("%s content = %q, want %q", path, got, body)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Errorf("%s mode = %o, want %o", path, info.Mode().Perm(), mode)
	}
}

func TestRollBackSwapsBackupIn(t *testing.T) {
	tr, dir := setup(t)
	path := filepath.Join(dir, "CLAUDE.md")
	writeFile(t, path, "v1", 0o640)
	t1 := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	id, err := tr.BackupBeforeWrite(path, t1)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "v2", 0o640)

	// Rolling back at the backup's own timestamp must not trample its entry.
	e, err := tr.Restore(id, t1)
	if err != nil {
		t.Fatalf("roll back: %v", err)
	}
	if e.Kind != KindMemoryBackup {
		t.Errorf("restored kind = %q", e.Kind)
	}
	assertFile(t, path, "v1", 0o640)
	if _, err := os.Stat(e.TrashPath); !os.IsNotExist(err) {
		t.Errorf("rolled-back backup still in the trash: %v", err)
	}

	// The replaced version is now the only entry, and rolling back to it
	// undoes the swap.
	entries, err := tr.store.ListTrash()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Kind != KindMemoryBackup || entries[0].OriginalPath != path {
		t.Fatalf("trash after roll back = %+v, want one backup of %s", entries, path)
	}
	if got, _ := os.ReadFile(entries[0].TrashPath); string(got) != "v2" {
		t.Errorf("backup of the replaced version holds %q, want v2", got)
	}
	if _, err := tr.Restore(entries[0].ID, t1.Add(time.Minute)); err != nil {
		t.Fatalf("undoing the roll back: %v", err)
	}
	assertFile(t, path, "v2", 0o640)
}

func TestRestoreBackupOfDeletedFile(t *testing.T) {
	tr, dir := setup(t)
	path := filepath.Join(dir, "CLAUDE.md")
	writeFile(t, path, "v1", 0o644)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	id, err := tr.BackupBeforeWrite(path, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	if _, err := tr.Restore(id, now); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "v1" {
		t.Errorf("restored content = %q, want v1", got)
	}
	if n, _ := tr.store.CountTrash(); n != 0 {
		t.Errorf("trash holds %d entries, want 0: nothing was replaced", n)
	}
}
