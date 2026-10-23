package server

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var tokenField = regexp.MustCompile(`name="token" value="([^"]+)"`)

func TestSaveDetectsConflictAndBacksUp(t *testing.T) {
	h, cfg := newTestServer(t)
	path := filepath.Join(cfg.ConfigRoot, "CLAUDE.md")

	m := tokenField.FindStringSubmatch(do(t, h, "GET", "/memory/edit?path="+url.QueryEscape(path), nil, false).Body.String())
	if m == nil {
		t.Fatal("editor has no change token")
	}
	if err := os.WriteFile(path, []byte("changed on disk\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := do(t, h, "POST", "/memory/save", url.Values{"path": {path}, "content": {"mine\r\n"}, "token": {m[1]}}, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("stale save = %d, want the 200 conflict page", rec.Code)
	}
	assertContains(t, "conflict", rec.Body.String(), "File changed on disk", `name="force" value="1"`)
	if got := readBody(t, path); got != "changed on disk\n" {
		t.Fatalf("a conflicting save wrote the file: %q", got)
	}

	m = tokenField.FindStringSubmatch(rec.Body.String())
	rec = do(t, h, "POST", "/memory/save", url.Values{"path": {path}, "content": {"mine\r\n"}, "token": {m[1]}, "force": {"1"}}, false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("forced save = %d: %s", rec.Code, rec.Body.String())
	}
	if got := readBody(t, path); got != "mine\n" {
		t.Errorf("after the forced save content = %q, want CRLF normalized", got)
	}
	assertContains(t, "trash", do(t, h, "GET", "/trash", nil, false).Body.String(), "Backup", "Roll back")
}

func TestSaveRejectsBadToken(t *testing.T) {
	h, cfg := newTestServer(t)
	path := filepath.Join(cfg.ConfigRoot, "CLAUDE.md")
	for _, token := range []string{"", "nope", "1:x"} {
		rec := do(t, h, "POST", "/memory/save", url.Values{"path": {path}, "content": {"x"}, "token": {token}}, false)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("token %q = %d, want 400", token, rec.Code)
		}
	}
	if got := readBody(t, path); got != "# User memory\n\nPrefer small diffs.\n" {
		t.Errorf("a rejected save changed the file: %q", got)
	}
}
