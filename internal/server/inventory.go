package server

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dansarpong/ccmanager/internal/memory"
)

// InventoryItem is one memory file found on the machine.
type InventoryItem struct {
	Path      string
	Kind      string // an engine layer (memory.Layer*), "auto-memory" or "auto-memory-topic"
	Dir       string // the directory a project-layer file belongs to
	Lines     int
	Size      int64
	ModTime   string // RFC 3339
	Content   string
	ReadOnly  bool // managed policy and auto memory can't be edited
	Deletable bool // everything but managed policy can be moved to the trash
	TooLong   bool // over the 200-line guideline (a quick lint badge)
}

const inventoryLineWarn = 200

// inventoryFile is a memory file's location and kind, found without reading it.
type inventoryFile struct {
	Path      string
	Kind      string
	Dir       string
	ReadOnly  bool
	Deletable bool
}

// inventoryFiles lists every memory file ccmanager knows how to find, by stat
// alone: everything the engine loads for any project recorded in the
// transcripts (managed policy, the user's CLAUDE.md and rules, and each
// project's CLAUDE.md, CLAUDE.local.md and rules up to the filesystem root),
// plus every project's auto memory under ProjectsDir. A project whose
// directory is gone contributes nothing.
func (s *Server) inventoryFiles() ([]inventoryFile, error) {
	projects, err := s.store.DistinctProjects()
	if err != nil {
		return nil, err
	}
	var live []string
	for _, p := range projects {
		if isDir(p) {
			live = append(live, p)
		}
	}
	var files []inventoryFile
	for _, c := range memory.CandidatesAcross(s.memoryInput(), live) {
		managed := c.Layer == memory.LayerManaged
		files = append(files, inventoryFile{Path: c.Path, Kind: c.Layer, Dir: c.Dir, ReadOnly: managed, Deletable: !managed})
	}

	// Auto-memory under each project directory.
	entries, err := os.ReadDir(s.cfg.ProjectsDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading projects dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		memDir := filepath.Join(s.cfg.ProjectsDir, e.Name(), "memory")
		for _, m := range mdFiles(memDir) {
			kind := "auto-memory-topic"
			if strings.EqualFold(filepath.Base(m), "MEMORY.md") {
				kind = "auto-memory"
			}
			files = append(files, inventoryFile{Path: m, Kind: kind, ReadOnly: true, Deletable: true})
		}
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// scanInventory reads every file inventoryFiles finds.
func (s *Server) scanInventory() ([]InventoryItem, error) {
	files, err := s.inventoryFiles()
	if err != nil {
		return nil, err
	}
	var items []InventoryItem
	for _, f := range files {
		info, err := os.Stat(f.Path)
		if err != nil || info.IsDir() {
			continue
		}
		data, _ := os.ReadFile(f.Path)
		lines := countFileLines(data)
		items = append(items, InventoryItem{
			Path:      f.Path,
			Kind:      f.Kind,
			Dir:       f.Dir,
			Lines:     lines,
			Size:      info.Size(),
			ModTime:   info.ModTime().UTC().Format(time.RFC3339),
			Content:   string(data),
			ReadOnly:  f.ReadOnly,
			Deletable: f.Deletable,
			TooLong:   lines > inventoryLineWarn,
		})
	}
	return items, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isAutoMemoryKind(kind string) bool {
	return strings.HasPrefix(kind, "auto-memory")
}

func mdFiles(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".md") {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func countFileLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := 0
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}

var inventoryKindLabels = map[string]string{
	"managed":           "Managed policy",
	"user":              "User CLAUDE.md",
	"user-rule":         "User rule",
	"project":           "Project CLAUDE.md",
	"project-local":     "Project CLAUDE.local.md",
	"project-rule":      "Project rule",
	"auto-memory":       "Auto memory index",
	"auto-memory-topic": "Auto memory topic",
}

// memoryEntry is the data for the "memory-entry" partial.
type memoryEntry struct {
	ID              string
	Title           string
	Path            string
	Kind            string
	KindLabel       string
	Project         string // the project a project or auto-memory file belongs to
	Preview         string
	SourceSessionID string // originSessionId from the frontmatter, if any
	SourceIndexed   bool   // the source session is in the index, so a link works
	UpdatedAt       string
	Lines           int
	Size            int64
	ReadOnly        bool
	ReadOnlyNote    string // why ReadOnly, for the disabled actions
	Deletable       bool
	TooLong         bool
	Open            bool
	Body            codeBlockView
}

func newMemoryEntry(it InventoryItem) memoryEntry {
	fields, body := frontmatter(it.Content)
	id := memoryID(it.Path)
	e := memoryEntry{
		ID:              id,
		Title:           firstHeading(body),
		Path:            it.Path,
		Kind:            it.Kind,
		KindLabel:       inventoryKindLabels[it.Kind],
		Project:         tildePath(it.Dir),
		Preview:         fields["description"],
		SourceSessionID: fields["originSessionId"],
		UpdatedAt:       it.ModTime,
		Lines:           it.Lines,
		Size:            it.Size,
		ReadOnly:        it.ReadOnly,
		Deletable:       it.Deletable,
		TooLong:         it.TooLong,
		Body:            codeBlock("body-"+id, tildePath(it.Path), it.Content),
	}
	if it.ReadOnly {
		e.ReadOnlyNote = "Auto memory is read-only"
		if it.Kind == memory.LayerManaged {
			e.ReadOnlyNote = "Managed policy is read-only"
		}
	}
	if e.Title == "" {
		e.Title = fields["name"]
	}
	if e.Title == "" {
		e.Title = filepath.Base(it.Path)
	}
	if e.Preview == "" {
		e.Preview = firstProseLine(body)
	}
	return e
}

// frontmatter returns the scalar "key: value" fields of a leading "---" block,
// at any indent, and the text after it. It is deliberately shallow: the
// inventory only reads name, description and originSessionId.
func frontmatter(content string) (map[string]string, string) {
	fields := map[string]string{}
	text := strings.ReplaceAll(strings.TrimPrefix(content, "\uFEFF"), "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return fields, text
	}
	block, body, ok := strings.Cut(rest, "\n---")
	if !ok {
		return fields, text
	}
	for _, line := range strings.Split(block, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || strings.ContainsAny(key, " \t") {
			continue
		}
		fields[key] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	_, body, _ = strings.Cut(body, "\n")
	return fields, body
}

func firstHeading(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if h, ok := strings.CutPrefix(strings.TrimSpace(line), "# "); ok {
			return strings.TrimSpace(h)
		}
	}
	return ""
}

// firstProseLine returns the first line that is not blank, a heading or an
// HTML comment, for the one-line preview.
func firstProseLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "<!--") {
			continue
		}
		return t
	}
	return ""
}

// autoMemoryProject returns the projects/<encoded> folder an auto-memory path
// lives in, or "".
func autoMemoryProject(projectsDir, path string) string {
	rel, err := filepath.Rel(projectsDir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	dir, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	return dir
}

// matchesInventoryKind applies the inventory's kind filter; "auto-memory"
// covers both the index and topic files, and "project" every project file.
func matchesInventoryKind(kind, filter string) bool {
	switch filter {
	case "":
		return true
	case "auto-memory", "project":
		return kind == filter || strings.HasPrefix(kind, filter+"-")
	default:
		return kind == filter
	}
}
