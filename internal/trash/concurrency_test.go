package trash

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRestoreTwiceAppliesOnce(t *testing.T) {
	tr, dir := setup(t)
	path := filepath.Join(dir, "CLAUDE.md")
	writeFile(t, path, "v1", 0o644)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	id, err := tr.BackupBeforeWrite(path, now)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "v2", 0o644)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = tr.Restore(id, now)
		}()
	}
	wg.Wait()

	gone := 0
	for _, err := range errs {
		if errors.Is(err, sql.ErrNoRows) {
			gone++
		} else if err != nil {
			t.Errorf("Restore: %v", err)
		}
	}
	if gone != 1 {
		t.Errorf("%d restores found the entry gone, want exactly 1: %v", gone, errs)
	}
	assertFile(t, path, "v1", 0o644)
	entries, err := tr.store.ListTrash()
	if err != nil || len(entries) != 1 {
		t.Fatalf("trash after a double roll back = %d entries (err %v), want only the replaced version", len(entries), err)
	}
}

func TestPurgeThroughSymlinkedTrashDir(t *testing.T) {
	tr, dir := setup(t)
	path := filepath.Join(dir, "rules", "x.md")
	writeFile(t, path, "rule", 0o644)
	if _, err := tr.MoveToTrash(KindMemoryDelete, path, "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "cfglink")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}

	viaLink := New(tr.store, filepath.Join(link, "trash"))
	if n, err := viaLink.EmptyNow(); err != nil || n != 1 {
		t.Fatalf("EmptyNow through a symlinked dir = %d, %v; want 1, nil", n, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "trash")); len(entries) != 0 {
		t.Errorf("trash dir still holds %d entries", len(entries))
	}
}

func TestRollBackRefusesBrokenSymlink(t *testing.T) {
	tr, dir := setup(t)
	target := filepath.Join(dir, "AGENTS.md")
	link := filepath.Join(dir, "CLAUDE.md")
	writeFile(t, target, "v1", 0o644)
	if err := os.Symlink("AGENTS.md", link); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	id, err := tr.BackupBeforeWrite(link, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	if _, err := tr.Restore(id, now); !errors.Is(err, ErrBrokenLink) {
		t.Fatalf("Restore onto a broken symlink = %v, want ErrBrokenLink", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v", err)
	}
	if _, err := tr.store.GetTrash(id); err != nil {
		t.Errorf("the refused backup left the trash: %v", err)
	}
}
