package memory

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// resolveAutoMemory locates the project's auto-memory directory and loads the
// head of MEMORY.md (the only part Claude Code loads at session start). The
// directory is normally ProjectsDir/<encoded(mainRoot)>/memory, where mainRoot
// is the main checkout's root so every worktree of a repo shares it, but a
// settings autoMemoryDirectory override takes precedence. Returns nil if no
// memory directory can be found.
func resolveAutoMemory(in Input, workdir, mainRoot string) *AutoMemory {
	dir := autoMemoryDir(in.ConfigRoot, workdir)
	if dir == "" {
		dir = projectMemoryDir(in.ProjectsDir, mainRoot)
	}
	if dir == "" || !fileOrDirExists(dir) {
		return nil
	}

	am := &AutoMemory{Dir: dir, MemoryMDPath: filepath.Join(dir, "MEMORY.md")}

	if data, err := os.ReadFile(am.MemoryMDPath); err == nil {
		am.Head, am.Truncated = headOf(string(data))
	} else if !os.IsNotExist(err) {
		am.Err = err.Error()
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if am.Err == "" {
			am.Err = err.Error()
		}
		return am
	}
	for _, e := range entries {
		if e.IsDir() || strings.EqualFold(e.Name(), "MEMORY.md") {
			continue
		}
		if strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			am.TopicFiles = append(am.TopicFiles, e.Name())
		}
	}
	sort.Strings(am.TopicFiles)
	return am
}

// projectMemoryDir returns ProjectsDir/<encoded root>/memory, or "" if it
// can't be located. The hash Claude Code appends to an over-long name can't be
// reproduced, so such a folder is found by its prefix, and only if one matches.
func projectMemoryDir(projectsDir, root string) string {
	if projectsDir == "" {
		return ""
	}
	name := EncodeProjectDir(root)
	if len(name) > maxProjectDirName {
		name = uniqueLongerDir(projectsDir, name[:maxProjectDirName])
		if name == "" {
			return ""
		}
	}
	return filepath.Join(projectsDir, name, "memory")
}

// uniqueLongerDir returns the one directory in parent whose name starts with,
// and is longer than, prefix; "" if none or several do.
func uniqueLongerDir(parent, prefix string) string {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return ""
	}
	found := ""
	for _, e := range entries {
		if !e.IsDir() || len(e.Name()) <= len(prefix) || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if found != "" {
			return ""
		}
		found = e.Name()
	}
	return found
}

// headOf returns the first autoMemoryHeadLines lines or autoMemoryHeadBytes,
// whichever comes first, and whether the content was truncated.
func headOf(s string) (string, bool) {
	limit := len(s)
	truncated := false
	if limit > autoMemoryHeadBytes {
		limit = autoMemoryHeadBytes
		truncated = true
	}

	lines := 0
	for i := 0; i < limit; i++ {
		if s[i] == '\n' {
			lines++
			if lines == autoMemoryHeadLines {
				return s[:i+1], truncated || i+1 < len(s)
			}
		}
	}
	return s[:limit], truncated
}
