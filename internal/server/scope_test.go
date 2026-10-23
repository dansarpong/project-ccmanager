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
		dataDir:          filepath.Join(root, "config", "ccmanager"),
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
		{work + "/sub/../CLAUDE.local.md", scopeOutside},
		{work + "/../work/notes.txt", scopeOutside},
		{filepath.Join("missing", "CLAUDE.md"), scopeOutside},
		{filepath.Join(roots.dataDir, "trash", "1", "CLAUDE.md"), scopeOutside},
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
		if got.scope != c.want {
			t.Errorf("classify(%s) = %d, want %d", c.path, got.scope, c.want)
		}
		if got.scope != scopeOutside && got.target != filepath.Clean(c.path) {
			t.Errorf("classify(%s) target = %s, want the file itself", c.path, got.target)
		}
	}

	wantEditable := map[scope]bool{scopeEditable: true}
	wantDeletable := map[scope]bool{scopeEditable: true, scopeAutoMemory: true, scopeLinkOut: true}
	for _, sc := range []scope{scopeOutside, scopeManaged, scopeAutoMemory, scopeEditable, scopeLinkOut} {
		if sc.editable() != wantEditable[sc] || sc.deletable() != wantDeletable[sc] {
			t.Errorf("scope %d: editable=%v deletable=%v", sc, sc.editable(), sc.deletable())
		}
	}
}

func symlink(t *testing.T, target, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClassifySymlinkedParentDirs(t *testing.T) {
	root := t.TempDir()
	roots := scopeRoots{
		configRoot:       filepath.Join(root, "config"),
		projectsDir:      filepath.Join(root, "config", "projects"),
		managedPolicyDir: filepath.Join(root, "managed"),
	}
	mem := filepath.Join(roots.projectsDir, "-p", "memory")
	dotfiles := filepath.Join(root, "dotfiles", "rules")
	writeTestFile(t, filepath.Join(mem, "MEMORY.md"), "# index\n")
	writeTestFile(t, filepath.Join(roots.managedPolicyDir, "policy.md"), "# policy\n")
	writeTestFile(t, filepath.Join(dotfiles, "style.md"), "# style\n")
	symlink(t, mem, filepath.Join(root, "a", ".claude", "rules"))
	symlink(t, roots.managedPolicyDir, filepath.Join(root, "b", ".claude", "rules"))
	symlink(t, dotfiles, filepath.Join(root, "c", ".claude", "rules"))
	// The user's own rules dir may live in dotfiles; a repo's may not lead there.
	writeTestFile(t, filepath.Join(root, "dotfiles", "user-rules", "u.md"), "# user\n")
	symlink(t, filepath.Join(root, "dotfiles", "user-rules"), filepath.Join(roots.configRoot, "rules"))
	writeTestFile(t, filepath.Join(root, "realproj", "CLAUDE.md"), "# project\n")
	symlink(t, filepath.Join(root, "realproj"), filepath.Join(root, "d"))

	cases := []struct {
		path string
		want scope
	}{
		{filepath.Join(root, "a", ".claude", "rules", "MEMORY.md"), scopeAutoMemory},
		{filepath.Join(root, "b", ".claude", "rules", "policy.md"), scopeManaged},
		{filepath.Join(root, "c", ".claude", "rules", "style.md"), scopeLinkOut},
		{filepath.Join(roots.configRoot, "rules", "u.md"), scopeEditable},
		{filepath.Join(root, "d", "CLAUDE.md"), scopeEditable},
	}
	for _, c := range cases {
		if got := classify(c.path, roots).scope; got != c.want {
			t.Errorf("classify(%s) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestClassifySymlinks(t *testing.T) {
	root := t.TempDir()
	roots := scopeRoots{
		configRoot:       filepath.Join(root, "config"),
		projectsDir:      filepath.Join(root, "config", "projects"),
		managedPolicyDir: filepath.Join(root, "managed"),
	}
	work := filepath.Join(root, "work")
	rules := filepath.Join(work, ".claude", "rules")
	agents := filepath.Join(work, "AGENTS.md")
	bashrc := filepath.Join(root, "outside", ".bashrc")
	autoMemory := filepath.Join(roots.projectsDir, "p", "memory", "MEMORY.md")
	policy := filepath.Join(roots.managedPolicyDir, "CLAUDE.md")
	shared := filepath.Join(rules, "shared.md")
	userRule := filepath.Join(roots.configRoot, "rules", "u.md")
	guide := filepath.Join(work, "docs", "guide.md")
	notes := filepath.Join(work, "txt", "notes.txt")
	realAgents := filepath.Join(work, "real", "AGENTS.md")
	for _, p := range []string{agents, bashrc, autoMemory, policy, shared, userRule, guide, notes, realAgents} {
		writeTestFile(t, p, "x\n")
	}
	symlink(t, "AGENTS.md", filepath.Join(work, "real", "CLAUDE.md"))
	symlink(t, "real", filepath.Join(work, "linked"))

	cases := []struct {
		name   string
		path   string
		want   scope
		target string
	}{
		{"CLAUDE.md beside its target", symlink(t, "AGENTS.md", filepath.Join(work, "CLAUDE.md")), scopeEditable, agents},
		{"rules file to another rules file", symlink(t, "shared.md", filepath.Join(rules, "y.md")), scopeEditable, shared},
		{"rules file to a user rule", symlink(t, userRule, filepath.Join(rules, "z.md")), scopeEditable, userRule},
		{"through a symlinked directory", filepath.Join(work, "linked", "CLAUDE.md"), scopeEditable, realAgents},
		{"CLAUDE.md to .bashrc", symlink(t, "../../outside/.bashrc", filepath.Join(work, "bash", "CLAUDE.md")), scopeLinkOut, bashrc},
		{"rules file to auto memory", symlink(t, autoMemory, filepath.Join(rules, "x.md")), scopeLinkOut, autoMemory},
		{"CLAUDE.md to managed policy", symlink(t, policy, filepath.Join(work, "policy", "CLAUDE.md")), scopeLinkOut, policy},
		{"CLAUDE.md to a .md elsewhere", symlink(t, "../docs/guide.md", filepath.Join(work, "other", "CLAUDE.md")), scopeLinkOut, guide},
		{"CLAUDE.md to a non-.md beside it", symlink(t, "notes.txt", filepath.Join(work, "txt", "CLAUDE.md")), scopeLinkOut, notes},
		{"dangling", symlink(t, "missing.md", filepath.Join(work, "gone", "CLAUDE.md")), scopeOutside, ""},
		{"to a directory", symlink(t, "../docs", filepath.Join(work, "dir", "CLAUDE.md")), scopeOutside, ""},
		{"named out of scope", symlink(t, "AGENTS.md", filepath.Join(work, "README.md")), scopeOutside, ""},
	}
	for _, c := range cases {
		got := classify(c.path, roots)
		if got.scope != c.want || got.target != c.target {
			t.Errorf("%s: classify = %d %q, want %d %q", c.name, got.scope, got.target, c.want, c.target)
		}
		if got.scope == scopeLinkOut {
			if !got.deletable() {
				t.Errorf("%s: the link itself should be deletable", c.name)
			}
			assertContains(t, c.name+" refusal", got.editRefusal(), "resolves to "+c.target, "outside what ccmanager edits")
		}
	}

	// A symlinked config dir must not hide auto memory behind its real path.
	real := filepath.Join(root, "dotfiles")
	hidden := filepath.Join(real, "projects", "p", "memory", "CLAUDE.md")
	realRule := filepath.Join(real, "rules", "v.md")
	writeTestFile(t, hidden, "x\n")
	writeTestFile(t, realRule, "x\n")
	linkedRoots := scopeRoots{configRoot: symlink(t, real, filepath.Join(root, "dotclaude"))}
	linkedRoots.projectsDir = filepath.Join(linkedRoots.configRoot, "projects")
	toHidden := symlink(t, filepath.Join(linkedRoots.projectsDir, "p", "memory", "CLAUDE.md"), filepath.Join(work, "hidden", "CLAUDE.md"))
	if got := classify(toHidden, linkedRoots); got.scope != scopeLinkOut || got.target != hidden {
		t.Errorf("link into auto memory under a symlinked config dir = %d %q, want %d %q", got.scope, got.target, scopeLinkOut, hidden)
	}
	toRule := symlink(t, filepath.Join(linkedRoots.configRoot, "rules", "v.md"), filepath.Join(rules, "v.md"))
	if got := classify(toRule, linkedRoots); got.scope != scopeEditable || got.target != realRule {
		t.Errorf("link to a user rule under a symlinked config dir = %d %q, want %d %q", got.scope, got.target, scopeEditable, realRule)
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
		{symlink(t, managed, filepath.Join(root, "work", "a", "CLAUDE.md")), scopeLinkOut, scopeEditable},
		{symlink(t, autoMemory, filepath.Join(root, "work", "b", "CLAUDE.md")), scopeLinkOut, scopeEditable},
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
			if got := classify(c.path, roots).scope; got != want {
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
