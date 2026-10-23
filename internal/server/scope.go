package server

import (
	"os"
	"path/filepath"
	"strings"
)

// scope is what ccmanager may do to a file through the memory routes.
type scope int

const (
	scopeOutside    scope = iota // not a memory file ccmanager manages: hands off
	scopeManaged                 // managed policy: view only
	scopeAutoMemory              // auto memory, which Claude Code writes: view and delete
	scopeEditable                // CLAUDE.md, CLAUDE.local.md and rules: view, edit and delete
)

func (sc scope) editable() bool  { return sc == scopeEditable }
func (sc scope) deletable() bool { return sc == scopeEditable || sc == scopeAutoMemory }

func (sc scope) editRefusal() string {
	switch sc {
	case scopeAutoMemory:
		return "auto memory is view-and-delete only and cannot be edited"
	case scopeManaged:
		return "managed policy is read-only and cannot be edited"
	}
	return "only CLAUDE.md, CLAUDE.local.md and rules files can be edited"
}

func (sc scope) deleteRefusal() string {
	if sc == scopeManaged {
		return "managed policy is read-only and cannot be deleted"
	}
	return "only CLAUDE.md, CLAUDE.local.md, rules and auto memory files can be moved to the trash"
}

// scopeRoots are the directories that decide a file's scope.
type scopeRoots struct {
	configRoot       string
	projectsDir      string
	managedPolicyDir string
}

func (s *Server) memoryScope(path string) scope {
	return classify(path, scopeRoots{configRoot: s.cfg.ConfigRoot, projectsDir: s.cfg.ProjectsDir, managedPolicyDir: managedPolicyDir})
}

// classify returns path's scope. Only an existing regular file can be in
// scope; anything in the managed-policy directory is view only.
func classify(path string, roots scopeRoots) scope {
	p, err := filepath.Abs(path)
	if err != nil {
		return scopeOutside
	}
	if info, err := os.Stat(p); err != nil || !info.Mode().IsRegular() {
		return scopeOutside
	}
	md := strings.EqualFold(filepath.Ext(p), ".md")
	switch {
	case within(roots.managedPolicyDir, p):
		return scopeManaged
	case isAutoMemoryPath(roots.projectsDir, p):
		if md {
			return scopeAutoMemory
		}
		return scopeOutside
	}
	if base := filepath.Base(p); base == "CLAUDE.md" || base == "CLAUDE.local.md" {
		return scopeEditable
	}
	if md && (within(filepath.Join(roots.configRoot, "rules"), p) || inClaudeRules(p)) {
		return scopeEditable
	}
	return scopeOutside
}

// relInside returns the absolute path p relative to root, if p lies strictly
// inside root.
func relInside(root, p string) (string, bool) {
	if root == "" {
		return "", false
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(abs, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

func within(root, p string) bool {
	_, ok := relInside(root, p)
	return ok
}

// isAutoMemoryPath reports whether p lies inside a project's auto-memory
// directory: <projectsDir>/<project>/memory/….
func isAutoMemoryPath(projectsDir, p string) bool {
	rel, ok := relInside(projectsDir, p)
	parts := strings.Split(filepath.ToSlash(rel), "/")
	return ok && len(parts) >= 3 && parts[1] == "memory"
}

// inClaudeRules reports whether p lies under a .claude/rules/ directory.
func inClaudeRules(p string) bool {
	parts := strings.Split(filepath.ToSlash(p), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == ".claude" && parts[i+1] == "rules" {
			return true
		}
	}
	return false
}
