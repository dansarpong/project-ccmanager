// Package memory reproduces how Claude Code assembles memory (CLAUDE.md, rules,
// and auto-memory) for a given working directory. The Resolve function is pure
// (filesystem in, struct out) so it is easy to test, and it returns an ordered
// structure with provenance back to every source file.
//
// The resolution rules mirror Claude Code's documented behaviour
// (code.claude.com/docs/en/memory), verified against v2.1.186. The key rule is
// load order: broadest scope first, most specific last, so the most specific
// instruction is the one Claude reads last.
package memory

// Layer names a source's scope, in increasing specificity.
const (
	LayerManaged      = "managed"       // <ManagedDir>/CLAUDE.md (org policy)
	LayerUser         = "user"          // ~/.claude/CLAUDE.md
	LayerUserRule     = "user-rule"     // ~/.claude/rules/*.md
	LayerProject      = "project"       // <dir>/CLAUDE.md or <dir>/.claude/CLAUDE.md
	LayerProjectLocal = "project-local" // <dir>/CLAUDE.local.md
	LayerProjectRule  = "project-rule"  // <dir>/.claude/rules/*.md
)

// Source is one memory file that loads at session start, with provenance.
type Source struct {
	Path        string   // absolute file path
	Layer       string   // one of the Layer* constants
	Order       int      // global load order (0 = read first / broadest)
	Conditional bool     // rule carries paths: frontmatter (applies only to matching files)
	Paths       []string // the paths: globs, when Conditional
	Excluded    bool     // matched a claudeMdExcludes pattern
	Raw         string   // file contents exactly as on disk
	Expanded    string   // imports inlined + block comments stripped ("what Claude sees")
	Imports     []Import // imports found directly in this file
	Err         string   // read/parse problem, if any (never fatal to Resolve)
}

// Import is a single @path reference discovered while expanding a file.
type Import struct {
	Spec         string // the raw @path text
	ResolvedPath string // absolute path it resolves to ("" if unresolved)
	Depth        int    // 1 for a top-level import, up to MaxImportDepth
	External     bool   // resolves outside the project subtree (approval-gated)
	Circular     bool   // would re-enter a file already on the import chain
	Missing      bool   // resolved path does not exist
}

// ImportEdge is one edge of the import graph, for the per-project graph view.
type ImportEdge struct {
	From     string // importing file (absolute)
	To       string // imported file (absolute or the raw spec if unresolved)
	External bool
	Circular bool
	Missing  bool
}

// AutoMemory describes the project's auto-memory directory. Only the head of
// MEMORY.md loads at session start; topic files load on demand.
type AutoMemory struct {
	Dir          string   // the memory/ directory
	MemoryMDPath string   // MEMORY.md path
	Head         string   // first 200 lines or 25 KB of MEMORY.md, whichever comes first
	Truncated    bool     // true if MEMORY.md is larger than the head limit
	TopicFiles   []string // other *.md files in the dir (loaded on demand, not at start)
	Err          string
}

// ResolvedStack is the full result of resolving memory for a working directory.
type ResolvedStack struct {
	Workdir     string
	ConfigRoot  string
	Sources     []Source     // in load order
	ImportEdges []ImportEdge // flattened import graph across all sources
	AutoMemory  *AutoMemory  // nil if no memory dir found
}

// MaxImportDepth is the maximum number of @path hops Claude Code follows.
const MaxImportDepth = 4

// autoMemoryHeadLines and autoMemoryHeadBytes bound how much of MEMORY.md loads
// at session start (first 200 lines or 25 KB, whichever comes first).
const (
	autoMemoryHeadLines = 200
	autoMemoryHeadBytes = 25 * 1024
)
