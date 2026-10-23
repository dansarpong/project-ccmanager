package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dansarpong/ccmanager/internal/advisor"
	"github.com/dansarpong/ccmanager/internal/config"
	"github.com/dansarpong/ccmanager/internal/sessions"
	"github.com/dansarpong/ccmanager/internal/store"
	"github.com/dansarpong/ccmanager/internal/trash"
)

const testSessionID = "a3f9c2e1-5b7d-4c1e-9f02-8e6a1d3b7c45"

// newTestServer builds a server over a temp config root holding one indexed
// session, a user CLAUDE.md, a user rule and one project's auto memory.
func newTestServer(t *testing.T) (http.Handler, config.Config) {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	cfg := config.Config{
		ConfigRoot:  root,
		ProjectsDir: filepath.Join(root, "projects"),
		DataDir:     filepath.Join(root, "ccmanager"),
		TrashDir:    filepath.Join(root, "ccmanager", "trash"),
		DBPath:      filepath.Join(root, "ccmanager", "index.db"),
	}
	proj := filepath.Join(cfg.ProjectsDir, "-work")
	transcript := strings.Join([]string{
		`{"type":"user","sessionId":"` + testSessionID + `","cwd":"` + work + `","timestamp":"2026-10-21T14:02:11Z","uuid":"u1","message":{"role":"user","content":"Skip unchanged transcripts in the indexer"}}`,
		`{"type":"assistant","sessionId":"` + testSessionID + `","timestamp":"2026-10-21T14:02:15Z","uuid":"u2","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read"}]}}`,
		`{"type":"assistant","sessionId":"` + testSessionID + `","timestamp":"2026-10-21T14:02:31Z","uuid":"u3","parentUuid":"u2","message":{"role":"assistant","content":[{"type":"text","text":"Unchanged transcripts are now skipped <b>safely</b>."}]}}`,
	}, "\n") + "\n"
	files := map[string]string{
		filepath.Join(proj, testSessionID+".jsonl"):    transcript,
		filepath.Join(proj, "memory", "MEMORY.md"):     "# Memory index\n\n- [Commits](commits.md)\n",
		filepath.Join(proj, "memory", "commits.md"):    "---\nname: commits\ndescription: One summary line\nmetadata:\n  originSessionId: " + testSessionID + "\n---\nKeep it short.\n",
		filepath.Join(root, "CLAUDE.md"):               "# User memory\n\nPrefer small diffs.\n",
		filepath.Join(root, "rules", "safe-writes.md"): "# Safe writes\n\nEvery write is atomic.\n",
		filepath.Join(work, "CLAUDE.md"):               "# Project\n\nRun the tests properly.\n",
	}
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// An old mtime makes the session idle, so status filters are deterministic.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(proj, testSessionID+".jsonl"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.TrashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := sessions.Index(st, cfg); err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, st, trash.New(st, cfg.TrashDir))
	if err != nil {
		t.Fatal(err)
	}
	return srv.Routes(), cfg
}

func do(t *testing.T, h http.Handler, method, target string, form url.Values, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	// httptest defaults Host to example.com, which localOnly rejects.
	req.Host = "127.0.0.1:8765"
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertContains(t *testing.T, name, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("%s: missing %q", name, w)
		}
	}
}

func TestPagesRender(t *testing.T) {
	h, cfg := newTestServer(t)
	work := filepath.Join(cfg.ConfigRoot, "work")
	shell := []string{
		`<meta name="htmx-config" content='{"includeIndicatorStyles":false,"responseHandling":[{"code":"204","swap":false},{"code":"[23]..","swap":true},{"code":"[45]..","swap":true,"error":true}]}'>`,
		`<script src="/static/app.js" defer></script>`,
		`<div class="app">`, `<nav class="sidebar"`, `<main class="main">`,
		`<dialog class="palette" id="palette"`, `id="count-sessions">1</span>`,
	}
	pages := []struct {
		target string
		wants  []string
	}{
		{"/sessions", []string{`class="session-list swap-region" id="session-list"`, `badge badge--idle`,
			`<span class="session-item__id">a3f9c2e1</span>`, `aria-current="page"`, `id="search-results"`}},
		{"/sessions?status=active", []string{"No sessions match these filters"}},
		{"/sessions/" + testSessionID, []string{`class="transcript density-compact"`, `id="m-1"`,
			`transcript-entry--tool`, `claude --resume ` + testSessionID, `hx-confirm="Move this session to the trash?`,
			`&lt;b&gt;safely&lt;/b&gt;`}},
		{"/memory", []string{`class="memory-list swap-region" id="memory-list"`, `class="memory-entry swap-in"`,
			`aria-disabled="true"`, `Open source session`, `<span class="mono">` + work + `</span>`, `data-n="1"`}},
		{"/memory/resolve?dir=" + url.QueryEscape(work), []string{`Load order`, `Static lint`, `id="advisor-results"`,
			`hx-include="#advisor-dir"`}},
		{"/memory/edit?path=" + url.QueryEscape(filepath.Join(cfg.ConfigRoot, "CLAUDE.md")), []string{`form="editor"`, `Prefer small diffs.`}},
		{"/trash", []string{"Trash is empty"}},
	}
	for _, p := range pages {
		rec := do(t, h, "GET", p.target, nil, false)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d: %s", p.target, rec.Code, rec.Body.String())
			continue
		}
		assertContains(t, p.target, rec.Body.String(), append(shell, p.wants...)...)
	}
}

func TestFragments(t *testing.T) {
	h, cfg := newTestServer(t)

	rec := do(t, h, "GET", "/sessions/"+testSessionID+"/transcript?offset=1", nil, true)
	body := strings.TrimSpace(rec.Body.String())
	if !strings.HasPrefix(body, `<li class="transcript-entry`) || strings.Contains(body, "<ol") {
		t.Errorf("transcript page should be bare entries, got: %.120s", body)
	}
	assertContains(t, "transcript page", body, `id="m-2"`, `id="m-3"`)

	rec = do(t, h, "GET", "/search?q=unchanged", nil, true)
	assertContains(t, "search", rec.Body.String(), `id="search-results"`, "<mark>", `href="/sessions/`+testSessionID+`"`)

	rec = do(t, h, "GET", "/palette?q=indexer", nil, true)
	assertContains(t, "palette sessions", rec.Body.String(), `id="opt-s-`+testSessionID+`"`, "<mark>indexer</mark>", "a3f9c2e1")
	if strings.Contains(rec.Body.String(), `class="palette"`) {
		t.Error("palette endpoint should return only the results partial")
	}

	rec = do(t, h, "GET", "/palette?q=safe-writes", nil, true)
	assertContains(t, "palette memory", rec.Body.String(), `<mark>safe-writes</mark>.md`, `/memory?open=`, `palette-item__meta">`+tildePath(filepath.Join(cfg.ConfigRoot, "rules"))+`</span>`)

	rec = do(t, h, "GET", "/palette?q="+url.QueryEscape("<img src=x>"), nil, true)
	if strings.Contains(rec.Body.String(), "<img") {
		t.Errorf("palette query must be escaped: %s", rec.Body.String())
	}
	assertContains(t, "palette empty", rec.Body.String(), "No matches for “&lt;img src=x&gt;”")

	rec = do(t, h, "POST", "/refresh", url.Values{"status": {"idle"}}, true)
	assertContains(t, "refresh", rec.Body.String(), `id="session-list"`, `a3f9c2e1`)
}

func TestHTMXWritesKeepSafetyNet(t *testing.T) {
	h, cfg := newTestServer(t)
	topic := filepath.Join(cfg.ProjectsDir, "-work", "memory", "commits.md")

	// Auto memory stays read-only.
	if rec := do(t, h, "GET", "/memory/edit?path="+url.QueryEscape(topic), nil, false); rec.Code != http.StatusForbidden {
		t.Errorf("editing auto memory = %d, want 403", rec.Code)
	}

	// An htmx delete answers with only out-of-band counts, and the file is
	// recoverable from the trash.
	rec := do(t, h, "POST", "/memory/delete", url.Values{"path": {topic}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("memory delete = %d: %s", rec.Code, rec.Body.String())
	}
	assertContains(t, "memory delete", rec.Body.String(), `id="count-memory" hx-swap-oob="true">4</span>`, `id="count-trash" hx-swap-oob="true">1</span>`)
	if _, err := os.Stat(topic); !os.IsNotExist(err) {
		t.Errorf("deleted file still in place: %v", err)
	}
	assertContains(t, "trash", do(t, h, "GET", "/trash", nil, false).Body.String(), "commits.md", `action="/trash/`)

	// Deleting the most recent session goes through the confirmation page.
	rec = do(t, h, "POST", "/sessions/"+testSessionID+"/delete", url.Values{}, true)
	if got := rec.Header().Get("HX-Redirect"); got != "/sessions/"+testSessionID+"/delete" {
		t.Fatalf("HX-Redirect = %q", got)
	}
	rec = do(t, h, "GET", "/sessions/"+testSessionID+"/delete", nil, false)
	assertContains(t, "confirm", rec.Body.String(), "This is your most recently active session.", `name="confirm" value="1"`)
	rec = do(t, h, "POST", "/sessions/"+testSessionID+"/delete", url.Values{"confirm": {"1"}}, true)
	if got := rec.Header().Get("HX-Redirect"); got != "/sessions" {
		t.Errorf("HX-Redirect after delete = %q, want /sessions", got)
	}
	// A plain form post still gets a 303.
	rec = do(t, h, "POST", "/trash/empty", url.Values{}, false)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("plain trash empty = %d, want 303", rec.Code)
	}
}

func TestRequestGuards(t *testing.T) {
	h, cfg := newTestServer(t)
	userMemory := filepath.Join(cfg.ConfigRoot, "CLAUDE.md")

	cases := []struct {
		name    string
		method  string
		target  string
		host    string
		form    url.Values
		headers map[string]string
		want    int
	}{
		{"rebound host read", "GET", "/sessions", "evil.example:8765", nil, nil, http.StatusForbidden},
		{"localhost read", "GET", "/sessions", "localhost:8765", nil, nil, http.StatusOK},
		{"cross-site delete", "POST", "/memory/delete", "127.0.0.1:8765", url.Values{"path": {userMemory}},
			map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"other local port", "POST", "/trash/empty", "127.0.0.1:8765", url.Values{},
			map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"foreign origin", "POST", "/trash/empty", "127.0.0.1:8765", url.Values{},
			map[string]string{"Origin": "http://evil.example"}, http.StatusForbidden},
		{"same-origin post", "POST", "/trash/empty", "127.0.0.1:8765", url.Values{},
			map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusSeeOther},
		{"non-browser post", "POST", "/trash/empty", "127.0.0.1:8765", url.Values{}, nil, http.StatusSeeOther},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.target, strings.NewReader(c.form.Encode()))
		req.Host = c.host
		if c.form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: %s %s = %d, want %d", c.name, c.method, c.target, rec.Code, c.want)
		}
	}

	if _, err := os.Stat(userMemory); err != nil {
		t.Errorf("refused cross-site delete still removed the file: %v", err)
	}
}

func TestAdvisorPartialRendersEveryBranch(t *testing.T) {
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		data  map[string]any
		wants []string
	}{
		{"unavailable", map[string]any{"Kind": "rewrite", "Dir": "/w", "Unavailable": "claude is unavailable"},
			[]string{"Advisor unavailable", "claude is unavailable"}},
		{"no contradictions", map[string]any{"Kind": "contradiction", "Dir": "/w"}, []string{"No contradictions found"}},
		{"contradictions", map[string]any{"Kind": "contradiction", "Dir": "/w", "Contradictions": []advisor.Contradiction{
			{FileA: "/w/CLAUDE.md", FileB: "/w/rules/a.md", ClaimA: "use tabs", ClaimB: "use spaces", Explanation: "Indentation disagrees"},
		}}, []string{"1 possible contradiction", "Indentation disagrees", "“use tabs”"}},
		{"no edits", map[string]any{"Kind": "rewrite", "Dir": "/w"}, []string{"No rewrites suggested"}},
		{"edits", map[string]any{"Kind": "rewrite", "Dir": "/w <x>", "Edits": []editView{
			{File: "/w/CLAUDE.md", Original: "format properly", Suggestion: "use gofmt", Reason: "Vague", Diff: "  # Go\n- format properly\n+ use gofmt\n"},
		}}, []string{"1 proposed rewrite", `code-block code-block--diff`, `class="code-line code-line--del"`,
			`<span class="code-block__added">+1</span>`, `name="dir" value="/w &lt;x&gt;"`, `action="/advisor/apply"`}},
	}
	for _, c := range cases {
		var b strings.Builder
		if err := tmpl.ExecuteTemplate(&b, "_advisor.html", c.data); err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		assertContains(t, c.name, b.String(), append(c.wants, `id="advisor-results"`)...)
	}
}
