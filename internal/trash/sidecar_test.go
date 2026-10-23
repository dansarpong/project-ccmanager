package trash

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// writeSession lays out a transcript and its sidecar folder the way Claude
// Code does, returning the transcript and sidecar paths.
func writeSession(t *testing.T, dir string) (string, string) {
	t.Helper()
	proj := filepath.Join(dir, "projects", "-work")
	transcript := filepath.Join(proj, "s1.jsonl")
	side := filepath.Join(proj, "s1")
	writeFile(t, transcript, "transcript", 0o600)
	writeFile(t, filepath.Join(side, "subagents", "agent-a.jsonl"), "subagent", 0o600)
	writeFile(t, filepath.Join(side, "tool-results", "r1.txt"), "result", 0o600)
	return transcript, side
}

func assertSidecar(t *testing.T, side string) {
	t.Helper()
	assertFile(t, filepath.Join(side, "subagents", "agent-a.jsonl"), "subagent", 0o600)
	assertFile(t, filepath.Join(side, "tool-results", "r1.txt"), "result", 0o600)
}

func TestSessionSidecarTrashedAndRestored(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		tr, dir := setup(t)
		if fallback {
			failRename(t, syscall.EXDEV)
		}
		transcript, side := writeSession(t, dir)
		now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

		id, err := tr.MoveToTrash(KindSession, transcript, "s1", "", now)
		if err != nil {
			t.Fatalf("fallback=%v: MoveToTrash: %v", fallback, err)
		}
		assertGone(t, transcript)
		assertGone(t, side)
		e, _ := tr.store.GetTrash(id)
		assertSidecar(t, strings.TrimSuffix(e.TrashPath, ".jsonl"))

		if _, err := tr.Restore(id, now); err != nil {
			t.Fatalf("fallback=%v: Restore: %v", fallback, err)
		}
		assertFile(t, transcript, "transcript", 0o600)
		assertSidecar(t, side)
		assertGone(t, filepath.Dir(e.TrashPath))
	}
}

func TestRestoreRefusesExistingSidecar(t *testing.T) {
	tr, dir := setup(t)
	transcript, side := writeSession(t, dir)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	id, err := tr.MoveToTrash(KindSession, transcript, "s1", "", now)
	if err != nil {
		t.Fatal(err)
	}
	// Claude Code started a new sidecar at the same path.
	writeFile(t, filepath.Join(side, "subagents", "new.jsonl"), "new", 0o600)

	if _, err := tr.Restore(id, now); err == nil {
		t.Fatal("Restore should refuse when the sidecar folder exists")
	}
	// Nothing moved: the transcript is still in the trash.
	assertGone(t, transcript)
	e, err := tr.store.GetTrash(id)
	if err != nil {
		t.Fatalf("entry should still be in the manifest: %v", err)
	}
	assertFile(t, e.TrashPath, "transcript", 0o600)
	assertSidecar(t, strings.TrimSuffix(e.TrashPath, ".jsonl"))
}

func TestRestoreLegacySessionWithoutSidecar(t *testing.T) {
	tr, dir := setup(t)
	transcript := filepath.Join(dir, "projects", "-work", "s1.jsonl")
	writeFile(t, transcript, "transcript", 0o600)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	id, err := tr.MoveToTrash(KindSession, transcript, "s1", "", now)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := tr.Restore(id, now); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	assertFile(t, transcript, "transcript", 0o600)
	assertGone(t, strings.TrimSuffix(transcript, ".jsonl"))
}

func TestSidecarMoveIsUndoneWhenManifestFails(t *testing.T) {
	tr, dir := setup(t)
	transcript, side := writeSession(t, dir)
	tr.store.Close()

	if _, err := tr.MoveToTrash(KindSession, transcript, "s1", "", time.Now()); err == nil {
		t.Fatal("MoveToTrash should fail when the manifest can't be written")
	}
	assertFile(t, transcript, "transcript", 0o600)
	assertSidecar(t, side)
	if left := trashEntries(t, tr); len(left) != 0 {
		t.Errorf("trash should be empty, holds %v", left)
	}
}
