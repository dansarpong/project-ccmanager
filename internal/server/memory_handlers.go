package server

import (
	"net/http"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/dansarpong/ccmanager/internal/advisor"
	"github.com/dansarpong/ccmanager/internal/memory"
)

// managedPolicyDir is this OS's managed-policy directory; a variable so
// tests can keep the machine's real policy out of their results.
var managedPolicyDir = memory.ManagedDir(runtime.GOOS)

// memoryInput builds the pure engine's Input from the app config. The managed
// policy files are simply absent when they don't exist on this machine.
func (s *Server) memoryInput() memory.Input {
	return memory.Input{
		ConfigRoot:          s.cfg.ConfigRoot,
		ProjectsDir:         s.cfg.ProjectsDir,
		ManagedPath:         filepath.Join(managedPolicyDir, "CLAUDE.md"),
		ManagedSettingsPath: filepath.Join(managedPolicyDir, "managed-settings.json"),
		GOOS:                runtime.GOOS,
	}
}

// resolveStackForDir resolves the memory stack for a directory using the
// app's engine input. Shared by the memory and advisor handlers.
func resolveStackForDir(s *Server, dir string) (*memory.ResolvedStack, error) {
	return memory.Resolve(s.memoryInput(), dir)
}

// resolveDir returns the working directory to resolve memory for: the ?dir=
// query if present, otherwise the most-recently-active session's project path.
func (s *Server) resolveDir(r *http.Request) string {
	if dir := r.URL.Query().Get("dir"); dir != "" {
		return dir
	}
	id, err := s.store.MostRecentlyActiveID()
	if err != nil || id == "" {
		return ""
	}
	se, err := s.store.GetSession(id)
	if err != nil {
		return ""
	}
	return se.ProjectPath
}

func (s *Server) handleMemoryResolve(w http.ResponseWriter, r *http.Request) {
	dir := s.resolveDir(r)
	projects, err := s.store.DistinctProjects()
	if err != nil {
		s.fail(w, "listing projects", err)
		return
	}

	data := map[string]any{
		"Title":    "Memory — ccmanager",
		"Active":   "memory",
		"Dir":      dir,
		"Projects": projects,
	}

	if dir != "" {
		stack, err := memory.Resolve(s.memoryInput(), dir)
		if err != nil {
			s.fail(w, "resolving memory", err)
			return
		}
		data["Stack"] = stack
		data["Findings"] = advisor.Lint(stack)
	}
	s.page(w, r, "memory_resolve.html", data)
}

func (s *Server) handleMemoryLint(w http.ResponseWriter, r *http.Request) {
	dir := s.resolveDir(r)
	if dir == "" {
		s.render(w, "_lint.html", map[string]any{"Findings": nil})
		return
	}
	stack, err := memory.Resolve(s.memoryInput(), dir)
	if err != nil {
		s.fail(w, "resolving memory", err)
		return
	}
	s.render(w, "_lint.html", map[string]any{"Findings": advisor.Lint(stack)})
}

func (s *Server) handleMemoryInventory(w http.ResponseWriter, r *http.Request) {
	items, err := scanInventory(s.cfg.ConfigRoot, s.cfg.ProjectsDir)
	if err != nil {
		s.fail(w, "scanning memory inventory", err)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	kind := r.URL.Query().Get("kind")
	open := r.URL.Query().Get("open")
	terms := strings.Fields(q)

	// Project paths are best effort: without them an auto-memory entry shows
	// its encoded projects/ folder name instead.
	dirs, _ := s.store.ProjectPathsByDir()
	entries := []memoryEntry{}
	readOnly := 0
	for _, it := range items {
		if it.ReadOnly {
			readOnly++
		}
		e := newMemoryEntry(it)
		if !matchesInventoryKind(it.Kind, kind) || !containsAll(it.Path+" "+e.Title, terms) {
			continue
		}
		e.Open = e.ID == open
		if it.ReadOnly {
			folder := autoMemoryProject(s.cfg.ProjectsDir, it.Path)
			e.Project = folder
			if path, ok := dirs[folder]; ok {
				e.Project = tildePath(path)
			}
		}
		if e.SourceSessionID != "" {
			_, err := s.store.GetSession(e.SourceSessionID)
			e.SourceIndexed = err == nil
		}
		entries = append(entries, e)
	}
	data := map[string]any{
		"Title":    "Memory — ccmanager",
		"Active":   "memory",
		"Entries":  entries,
		"Total":    len(items),
		"ReadOnly": readOnly,
		"Query":    q,
		"Kind":     kind,
		"Filtered": q != "" || kind != "",
	}
	s.page(w, r, "memory_inventory.html", data)
}
