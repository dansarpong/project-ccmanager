package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Input is the pure engine's configuration. It is deliberately minimal (and
// independent of internal/config) so Resolve stays easy to test with a fake
// config root and a managed-policy path pointed at a fixture.
type Input struct {
	ConfigRoot  string // ~/.claude or $CLAUDE_CONFIG_DIR
	ProjectsDir string // ConfigRoot/projects (for locating auto-memory)
	ManagedPath string // managed-policy CLAUDE.md path; "" disables (default /etc/claude-code/CLAUDE.md on Linux)
}

// Resolve assembles the memory stack for workdir. It never returns an error
// for a missing or unreadable individual file (those are recorded on the
// Source); it only errors if workdir itself cannot be made absolute.
func Resolve(in Input, workdir string) (*ResolvedStack, error) {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return nil, fmt.Errorf("resolving workdir %q: %w", workdir, err)
	}

	stack := &ResolvedStack{Workdir: abs, ConfigRoot: in.ConfigRoot}

	excludes := loadExcludes(in.ConfigRoot, abs)
	r := findRepo(abs)

	var sources []Source
	for i, c := range collectCandidates(in, abs) {
		s := readSource(c, excludes)
		s.Order = i
		sources = append(sources, s)
	}

	// Expand imports and strip comments per source, then collect graph edges.
	for i := range sources {
		expandSource(&sources[i], r.root)
		for _, imp := range sources[i].Imports {
			to := imp.ResolvedPath
			if to == "" {
				to = imp.Spec
			}
			stack.ImportEdges = append(stack.ImportEdges, ImportEdge{
				From: sources[i].Path, To: to,
				External: imp.External, Circular: imp.Circular, Missing: imp.Missing,
			})
		}
	}
	stack.Sources = sources

	stack.AutoMemory = resolveAutoMemory(in, abs, r.main)
	return stack, nil
}

// candidate is a file we intend to load, with its layer, before reading it.
type candidate struct {
	path  string
	layer string
}

// collectCandidates lists every memory file to load, in Claude Code's load
// order: broadest scope first (managed → user → user rules), then the
// filesystem walk from root down to the working dir, where deeper directories
// are read after shallower ones, and within a directory CLAUDE.local.md comes
// after CLAUDE.md. User rules always precede project rules.
func collectCandidates(in Input, workdir string) []candidate {
	var out []candidate

	if in.ManagedPath != "" {
		out = append(out, candidate{in.ManagedPath, LayerManaged})
	}

	if in.ConfigRoot != "" {
		out = append(out, candidate{filepath.Join(in.ConfigRoot, "CLAUDE.md"), LayerUser})
		for _, r := range globRules(filepath.Join(in.ConfigRoot, "rules")) {
			out = append(out, candidate{r, LayerUserRule})
		}
	}

	for _, dir := range dirsRootToWorkdir(workdir) {
		out = append(out, candidate{filepath.Join(dir, "CLAUDE.md"), LayerProject})
		out = append(out, candidate{filepath.Join(dir, "CLAUDE.local.md"), LayerProjectLocal})
		out = append(out, candidate{filepath.Join(dir, ".claude", "CLAUDE.md"), LayerProject})
		for _, r := range globRules(filepath.Join(dir, ".claude", "rules")) {
			out = append(out, candidate{r, LayerProjectRule})
		}
	}

	// Keep only candidates that actually exist, and drop duplicates by absolute
	// path keeping the first (broadest) occurrence. The config dir can sit on
	// the root→workdir walk (e.g. ~/.claude/CLAUDE.md when the project is under
	// the home dir), so the same file would otherwise be collected as both the
	// user layer and a project layer.
	seen := map[string]bool{}
	var existing []candidate
	for _, c := range out {
		abs, err := filepath.Abs(c.path)
		if err != nil {
			abs = c.path
		}
		if seen[abs] || !fileExists(abs) {
			continue
		}
		seen[abs] = true
		existing = append(existing, candidate{abs, c.layer})
	}
	return existing
}

// dirsRootToWorkdir returns every directory from the filesystem root down to
// (and including) workdir, in that order.
func dirsRootToWorkdir(workdir string) []string {
	var dirs []string
	cur := workdir
	for {
		dirs = append(dirs, cur)
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	// Reverse so root comes first, workdir last.
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	return dirs
}

// globRules returns the .md files under a rules directory, recursively, sorted
// for deterministic order. Missing directories yield nothing.
func globRules(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".md") {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// readSource reads one candidate file, parses any paths: frontmatter, and
// applies claudeMdExcludes. Read errors are recorded, not returned.
func readSource(c candidate, excludes []string) Source {
	s := Source{Path: c.path, Layer: c.layer}
	data, err := os.ReadFile(c.path)
	if err != nil {
		s.Err = err.Error()
		return s
	}
	s.Raw = string(data)

	if c.layer == LayerUserRule || c.layer == LayerProjectRule {
		if paths, ok := parsePathsFrontmatter(s.Raw); ok {
			s.Conditional = true
			s.Paths = paths
		}
	}
	if matchesAny(c.path, excludes) {
		s.Excluded = true
	}
	return s
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func fileOrDirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
