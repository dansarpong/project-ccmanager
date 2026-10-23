package trash

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/dansarpong/ccmanager/internal/store"
)

func TestPurgeRemovesWholeEntry(t *testing.T) {
	tr, dir := setup(t)
	path := filepath.Join(dir, "s.jsonl")
	writeFile(t, path, "transcript", 0o600)
	id, err := tr.MoveToTrash(KindSession, path, "s", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e, _ := tr.store.GetTrash(id)
	entry := filepath.Dir(e.TrashPath)
	writeFile(t, filepath.Join(entry, "s", "subagents", "agent-1.jsonl"), "{}", 0o600)

	if n, err := tr.EmptyNow(); err != nil || n != 1 {
		t.Fatalf("EmptyNow = %d, %v", n, err)
	}
	assertGone(t, entry)
}

func TestPurgeRefusesPathsOutsideTrash(t *testing.T) {
	tr, dir := setup(t)
	outside := filepath.Join(dir, "work", "CLAUDE.md")
	writeFile(t, outside, "keep", 0o644)
	nested := filepath.Join(tr.trashDir, "1", "deeper", "f.md")
	writeFile(t, nested, "keep", 0o644)
	loose := filepath.Join(tr.trashDir, "f.md")
	writeFile(t, loose, "keep", 0o644)

	bad := []string{
		outside,
		nested,
		loose,
		filepath.Join(tr.trashDir, "..", "work", "CLAUDE.md"),
		"relative/CLAUDE.md",
	}
	for _, p := range bad {
		if _, err := tr.store.InsertTrash(store.TrashEntry{Kind: KindMemoryDelete, OriginalPath: p, TrashPath: p}); err != nil {
			t.Fatal(err)
		}
	}
	good := filepath.Join(dir, "ok.md")
	writeFile(t, good, "x", 0o644)
	if _, err := tr.MoveToTrash(KindMemoryDelete, good, "", "", time.Now()); err != nil {
		t.Fatal(err)
	}

	n, err := tr.EmptyNow()
	if err == nil {
		t.Error("EmptyNow should report the entries it refused")
	}
	if n != 1 {
		t.Errorf("purged %d entries, want only the one inside the trash", n)
	}
	for _, p := range []string{outside, nested, loose} {
		assertFile(t, p, "keep", 0o644)
	}
	if left, _ := tr.store.CountTrash(); left != len(bad) {
		t.Errorf("trash holds %d entries, want the %d refused ones kept", left, len(bad))
	}
}
