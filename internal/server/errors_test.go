package server

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dansarpong/ccmanager/internal/config"
	"github.com/dansarpong/ccmanager/internal/store"
	"github.com/dansarpong/ccmanager/internal/trash"
)

// newBareServer builds a Server over an empty config root, for driving
// friendlyErrors with stub handlers.
func newBareServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{
		ConfigRoot:  root,
		ProjectsDir: filepath.Join(root, "projects"),
		DataDir:     filepath.Join(root, "ccmanager"),
		TrashDir:    filepath.Join(root, "ccmanager", "trash"),
		DBPath:      filepath.Join(root, "ccmanager", "index.db"),
	}
	if err := os.MkdirAll(cfg.TrashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := New(cfg, st, trash.New(st, cfg.TrashDir))
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func serve(h http.Handler, method, target string, htmx bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = "127.0.0.1:8765"
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMissingSessionRendersErrorPage(t *testing.T) {
	h, _ := newTestServer(t)

	rec := do(t, h, "GET", "/sessions/nope", nil, false)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("page: status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("page: Content-Type = %q", ct)
	}
	assertContains(t, "page", rec.Body.String(),
		`<nav class="sidebar"`, `<main class="main">`, `<div id="flash" class="flash" aria-live="polite"></div>`,
		`<title>Not Found — ccmanager</title>`, `class="empty-state"`, "404 page not found",
		`<a class="btn btn--secondary" href="/sessions">Back to sessions</a>`, `aria-current="page"`)

	rec = do(t, h, "GET", "/sessions/nope", nil, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("htmx: status = %d, want 404", rec.Code)
	}
	for k, want := range map[string]string{"HX-Retarget": "#flash", "HX-Reswap": "innerHTML", "HX-Reselect": ".flash__message"} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("htmx: %s = %q, want %q", k, got, want)
		}
	}
	body := rec.Body.String()
	assertContains(t, "htmx", body, `<dialog class="flash__message" open>`, "<strong>Not Found</strong> 404 page not found", `<form method="dialog">`)
	if strings.Contains(body, "<nav") || strings.Contains(body, "<html") {
		t.Errorf("htmx: want only the flash fragment, got %.200s", body)
	}
}

func TestFriendlyErrorsEscapesMessage(t *testing.T) {
	srv := newBareServer(t)
	h := srv.friendlyErrors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "<script>x</script>", http.StatusBadRequest)
	}))
	for _, htmx := range []bool{false, true} {
		rec := serve(h, "GET", "/memory/edit", htmx)
		body := rec.Body.String()
		if rec.Code != http.StatusBadRequest {
			t.Errorf("htmx=%v: status = %d, want 400", htmx, rec.Code)
		}
		if strings.Contains(body, "<script>x") {
			t.Errorf("htmx=%v: message not escaped: %s", htmx, body)
		}
		assertContains(t, "escaped", body, "&lt;script&gt;x&lt;/script&gt;", "Bad Request")
	}
	assertContains(t, "back link", serve(h, "GET", "/memory/edit", false).Body.String(),
		`href="/memory">Back to memory</a>`)
}

func TestFriendlyErrorsPassesOtherResponsesThrough(t *testing.T) {
	srv := newBareServer(t)
	cases := []struct {
		name    string
		handler http.HandlerFunc
		code    int
		body    string
		header  [2]string
	}{
		{"html 200", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte("<p>ok</p>"))
		}, http.StatusOK, "<p>ok</p>", [2]string{"Content-Type", "text/html; charset=utf-8"}},
		{"htmx redirect", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("HX-Redirect", "/sessions")
		}, http.StatusOK, "", [2]string{"HX-Redirect", "/sessions"}},
		{"see other", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/trash", http.StatusSeeOther)
		}, http.StatusSeeOther, "", [2]string{"Location", "/trash"}},
		{"html 409", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte("<p>conflict</p>"))
		}, http.StatusConflict, "<p>conflict</p>", [2]string{"Content-Type", "text/html; charset=utf-8"}},
		{"plain 200", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write([]byte("just text"))
		}, http.StatusOK, "just text", [2]string{"Content-Type", "text/plain; charset=utf-8"}},
	}
	for _, c := range cases {
		for _, htmx := range []bool{false, true} {
			rec := serve(srv.friendlyErrors(c.handler), "POST", "/memory/save", htmx)
			if rec.Code != c.code || rec.Body.String() != c.body {
				t.Errorf("%s htmx=%v: got %d %q, want %d %q", c.name, htmx, rec.Code, rec.Body.String(), c.code, c.body)
			}
			if got := rec.Header().Get(c.header[0]); got != c.header[1] {
				t.Errorf("%s htmx=%v: %s = %q, want %q", c.name, htmx, c.header[0], got, c.header[1])
			}
			if got := rec.Header().Get("HX-Retarget"); got != "" {
				t.Errorf("%s htmx=%v: unexpected HX-Retarget %q", c.name, htmx, got)
			}
		}
	}
}

func TestFriendlyErrorsHeadHasNoBody(t *testing.T) {
	srv := newBareServer(t)
	rec := serve(srv.friendlyErrors(http.HandlerFunc(http.NotFound)), "HEAD", "/sessions/nope", false)
	if rec.Code != http.StatusNotFound || rec.Body.Len() != 0 {
		t.Errorf("HEAD = %d with %d body bytes, want 404 and none", rec.Code, rec.Body.Len())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("HEAD Content-Type = %q, want the GET page's text/html", ct)
	}
}

func TestGuardRejectionsStayPlainText(t *testing.T) {
	h, _ := newTestServer(t)
	cases := []struct {
		name, method, target, host string
		headers                    map[string]string
	}{
		{"foreign host", "GET", "/sessions", "evil.example:8765", nil},
		{"cross-origin post", "POST", "/trash/empty", "127.0.0.1:8765", map[string]string{"Sec-Fetch-Site": "cross-site"}},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.target, nil)
		req.Host = c.host
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", c.name, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("%s: Content-Type = %q, want text/plain", c.name, ct)
		}
		if strings.Contains(rec.Body.String(), "<") {
			t.Errorf("%s: body should be plain text, got %.120q", c.name, rec.Body.String())
		}
	}
}

func TestFriendlyErrorsFallsBackWhenRenderFails(t *testing.T) {
	srv := newBareServer(t)
	srv.tmpl = template.Must(template.New("").Parse(`{{define "error.html"}}{{template "missing"}}{{end}}`))
	h := srv.friendlyErrors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "file is not editable", http.StatusForbidden)
	}))
	for _, htmx := range []bool{false, true} {
		rec := serve(h, "GET", "/memory/edit", htmx)
		if rec.Code != http.StatusForbidden || rec.Body.String() != "file is not editable\n" {
			t.Errorf("htmx=%v: got %d %q, want the original plain-text 403", htmx, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("htmx=%v: Content-Type = %q, want text/plain", htmx, ct)
		}
		if got := rec.Header().Get("HX-Retarget"); got != "" {
			t.Errorf("htmx=%v: fallback should not retarget, got %q", htmx, got)
		}
		if got, want := rec.Header().Get("HX-Reswap"), map[bool]string{true: "none"}[htmx]; got != want {
			t.Errorf("htmx=%v: HX-Reswap = %q, want %q", htmx, got, want)
		}
	}
}

func TestResponsesForbidFraming(t *testing.T) {
	h, _ := newTestServer(t)
	for _, host := range []string{"127.0.0.1:8765", "evil.example:8765"} {
		req := httptest.NewRequest("GET", "/sessions", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s: X-Frame-Options = %q, want DENY", host, got)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
			t.Errorf("%s: Content-Security-Policy = %q", host, got)
		}
	}
}
