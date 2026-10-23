package server

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/dansarpong/ccmanager/internal/config"
	"github.com/dansarpong/ccmanager/internal/store"
	"github.com/dansarpong/ccmanager/internal/trash"
)

// newTrashTestServer builds a server over an empty temp config root and also
// returns its trash and store, so tests can stage entries directly.
func newTrashTestServer(t *testing.T) (http.Handler, *trash.Trash, *store.Store, config.Config) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{
		ConfigRoot:  root,
		ProjectsDir: filepath.Join(root, "projects"),
		DataDir:     filepath.Join(root, "ccmanager"),
		TrashDir:    filepath.Join(root, "ccmanager", "trash"),
		DBPath:      filepath.Join(root, "ccmanager", "index.db"),
	}
	if err := cfg.EnsureDataDirs(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tr := trash.New(st, cfg.TrashDir)
	srv, err := New(cfg, st, tr)
	if err != nil {
		t.Fatal(err)
	}
	return srv.Routes(), tr, st, cfg
}

func readBody(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTrashRollBackSwapsBackup(t *testing.T) {
	h, tr, st, cfg := newTrashTestServer(t)
	path := filepath.Join(cfg.ConfigRoot, "CLAUDE.md")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := tr.BackupBeforeWrite(path, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}

	restore := fmt.Sprintf("/trash/%d/restore", id)
	assertContains(t, "trash", do(t, h, "GET", "/trash", nil, false).Body.String(),
		`action="`+restore+`"`, `hx-post="`+restore+`"`, "Roll back",
		`hx-confirm="Replace the current file with this version?`)

	rec := do(t, h, "POST", restore, nil, false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/trash" {
		t.Fatalf("plain roll back = %d to %q: %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if got := readBody(t, path); got != "v1" {
		t.Errorf("after roll back content = %q, want v1", got)
	}

	// The replaced version is restorable, here through htmx.
	entries, err := st.ListTrash()
	if err != nil || len(entries) != 1 {
		t.Fatalf("trash after roll back = %+v err=%v, want the replaced version", entries, err)
	}
	rec = do(t, h, "POST", fmt.Sprintf("/trash/%d/restore", entries[0].ID), nil, true)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/trash" {
		t.Fatalf("htmx roll back = %d, HX-Redirect %q: %s", rec.Code, rec.Header().Get("HX-Redirect"), rec.Body.String())
	}
	if got := readBody(t, path); got != "v2" {
		t.Errorf("after undoing the roll back content = %q, want v2", got)
	}
}

func TestTrashRestoreConflictAndPlainBackupRestore(t *testing.T) {
	h, tr, _, cfg := newTrashTestServer(t)
	notes := filepath.Join(cfg.ConfigRoot, "rules", "notes.md")
	if err := os.MkdirAll(filepath.Dir(notes), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notes, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	deleted, err := tr.MoveToTrash(trash.KindMemoryDelete, notes, "", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notes, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, "POST", fmt.Sprintf("/trash/%d/restore", deleted), nil, false)
	if rec.Code != http.StatusConflict {
		t.Errorf("restore over an existing file = %d, want 409", rec.Code)
	}
	if got := readBody(t, notes); got != "new" {
		t.Errorf("refused restore changed the file to %q", got)
	}

	// A backup whose original is gone offers a plain restore, not a roll back.
	backup, err := tr.BackupBeforeWrite(notes, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(notes); err != nil {
		t.Fatal(err)
	}
	body := do(t, h, "GET", "/trash", nil, false).Body.String()
	if regexp.MustCompile(fmt.Sprintf(`hx-post="/trash/%d/restore"`, backup)).MatchString(body) {
		t.Error("backup of a deleted file is offered as a roll back")
	}
	assertContains(t, "trash", body, fmt.Sprintf(`action="/trash/%d/restore"`, backup), "Restore")
}

func TestSessionDeleteAndRestoreKeepSidecar(t *testing.T) {
	h, cfg := newTestServer(t)
	proj := filepath.Join(cfg.ProjectsDir, "-work")
	transcript := filepath.Join(proj, testSessionID+".jsonl")
	subagent := filepath.Join(proj, testSessionID, "subagents", "agent-a.jsonl")
	if err := os.MkdirAll(filepath.Dir(subagent), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subagent, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := do(t, h, "POST", "/sessions/"+testSessionID+"/delete", url.Values{"confirm": {"1"}}, false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	for _, p := range []string{transcript, subagent} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still in place after delete: %v", p, err)
		}
	}

	m := regexp.MustCompile(`action="(/trash/\d+/restore)"`).FindStringSubmatch(do(t, h, "GET", "/trash", nil, false).Body.String())
	if m == nil {
		t.Fatal("trash page has no restore form")
	}
	if rec := do(t, h, "POST", m[1], nil, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("restore = %d: %s", rec.Code, rec.Body.String())
	}
	if got := readBody(t, subagent); got != "{}\n" {
		t.Errorf("restored subagent transcript = %q", got)
	}
	assertContains(t, "sessions", do(t, h, "GET", "/sessions", nil, false).Body.String(), "a3f9c2e1")
}
