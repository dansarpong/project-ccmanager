package memory

import (
	"os"
	"path/filepath"
	"strings"
)

// repo is where a working directory sits in git.
type repo struct {
	root  string // nearest ancestor with a .git entry; the working dir outside git
	main  string // the main checkout's root, shared by all of a repo's worktrees
	inGit bool
}

// findRepo walks up from workdir to the nearest .git entry. Outside a git repo
// the working directory itself is the root (matching Claude Code's "outside a
// git repo, the project root is used" behaviour). In a linked worktree, root
// is the worktree and main is the checkout it was added from.
func findRepo(workdir string) repo {
	cur := workdir
	for {
		git := filepath.Join(cur, ".git")
		if info, err := os.Stat(git); err == nil {
			r := repo{root: cur, main: cur, inGit: true}
			if !info.IsDir() {
				if main := mainCheckout(cur, git); main != "" {
					r.main = main
				}
			}
			return r
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return repo{root: workdir, main: workdir}
		}
		cur = parent
	}
}

// mainCheckout follows a worktree's .git file ("gitdir: <path>") and that
// gitdir's commondir to the repository's .git directory, returning the checkout
// that holds it. It returns "" for a .git file without a commondir (a
// submodule) or a bare common dir, which keep their own root.
func mainCheckout(root, gitFile string) string {
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(string(data), "\n")
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(first), "gitdir:")
	if !ok {
		return ""
	}
	gitdir = resolveFrom(root, strings.TrimSpace(gitdir))
	common, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return ""
	}
	dir := resolveFrom(gitdir, strings.TrimSpace(string(common)))
	if filepath.Base(dir) != ".git" {
		return ""
	}
	return filepath.Dir(dir)
}

// resolveFrom resolves p against base unless it is already absolute.
func resolveFrom(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}

// isExternal reports whether target lies outside the project subtree rooted at
// projectRoot. Such imports are approval-gated by Claude Code.
func isExternal(projectRoot, target string) bool {
	rel, err := filepath.Rel(projectRoot, target)
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// matchesAny reports whether absPath matches any of the glob patterns. Patterns
// are matched against the absolute path with filepath.Match per path segment;
// a "**" segment matches across directory boundaries.
func matchesAny(absPath string, patterns []string) bool {
	for _, p := range patterns {
		if globMatch(p, absPath) {
			return true
		}
	}
	return false
}

// globMatch supports "**" (any number of path segments) in addition to the
// single-segment wildcards filepath.Match handles. It is used for both
// claudeMdExcludes and rule paths: matching.
func globMatch(pattern, path string) bool {
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)
	return matchSegments(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

func matchSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// Collapse consecutive ** and match the remainder against any suffix.
			rest := pat[1:]
			if len(rest) == 0 {
				return true
			}
			for i := 0; i <= len(name); i++ {
				if matchSegments(rest, name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		ok, err := filepath.Match(pat[0], name[0])
		if err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}
