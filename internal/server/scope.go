package server

import (
	"errors"
	"io/fs"
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
	scopeLinkOut                 // a memory file symlinked out of scope: delete the link only
)

func (sc scope) editable() bool { return sc == scopeEditable }
func (sc scope) deletable() bool {
	return sc == scopeEditable || sc == scopeAutoMemory || sc == scopeLinkOut
}

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

// scopedFile is a path's scope and its target, the regular file that reading
// or writing the path touches: the path itself, or a symlink's resolved target.
type scopedFile struct {
	scope
	target string
}

func (f scopedFile) editRefusal() string {
	if f.scope == scopeLinkOut {
		return "this file resolves to " + f.target + ", outside what ccmanager edits"
	}
	return f.scope.editRefusal()
}

// scopeRoots are the directories that decide a file's scope, and the OS, which
// decides whether comparing paths ignores case.
type scopeRoots struct {
	configRoot       string
	projectsDir      string
	managedPolicyDir string
	dataDir          string // ccmanager's own state, trash included: never in scope
	userRules        string // the user rules dir; "" means <configRoot>/rules
	goos             string
}

func (s *Server) scopeRoots() scopeRoots {
	return scopeRoots{configRoot: s.cfg.ConfigRoot, projectsDir: s.cfg.ProjectsDir, managedPolicyDir: managedPolicyDir, dataDir: s.cfg.DataDir, goos: runtime.GOOS}
}

func (s *Server) memoryScope(path string) scopedFile {
	return classify(path, s.scopeRoots())
}

// restorable reports whether a memory file trashed from path, which no longer
// exists, may be put back there: path must still be in scope where its nearest
// existing parent really leads, so a directory since swapped for a symlink
// can't redirect the restore.
func (s *Server) restorable(path string) bool {
	if !canonical(path) {
		return false
	}
	r := s.scopeRoots()
	sc := r.pathScope(path)
	if sc != scopeEditable && sc != scopeAutoMemory {
		return false
	}
	real, err := resolveMissing(path)
	if err != nil {
		return false
	}
	if real != path {
		sc = r.realScope(real, sc)
	}
	return sc == scopeEditable || sc == scopeAutoMemory
}

// canonical reports whether path is absolute and already clean. Cleaning ".."
// as text can disagree with the OS, which resolves it after following links.
func canonical(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

// resolveMissing resolves symlinks in the nearest existing ancestor of path
// and joins the rest, for a path that doesn't exist yet.
func resolveMissing(path string) (string, error) {
	dir, rest := path, ""
	for {
		real, err := filepath.EvalSymlinks(dir)
		if err == nil {
			return filepath.Join(real, rest), nil
		}
		parent := filepath.Dir(dir)
		if !errors.Is(err, fs.ErrNotExist) || parent == dir {
			return "", err
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// classify returns path's scope and target. Only a regular file, or a symlink
// to one, can be in scope; anything in the managed-policy directory is view
// only. A symlink named as a memory file is editable only when its target is
// in scope on its own or is a .md file beside it (CLAUDE.md -> AGENTS.md);
// otherwise just the link can be deleted.
func classify(path string, roots scopeRoots) scopedFile {
	if !canonical(path) {
		return scopedFile{}
	}
	p := path
	info, err := os.Lstat(p)
	if err != nil {
		return scopedFile{}
	}
	sc := roots.pathScope(p)
	if info.Mode().IsRegular() {
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			return scopedFile{}
		}
		if real != p {
			sc = roots.realScope(real, sc)
		}
		return scopedFile{scope: sc, target: real}
	}
	if sc == scopeOutside || info.Mode()&os.ModeSymlink == 0 {
		return scopedFile{}
	}
	target, err := filepath.EvalSymlinks(p)
	if err != nil {
		return scopedFile{}
	}
	if ti, err := os.Lstat(target); err != nil || !ti.Mode().IsRegular() {
		return scopedFile{}
	}
	if sc == scopeEditable && !roots.linkTargetEditable(p, target) {
		sc = scopeLinkOut
	}
	return scopedFile{scope: sc, target: target}
}

// linkTargetEditable reports whether target, the resolved target of the
// symlink link, may be edited through it. The roots are checked as given and
// resolved, so a symlinked config dir can't hide auto memory.
func (r scopeRoots) linkTargetEditable(link, target string) bool {
	inScope := false
	for _, rr := range []scopeRoots{r, r.resolved()} {
		switch rr.pathScope(target) {
		case scopeManaged, scopeAutoMemory:
			return false
		case scopeEditable:
			inScope = true
		}
	}
	if inScope {
		return true
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(link))
	return err == nil && dir == filepath.Dir(target) && strings.EqualFold(filepath.Ext(target), ".md")
}

// realScope is sc for a file whose real location, real, differs because a
// parent directory is a symlink. Managed policy and auto memory keep their
// scope wherever they're reached from, and an editable file stays editable
// only if it really is a memory file too; otherwise the write would land out
// of scope, so it can only be deleted.
func (r scopeRoots) realScope(real string, sc scope) scope {
	editable := false
	for _, rr := range []scopeRoots{r, r.resolved()} {
		switch s := rr.pathScope(real); s {
		case scopeManaged, scopeAutoMemory:
			return s
		case scopeEditable:
			editable = true
		}
	}
	if sc == scopeEditable && !editable {
		return scopeLinkOut
	}
	return sc
}

// resolved returns r with symlinks in its directories resolved, where they
// exist.
func (r scopeRoots) resolved() scopeRoots {
	r.userRules = r.rulesDir()
	for _, dir := range []*string{&r.configRoot, &r.projectsDir, &r.managedPolicyDir, &r.dataDir, &r.userRules} {
		if *dir == "" {
			continue // EvalSymlinks("") is ".", which would put the working directory in scope
		}
		if p, err := filepath.EvalSymlinks(*dir); err == nil {
			*dir = p
		}
	}
	return r
}

// pathScope applies the name and directory rules to the absolute path p,
// without looking at the filesystem.
func (r scopeRoots) pathScope(p string) scope {
	md := strings.EqualFold(filepath.Ext(p), ".md")
	switch {
	case r.within(r.dataDir, p):
		return scopeOutside
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
	if md && (r.within(r.rulesDir(), p) || r.inClaudeRules(p)) {
		return scopeEditable
	}
	return scopeOutside
}

func (r scopeRoots) rulesDir() string {
	if r.userRules != "" {
		return r.userRules
	}
	if r.configRoot == "" {
		return ""
	}
	return filepath.Join(r.configRoot, "rules")
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
