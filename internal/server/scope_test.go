package server

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dansarpong/ccmanager/internal/memory"
	"github.com/dansarpong/ccmanager/internal/safeio"
)

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestClassify(t *testing.T) {
	root := t.TempDir()
	roots := scopeRoots{
		configRoot:       filepath.Join(root, "config"),
		projectsDir:      filepath.Join(root, "config", "projects"),
		managedPolicyDir: filepath.Join(root, "managed"),
	}
	work := filepath.Join(root, "work")
	mem := filepath.Join(roots.projectsDir, "-work", "memory")
	cases := []struct {
		path string
		want scope
	}{
		{filepath.Join(work, "notes.txt"), scopeOutside},
		{filepath.Join(work, ".bashrc"), scopeOutside},
		{filepath.Join(work, "README.md"), scopeOutside},
		{filepath.Join(work, "rules", "x.md"), scopeOutside},
		{filepath.Join(work, "missing", "CLAUDE.md"), scopeOutside},
		{filepath.Join(roots.configRoot, "rules", "notes.txt"), scopeOutside},
		{filepath.Join(roots.projectsDir, "-work", "session.jsonl"), scopeOutside},
		{filepath.Join(mem, "notes.txt"), scopeOutside},
		{filepath.Join(roots.managedPolicyDir, "CLAUDE.md"), scopeManaged},
		{filepath.Join(roots.managedPolicyDir, "CLAUDE.local.md"), scopeManaged},
		{filepath.Join(mem, "MEMORY.md"), scopeAutoMemory},
		{filepath.Join(mem, "topic.md"), scopeAutoMemory},
		{filepath.Join(mem, "CLAUDE.md"), scopeAutoMemory},
		{filepath.Join(roots.configRoot, "CLAUDE.md"), scopeEditable},
		{filepath.Join(roots.configRoot, "rules", "userrule.md"), scopeEditable},
		{filepath.Join(roots.configRoot, "rules", "sub", "nested.md"), scopeEditable},
		{filepath.Join(work, "CLAUDE.local.md"), scopeEditable},
		{filepath.Join(work, "sub", "CLAUDE.md"), scopeEditable},
		{filepath.Join(work, ".claude", "CLAUDE.md"), scopeEditable},
		{filepath.Join(work, ".claude", "rules", "sub", "x.md"), scopeEditable},
		{work + "/sub/../CLAUDE.local.md", scopeEditable},
		{work + "/../work/notes.txt", scopeOutside},
		{filepath.Join(work, "dir", "CLAUDE.md"), scopeOutside},
	}
	// A directory named like a memory file is never in scope.
	if err := os.MkdirAll(filepath.Join(work, "dir", "CLAUDE.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if _, err := os.Stat(c.path); os.IsNotExist(err) && !strings.Contains(c.path, "missing") {
			writeTestFile(t, c.path, "x\n")
		}
	}

	for _, c := range cases {
		got := classify(c.path, roots)
		if got != c.want {
			t.Errorf("classify(%s) = %d, want %d", c.path, got, c.want)
		}
	}

	wantEditable := map[scope]bool{scopeEditable: true}
	wantDeletable := map[scope]bool{scopeEditable: true, scopeAutoMemory: true}
	for _, sc := range []scope{scopeOutside, scopeManaged, scopeAutoMemory, scopeEditable} {
		if sc.editable() != wantEditable[sc] || sc.deletable() != wantDeletable[sc] {
			t.Errorf("scope %d: editable=%v deletable=%v", sc, sc.editable(), sc.deletable())
		}
	}
}

func TestClassifyFoldsCaseOnDarwinAndWindows(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, "managed", "CLAUDE.md")
	autoMemory := filepath.Join(root, "config", "projects", "-work", "Memory", "CLAUDE.md")
	rule := filepath.Join(root, "work", ".Claude", "Rules", "x.md")
	userRule := filepath.Join(root, "config", "rules", "u.md")
	for _, p := range []string{managed, autoMemory, rule, userRule} {
		writeTestFile(t, p, "x\n")
	}
	cases := []struct {
		path          string
		folded, exact scope
	}{
		{managed, scopeManaged, scopeEditable},
		{autoMemory, scopeAutoMemory, scopeEditable},
		{rule, scopeEditable, scopeOutside},
		{userRule, scopeEditable, scopeOutside},
	}
	for _, goos := range []string{"darwin", "windows", "linux"} {
		roots := scopeRoots{
			configRoot:       filepath.Join(root, "CONFIG"),
			projectsDir:      filepath.Join(root, "Config", "Projects"),
			managedPolicyDir: filepath.Join(root, "Managed"),
			goos:             goos,
		}
		for _, c := range cases {
			want := c.folded
			if goos == "linux" {
				want = c.exact
			}
			if got := classify(c.path, roots); got != want {
				t.Errorf("%s: classify(%s) = %d, want %d", goos, c.path, got, want)
			}
		}
	}

	darwin := scopeRoots{managedPolicyDir: memory.ManagedDir("darwin"), goos: "darwin"}
	if got := darwin.pathScope("/library/application support/claudecode/CLAUDE.md"); got != scopeManaged {
		t.Errorf("darwin managed policy in lower case = %d, want %d", got, scopeManaged)
	}
}

func TestMemoryRoutesRefuseFilesOutsideScope(t *testing.T) {
	h, cfg := newTestServer(t)
	notes := filepath.Join(cfg.ConfigRoot, "work", "notes.txt")
	writeTestFile(t, notes, "not memory\n")
	snap, err := safeio.Snapshot(notes)
	if err != nil {
		t.Fatal(err)
	}
	token := fmt.Sprintf("%d:%d", snap.Mtime, snap.Size)

	rec := do(t, h, "GET", "/memory/edit?path="+url.QueryEscape(notes), nil, false)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "not memory") {
		t.Errorf("edit = %d %q, want 403 without the contents", rec.Code, rec.Body.String())
	}
	assertContains(t, "edit refusal", rec.Body.String(), "only CLAUDE.md, CLAUDE.local.md and rules files can be edited")

	rec = do(t, h, "POST", "/memory/save", url.Values{"path": {notes}, "content": {"overwritten"}, "token": {token}}, false)
	if rec.Code != http.StatusForbidden {
		t.Errorf("save = %d, want 403", rec.Code)
	}
	rec = do(t, h, "POST", "/advisor/apply", url.Values{"file": {notes}, "original": {"not memory"}, "suggestion": {"overwritten"}}, false)
	if rec.Code != http.StatusForbidden {
		t.Errorf("advisor apply = %d, want 403", rec.Code)
	}
	rec = do(t, h, "POST", "/memory/delete", url.Values{"path": {notes}}, false)
	if rec.Code != http.StatusForbidden {
		t.Errorf("delete = %d, want 403", rec.Code)
	}
	assertContains(t, "delete refusal", rec.Body.String(), "can be moved to the trash")

	if data, err := os.ReadFile(notes); err != nil || string(data) != "not memory\n" {
		t.Errorf("refused writes changed the file: %q, %v", data, err)
	}
	assertContains(t, "trash", do(t, h, "GET", "/trash", nil, false).Body.String(), "Trash is empty")

	// A project CLAUDE.md is in scope for both.
	project := filepath.Join(cfg.ConfigRoot, "work", "CLAUDE.md")
	if rec := do(t, h, "GET", "/memory/edit?path="+url.QueryEscape(project), nil, false); rec.Code != http.StatusOK {
		t.Errorf("editing a project CLAUDE.md = %d, want 200", rec.Code)
	}
	if rec := do(t, h, "POST", "/memory/delete", url.Values{"path": {project}}, false); rec.Code != http.StatusSeeOther {
		t.Errorf("deleting a project CLAUDE.md = %d, want 303", rec.Code)
	}
}

func TestMissingMemoryFileIsNotFound(t *testing.T) {
	h, cfg := newTestServer(t)
	gone := filepath.Join(cfg.ConfigRoot, "work", "sub", "CLAUDE.md")
	cases := []struct {
		method, target string
		form           url.Values
	}{
		{"GET", "/memory/edit?path=" + url.QueryEscape(gone), nil},
		{"POST", "/memory/save", url.Values{"path": {gone}, "content": {"x"}, "token": {"1:1"}}},
		{"POST", "/memory/delete", url.Values{"path": {gone}}},
		{"POST", "/advisor/apply", url.Values{"file": {gone}, "original": {"a"}, "suggestion": {"b"}}},
	}
	for _, c := range cases {
		if rec := do(t, h, c.method, c.target, c.form, false); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", c.method, c.target, rec.Code)
		}
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("a refused save created %s: %v", gone, err)
	}
}

func TestResolveOffersEditOnlyForEditableSources(t *testing.T) {
	h, cfg := newTestServer(t)
	dir := t.TempDir()
	defer func(prev string) { managedPolicyDir = prev }(managedPolicyDir)
	managedPolicyDir = dir
	policy := filepath.Join(dir, "CLAUDE.md")
	writeTestFile(t, policy, "# Managed policy\n")

	work := filepath.Join(cfg.ConfigRoot, "work")
	body := do(t, h, "GET", "/memory/resolve?dir="+url.QueryEscape(work), nil, false).Body.String()
	// html/template percent-encodes the query value with lowercase hex.
	editHref := func(p string) string {
		return `href="/memory/edit?path=` + strings.ReplaceAll(url.QueryEscape(p), "%2F", "%2f")
	}
	assertContains(t, "resolve", body, tildePath(policy), editHref(filepath.Join(work, "CLAUDE.md")))
	if strings.Contains(body, editHref(policy)) {
		t.Error("resolve view offers to edit the managed policy")
	}
}

func TestManagedPolicyIsViewOnly(t *testing.T) {
	h, _ := newTestServer(t)
	dir := t.TempDir()
	defer func(prev string) { managedPolicyDir = prev }(managedPolicyDir)
	managedPolicyDir = dir
	policy := filepath.Join(dir, "CLAUDE.md")
	writeTestFile(t, policy, "# Managed policy\n")

	if rec := do(t, h, "GET", "/memory/edit?path="+url.QueryEscape(policy), nil, false); rec.Code != http.StatusForbidden {
		t.Errorf("editing managed policy = %d, want 403", rec.Code)
	}
	rec := do(t, h, "POST", "/memory/delete", url.Values{"path": {policy}}, false)
	if rec.Code != http.StatusForbidden {
		t.Errorf("deleting managed policy = %d, want 403", rec.Code)
	}
	assertContains(t, "managed refusal", rec.Body.String(), "managed policy is read-only")
	if _, err := os.Stat(policy); err != nil {
		t.Errorf("managed policy was removed: %v", err)
	}
}
