package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInventoryIncludesProjectFiles(t *testing.T) {
	h, cfg := newTestServer(t)
	work := filepath.Join(cfg.ConfigRoot, "work")
	writeTestFile(t, filepath.Join(work, "CLAUDE.local.md"), "# Local\n\nMy own notes.\n")
	writeTestFile(t, filepath.Join(work, ".claude", "rules", "style.md"), "# Style\n\nKeep it tidy.\n")

	body := do(t, h, "GET", "/memory?kind=project", nil, false).Body.String()
	assertContains(t, "project filter", body,
		"Project CLAUDE.md", "Run the tests properly.",
		"Project CLAUDE.local.md", "My own notes.",
		"Project rule", "Keep it tidy.",
		`<span class="mono">`+tildePath(work)+`</span>`)
	if strings.Contains(body, "Prefer small diffs.") || strings.Contains(body, "Memory index") {
		t.Error("the project filter let user or auto-memory files through")
	}
	if got := strings.Count(body, `href="/memory/edit?path=`); got != 3 {
		t.Errorf("%d edit links, want one per project file", got)
	}

	body = do(t, h, "GET", "/memory?kind=project-rule", nil, false).Body.String()
	if !strings.Contains(body, "Keep it tidy.") || strings.Contains(body, "Run the tests properly.") {
		t.Error("the project-rule filter should show only project rules")
	}
	assertContains(t, "all kinds", do(t, h, "GET", "/memory", nil, false).Body.String(),
		"7 files across your projects", `id="count-memory">7</span>`)
}

func TestInventorySkipsProjectsThatAreGone(t *testing.T) {
	h, cfg := newTestServer(t)
	// A project rule in an ancestor of the indexed project loads for it.
	ancestor := filepath.Join(cfg.ConfigRoot, ".claude", "rules", "ancestor.md")
	writeTestFile(t, ancestor, "# Ancestor rule\n")
	assertContains(t, "with project", do(t, h, "GET", "/memory?kind=project-rule", nil, false).Body.String(), "Ancestor rule")

	if err := os.RemoveAll(filepath.Join(cfg.ConfigRoot, "work")); err != nil {
		t.Fatal(err)
	}
	if body := do(t, h, "GET", "/memory?kind=project", nil, false).Body.String(); strings.Contains(body, "Ancestor rule") {
		t.Error("a project whose directory is gone still contributed files")
	}
}

func TestInventoryManagedPolicyIsViewOnly(t *testing.T) {
	h, _ := newTestServer(t)
	defer func(prev string) { managedPolicyDir = prev }(managedPolicyDir)
	managedPolicyDir = t.TempDir()
	writeTestFile(t, filepath.Join(managedPolicyDir, "CLAUDE.md"), "# Org policy\n")

	body := do(t, h, "GET", "/memory?kind=managed", nil, false).Body.String()
	assertContains(t, "managed", body, "Org policy", "Managed policy",
		`aria-label="Edit — Managed policy is read-only"`, `aria-label="Move to trash — Managed policy is read-only"`)
	if strings.Contains(body, `action="/memory/delete"`) || strings.Contains(body, `href="/memory/edit`) {
		t.Error("managed policy should offer neither edit nor delete")
	}
}

func TestMatchesInventoryKindProjectGroup(t *testing.T) {
	cases := []struct {
		kind, filter string
		want         bool
	}{
		{"project", "project", true},
		{"project-local", "project", true},
		{"project-rule", "project", true},
		{"project-rule", "project-rule", true},
		{"project", "project-rule", false},
		{"user-rule", "project", false},
		{"managed", "managed", true},
		{"managed", "project", false},
	}
	for _, c := range cases {
		if got := matchesInventoryKind(c.kind, c.filter); got != c.want {
			t.Errorf("matchesInventoryKind(%q, %q) = %v, want %v", c.kind, c.filter, got, c.want)
		}
	}
}
