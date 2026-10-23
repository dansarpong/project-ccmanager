package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Input is the pure engine's configuration. It is deliberately minimal (and
// independent of internal/config) so Resolve stays easy to test with a fake
// config root and managed-policy paths pointed at fixtures.
type Input struct {
	ConfigRoot          string // ~/.claude or $CLAUDE_CONFIG_DIR
	ProjectsDir         string // ConfigRoot/projects (for locating auto-memory)
	ManagedPath         string // managed-policy CLAUDE.md (in ManagedDir); "" disables
	ManagedSettingsPath string // managed-settings.json (in ManagedDir); "" disables
	HomeDir             string // the user's home dir; "" means os.UserHomeDir
	GOOS                string // the OS whose rules apply; "" means runtime.GOOS
}

func (in Input) home() string {
	home := in.HomeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home == "" {
		return ""
	}
	return filepath.Clean(home)
}

func (in Input) goos() string {
	if in.GOOS == "" {
		return runtime.GOOS
	}
	return in.GOOS
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

	r := findRepo(abs)
	layers := loadSettings(in, abs, r)
	excludes := mergeExcludes(layers)

	var sources []Source
	for i, c := range Candidates(in, abs) {
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

	stack.AutoMemory = resolveAutoMemory(in, layers, r.main)
	return stack, nil
}

// Candidate is a memory file that loads at session start, found by stat alone.
type Candidate struct {
	Path  string // absolute file path
	Layer string // one of the Layer* constants
	Dir   string // the walked directory a project-layer file belongs to; "" for managed and user files
}

// Candidates lists every memory file that loads for workdir, in Claude Code's
// load order, without reading any of them: broadest scope first (managed →
// user → user rules), then the filesystem walk from root down to the working
// dir, where deeper directories are read after shallower ones, and within a
// directory CLAUDE.local.md comes after CLAUDE.md. User rules always precede
// project rules. Resolve reads exactly these, in this order.
func Candidates(in Input, workdir string) []Candidate {
	return CandidatesAcross(in, []string{workdir})
}

// CandidatesAcross is the union of Candidates for several working directories:
// each file appears once, at its first position, and a directory shared by
// several working directories is only stat'ed once.
func CandidatesAcross(in Input, workdirs []string) []Candidate {
	var out []Candidate

	if in.ManagedPath != "" {
		out = append(out, Candidate{Path: in.ManagedPath, Layer: LayerManaged})
	}

	if in.ConfigRoot != "" {
		out = append(out, Candidate{Path: filepath.Join(in.ConfigRoot, "CLAUDE.md"), Layer: LayerUser})
		for _, r := range globRules(filepath.Join(in.ConfigRoot, "rules")) {
			out = append(out, Candidate{Path: r, Layer: LayerUserRule})
		}
	}

	walked := map[string]bool{}
	for _, workdir := range workdirs {
		abs, err := filepath.Abs(workdir)
		if err != nil {
			continue
		}
		for _, dir := range dirsRootToWorkdir(abs) {
			if walked[dir] {
				continue
			}
			walked[dir] = true
			out = append(out,
				Candidate{filepath.Join(dir, "CLAUDE.md"), LayerProject, dir},
				Candidate{filepath.Join(dir, "CLAUDE.local.md"), LayerProjectLocal, dir},
				Candidate{filepath.Join(dir, ".claude", "CLAUDE.md"), LayerProject, dir},
			)
			for _, r := range globRules(filepath.Join(dir, ".claude", "rules")) {
				out = append(out, Candidate{r, LayerProjectRule, dir})
			}
		}
	}

	// Keep only candidates that actually exist, and drop duplicates by absolute
	// path keeping the first (broadest) occurrence. The config dir can sit on
	// the root→workdir walk (e.g. ~/.claude/CLAUDE.md when the project is under
	// the home dir), so the same file would otherwise be collected as both the
	// user layer and a project layer.
	seen := map[string]bool{}
	var existing []Candidate
	for _, c := range out {
		abs, err := filepath.Abs(c.Path)
		if err != nil {
			abs = c.Path
		}
		if seen[abs] || !fileExists(abs) {
			continue
		}
		seen[abs] = true
		c.Path = abs
		existing = append(existing, c)
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
// applies claudeMdExcludes, which can't exclude the managed CLAUDE.md. Read
// errors are recorded, not returned.
func readSource(c Candidate, excludes []string) Source {
	s := Source{Path: c.Path, Layer: c.Layer}
	data, err := os.ReadFile(c.Path)
	if err != nil {
		s.Err = err.Error()
		return s
	}
	s.Raw = string(data)

	if c.Layer == LayerUserRule || c.Layer == LayerProjectRule {
		if paths, ok := parsePathsFrontmatter(s.Raw); ok {
			s.Conditional = true
			s.Paths = paths
		}
	}
	if c.Layer != LayerManaged && matchesAny(c.Path, excludes) {
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
