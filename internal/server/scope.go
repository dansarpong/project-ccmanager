package server

import (
	"os"
	"path/filepath"
	"runtime"
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

// scopeRoots are the directories that decide a file's scope, and the OS, which
// decides whether comparing paths ignores case.
type scopeRoots struct {
	configRoot       string
	projectsDir      string
	managedPolicyDir string
	goos             string
}

func (s *Server) memoryScope(path string) scope {
	return classify(path, scopeRoots{configRoot: s.cfg.ConfigRoot, projectsDir: s.cfg.ProjectsDir, managedPolicyDir: managedPolicyDir, goos: runtime.GOOS})
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
	return roots.pathScope(p)
}

// pathScope applies the name and directory rules to the absolute path p,
// without looking at the filesystem.
func (r scopeRoots) pathScope(p string) scope {
	md := strings.EqualFold(filepath.Ext(p), ".md")
	switch {
	case r.within(r.managedPolicyDir, p):
		return scopeManaged
	case r.isAutoMemoryPath(p):
		if md {
			return scopeAutoMemory
		}
		return scopeOutside
	}
	if base := filepath.Base(p); base == "CLAUDE.md" || base == "CLAUDE.local.md" {
		return scopeEditable
	}
	if md && (r.within(filepath.Join(r.configRoot, "rules"), p) || r.inClaudeRules(p)) {
		return scopeEditable
	}
	return scopeOutside
}

// fold lower-cases p on darwin and windows, whose filesystems ignore case by
// default, so /library/… is still the managed-policy directory there.
func (r scopeRoots) fold(p string) string {
	if r.goos == "darwin" || r.goos == "windows" {
		return strings.ToLower(p)
	}
	return p
}

// relInside returns the absolute path p relative to root, if p lies strictly
// inside root. The result is case-folded where the filesystem ignores case.
func (r scopeRoots) relInside(root, p string) (string, bool) {
	if root == "" {
		return "", false
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(r.fold(abs), r.fold(p))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

func (r scopeRoots) within(root, p string) bool {
	_, ok := r.relInside(root, p)
	return ok
}

// isAutoMemoryPath reports whether p lies inside a project's auto-memory
// directory: <projectsDir>/<project>/memory/….
func (r scopeRoots) isAutoMemoryPath(p string) bool {
	rel, ok := r.relInside(r.projectsDir, p)
	parts := strings.Split(filepath.ToSlash(rel), "/")
	return ok && len(parts) >= 3 && parts[1] == "memory"
}

// inClaudeRules reports whether p lies under a .claude/rules/ directory.
func (r scopeRoots) inClaudeRules(p string) bool {
	parts := strings.Split(filepath.ToSlash(r.fold(p)), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == ".claude" && parts[i+1] == "rules" {
			return true
		}
	}
	return false
}
