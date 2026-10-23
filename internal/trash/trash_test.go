package trash

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dansarpong/ccmanager/internal/store"
)

func setup(t *testing.T) (*Trash, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, filepath.Join(dir, "trash")), dir
}

func TestMoveAndRestore(t *testing.T) {
	tr, dir := setup(t)
	orig := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(orig, []byte("transcript"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	id, err := tr.MoveToTrash(KindSession, orig, "sess-1", "", now)
	if err != nil {
		t.Fatalf("MoveToTrash: %v", err)
	}
	if _, err := os.Stat(orig); !os.IsNotExist(err) {
		t.Error("original should be gone after MoveToTrash")
	}

	if _, err := tr.Restore(id, now); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, err := os.ReadFile(orig)
	if err != nil || string(got) != "transcript" {
		t.Errorf("restored content = %q err=%v", got, err)
	}
}

func TestRestoreRefusesToClobber(t *testing.T) {
	tr, dir := setup(t)
	orig := filepath.Join(dir, "f.md")
	os.WriteFile(orig, []byte("v1"), 0o644)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	id, _ := tr.MoveToTrash(KindMemoryDelete, orig, "", "", now)

	// Recreate a file at the original path; restore must refuse.
	os.WriteFile(orig, []byte("new content"), 0o644)
	if _, err := tr.Restore(id, now); err == nil {
		t.Error("Restore should refuse to overwrite an existing file")
	}
}

func TestBackupBeforeWrite(t *testing.T) {
	tr, dir := setup(t)
	path := filepath.Join(dir, "CLAUDE.md")
	os.WriteFile(path, []byte("original"), 0o644)
	now := time.Now()

	id, err := tr.BackupBeforeWrite(path, now)
	if err != nil || id == 0 {
		t.Fatalf("BackupBeforeWrite: id=%d err=%v", id, err)
	}
	// Original is left in place (backup copies, does not move).
	if _, err := os.Stat(path); err != nil {
		t.Error("backup should not remove the original")
	}

	// Backing up a non-existent file is a no-op.
	id2, err := tr.BackupBeforeWrite(filepath.Join(dir, "nope.md"), now)
	if err != nil || id2 != 0 {
		t.Errorf("backup of missing file should be a no-op, got id=%d err=%v", id2, err)
	}
}

func TestPurgeExpired(t *testing.T) {
	tr, dir := setup(t)
	orig := filepath.Join(dir, "old.jsonl")
	os.WriteFile(orig, []byte("x"), 0o644)

	// Trash it "35 days ago" so its purge_after is already in the past.
	deleted := time.Now().AddDate(0, 0, -(RetentionDays + 5))
	id, err := tr.MoveToTrash(KindSession, orig, "s", "", deleted)
	if err != nil {
		t.Fatal(err)
	}

	n, err := tr.PurgeExpired(time.Now())
	if err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 entry purged, got %d", n)
	}
	if _, err := tr.store.GetTrash(id); err == nil {
		t.Error("purged entry should no longer exist in the manifest")
	}
}

func TestPurgeKeepsFreshEntries(t *testing.T) {
	tr, dir := setup(t)
	orig := filepath.Join(dir, "fresh.jsonl")
	os.WriteFile(orig, []byte("x"), 0o644)

	// Trashed just now: well within the retention window.
	if _, err := tr.MoveToTrash(KindSession, orig, "s", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	n, err := tr.PurgeExpired(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("fresh entry should not be purged, purged %d", n)
	}
}
