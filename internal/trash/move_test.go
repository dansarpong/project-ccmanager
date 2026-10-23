package trash

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// failRename makes every rename fail with errno, restoring os.Rename after the
// test. EXDEV is what a rename across filesystems fails with.
func failRename(t *testing.T, errno syscall.Errno) {
	t.Helper()
	rename = func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: errno}
	}
	t.Cleanup(func() { rename = os.Rename })
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}

func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s should be gone, stat err = %v", path, err)
	}
}

func trashEntries(t *testing.T, tr *Trash) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(tr.trashDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return entries
}

func TestMoveKeepsMode(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		tr, dir := setup(t)
		if fallback {
			failRename(t, syscall.EXDEV)
		}
		path := filepath.Join(dir, "rules", "private.md")
		writeFile(t, path, "secret", 0o640)
		now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

		id, err := tr.MoveToTrash(KindMemoryDelete, path, "", "", now)
		if err != nil {
			t.Fatalf("fallback=%v: MoveToTrash: %v", fallback, err)
		}
		assertGone(t, path)
		e, err := tr.store.GetTrash(id)
		if err != nil {
			t.Fatal(err)
		}
		assertFile(t, e.TrashPath, "secret", 0o640)
		assertMode(t, filepath.Dir(e.TrashPath), 0o700)

		if _, err := tr.Restore(id, now); err != nil {
			t.Fatalf("fallback=%v: Restore: %v", fallback, err)
		}
		assertFile(t, path, "secret", 0o640)
		assertGone(t, filepath.Dir(e.TrashPath))
	}
}

func TestBackupKeepsMode(t *testing.T) {
	tr, dir := setup(t)
	path := filepath.Join(dir, "CLAUDE.local.md")
	writeFile(t, path, "mine", 0o600)

	id, err := tr.BackupBeforeWrite(path, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e, err := tr.store.GetTrash(id)
	if err != nil {
		t.Fatal(err)
	}
	assertFile(t, e.TrashPath, "mine", 0o600)
	assertFile(t, path, "mine", 0o600)
}

func TestRestoredSessionIsPrivate(t *testing.T) {
	tr, dir := setup(t)
	proj := filepath.Join(dir, "projects", "-work")
	path := filepath.Join(proj, "s.jsonl")
	writeFile(t, path, "transcript", 0o600)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	id, err := tr.MoveToTrash(KindSession, path, "s", "", now)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := tr.store.GetTrash(id)
	assertMode(t, e.TrashPath, 0o600)

	// Older versions copied transcripts into the trash without their mode,
	// and the project folder may have gone since.
	if err := os.Chmod(e.TrashPath, 0o664); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(proj); err != nil {
		t.Fatal(err)
	}

	if _, err := tr.Restore(id, now); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	assertFile(t, path, "transcript", 0o600)
	assertMode(t, proj, 0o700)
}

func TestMoveFallbackCopiesTrees(t *testing.T) {
	failRename(t, syscall.EXDEV)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeFile(t, filepath.Join(src, "a.json"), "a", 0o600)
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "b", 0o644)
	if err := os.Symlink("a.json", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0o700); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "dst")
	if err := move(src, dst); err != nil {
		t.Fatalf("move: %v", err)
	}
	assertGone(t, src)
	assertMode(t, dst, 0o700)
	assertFile(t, filepath.Join(dst, "a.json"), "a", 0o600)
	assertMode(t, filepath.Join(dst, "sub"), 0o750)
	assertFile(t, filepath.Join(dst, "sub", "b.txt"), "b", 0o644)
	if target, err := os.Readlink(filepath.Join(dst, "link")); err != nil || target != "a.json" {
		t.Errorf("symlink not kept: target=%q err=%v", target, err)
	}
}

func TestMoveFailsWithoutCopyingOnOtherErrors(t *testing.T) {
	tr, dir := setup(t)
	failRename(t, syscall.EACCES)
	path := filepath.Join(dir, "CLAUDE.md")
	writeFile(t, path, "keep", 0o644)

	if _, err := tr.MoveToTrash(KindMemoryDelete, path, "", "", time.Now()); err == nil {
		t.Fatal("MoveToTrash should fail when rename is refused")
	}
	assertFile(t, path, "keep", 0o644)
	if left := trashEntries(t, tr); len(left) != 0 {
		t.Errorf("trash should be empty, holds %v", left)
	}
}

func TestMoveIsUndoneWhenManifestFails(t *testing.T) {
	tr, dir := setup(t)
	path := filepath.Join(dir, "s.jsonl")
	writeFile(t, path, "transcript", 0o600)
	tr.store.Close()

	if _, err := tr.MoveToTrash(KindSession, path, "s", "", time.Now()); err == nil {
		t.Fatal("MoveToTrash should fail when the manifest can't be written")
	}
	assertFile(t, path, "transcript", 0o600)
	if left := trashEntries(t, tr); len(left) != 0 {
		t.Errorf("trash should be empty, holds %v", left)
	}
}
