package server

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dansarpong/ccmanager/internal/safeio"
	"github.com/dansarpong/ccmanager/internal/trash"
)

func changeToken(t *testing.T, path string) string {
	t.Helper()
	snap, err := safeio.Snapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%d:%d", snap.Mtime, snap.Size)
}

func assertSymlink(t *testing.T, link, dest string) {
	t.Helper()
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is a %v, want the symlink kept", link, info.Mode().Type())
	}
	if got, _ := os.Readlink(link); got != dest {
		t.Errorf("%s now points at %q, want %q", link, got, dest)
	}
}

func TestMemoryRoutesRefuseSymlinksOutOfScope(t *testing.T) {
	h, cfg := newTestServer(t)
	bashrc := filepath.Join(t.TempDir(), ".bashrc")
	writeTestFile(t, bashrc, "export SECRET_SAUCE=1\n")
	link := symlink(t, bashrc, filepath.Join(cfg.ConfigRoot, "work", "sub", "CLAUDE.md"))
	token := changeToken(t, bashrc)

	rec := do(t, h, "GET", "/memory/edit?path="+url.QueryEscape(link), nil, false)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "SECRET_SAUCE") {
		t.Errorf("edit = %d %q, want 403 without the target's contents", rec.Code, rec.Body.String())
	}
	assertContains(t, "edit refusal", rec.Body.String(), "resolves to "+bashrc)

	rec = do(t, h, "POST", "/memory/save", url.Values{"path": {link}, "content": {"pwned"}, "token": {token}}, false)
	if rec.Code != http.StatusForbidden {
		t.Errorf("save = %d, want 403", rec.Code)
	}
	assertContains(t, "save refusal", rec.Body.String(), "outside what ccmanager edits")
	rec = do(t, h, "POST", "/advisor/apply", url.Values{"file": {link}, "original": {"SECRET_SAUCE=1"}, "suggestion": {"pwned"}}, false)
	if rec.Code != http.StatusForbidden {
		t.Errorf("advisor apply = %d, want 403", rec.Code)
	}
	if got := readBody(t, bashrc); got != "export SECRET_SAUCE=1\n" {
		t.Errorf("refused writes changed the target to %q", got)
	}
	assertSymlink(t, link, bashrc)
	assertContains(t, "trash", do(t, h, "GET", "/trash", nil, false).Body.String(), "Trash is empty")

	// Deleting moves the link itself and leaves the target alone.
	if rec := do(t, h, "POST", "/memory/delete", url.Values{"path": {link}}, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("link still in place after delete: %v", err)
	}
	if got := readBody(t, bashrc); got != "export SECRET_SAUCE=1\n" {
		t.Errorf("deleting the link changed the target to %q", got)
	}
}

func TestSaveThroughSymlinkBesideItsTarget(t *testing.T) {
	h, _, st, cfg := newTrashTestServer(t)
	agents := filepath.Join(cfg.ConfigRoot, "work", "AGENTS.md")
	writeTestFile(t, agents, "v1\n")
	link := symlink(t, "AGENTS.md", filepath.Join(cfg.ConfigRoot, "work", "CLAUDE.md"))

	rec := do(t, h, "GET", "/memory/edit?path="+url.QueryEscape(link), nil, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit = %d: %s", rec.Code, rec.Body.String())
	}
	assertContains(t, "edit", rec.Body.String(), "v1")

	rec = do(t, h, "POST", "/memory/save", url.Values{"path": {link}, "content": {"v2\n"}, "token": {changeToken(t, agents)}}, false)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "saved=1") {
		t.Fatalf("save = %d to %q: %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if got := readBody(t, agents); got != "v2\n" {
		t.Errorf("AGENTS.md = %q, want v2", got)
	}
	assertSymlink(t, link, "AGENTS.md")

	entries, err := st.ListTrash()
	if err != nil || len(entries) != 1 {
		t.Fatalf("trash = %+v err=%v, want one backup", entries, err)
	}
	backup := entries[0]
	if backup.Kind != trash.KindMemoryBackup || backup.OriginalPath != link {
		t.Errorf("backup = %s of %s, want a %s of %s", backup.Kind, backup.OriginalPath, trash.KindMemoryBackup, link)
	}
	if got := readBody(t, backup.TrashPath); got != "v1\n" {
		t.Errorf("backup holds %q, want AGENTS.md's prior v1", got)
	}

	// Rolling back writes through the link again.
	if rec := do(t, h, "POST", fmt.Sprintf("/trash/%d/restore", backup.ID), nil, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("roll back = %d: %s", rec.Code, rec.Body.String())
	}
	if got := readBody(t, agents); got != "v1\n" {
		t.Errorf("AGENTS.md after roll back = %q, want v1", got)
	}
	assertSymlink(t, link, "AGENTS.md")
}

func TestRollBackRefusesOriginalNowLinkedOutOfScope(t *testing.T) {
	h, tr, st, cfg := newTrashTestServer(t)
	path := filepath.Join(cfg.ConfigRoot, "work", "CLAUDE.md")
	writeTestFile(t, path, "v1\n")
	id, err := tr.BackupBeforeWrite(path, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	bashrc := filepath.Join(t.TempDir(), ".bashrc")
	writeTestFile(t, bashrc, "export SECRET_SAUCE=1\n")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	symlink(t, bashrc, path)

	rec := do(t, h, "POST", fmt.Sprintf("/trash/%d/restore", id), nil, false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("roll back = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	assertContains(t, "roll back refusal", rec.Body.String(), "resolves to "+bashrc)
	if got := readBody(t, bashrc); got != "export SECRET_SAUCE=1\n" {
		t.Errorf("refused roll back changed the target to %q", got)
	}
	assertSymlink(t, path, bashrc)
	entries, err := st.ListTrash()
	if err != nil || len(entries) != 1 || entries[0].ID != id {
		t.Fatalf("trash after a refused roll back = %+v err=%v, want just the backup", entries, err)
	}
	if got := readBody(t, entries[0].TrashPath); got != "v1\n" {
		t.Errorf("backup now holds %q", got)
	}

	// A broken link is left to Restore, which refuses to replace it.
	gone := filepath.Join(filepath.Dir(bashrc), "gone.md")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	symlink(t, gone, path)
	rec = do(t, h, "POST", fmt.Sprintf("/trash/%d/restore", id), nil, false)
	if rec.Code < 400 {
		t.Fatalf("roll back onto a broken link = %d, want it refused", rec.Code)
	}
	assertContains(t, "broken link refusal", rec.Body.String(), "symlink to a missing file")
	assertSymlink(t, path, gone)
	if _, err := os.Lstat(gone); !os.IsNotExist(err) {
		t.Errorf("refused roll back created the link's target: %v", err)
	}
}
