package server

import (
	"fmt"
	"hash/fnv"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dansarpong/ccmanager/internal/store"
)

// sessionActiveWindow is how recently a transcript must have been written for
// its session to count as active. It reads the indexed mtime, so status is as
// fresh as the last index refresh, and agrees with the row's last-active time.
const sessionActiveWindow = 10 * time.Minute

// sessionView is a session with its derived status. There is no "archived":
// a trashed session leaves the index, so the list never holds one.
type sessionView struct {
	store.Session
	Status      string // "active" | "idle"
	StatusLabel string
	Current     bool
}

func newSessionView(se store.Session, now time.Time) sessionView {
	v := sessionView{Session: se, Status: "idle", StatusLabel: "Idle"}
	if se.FileMtime > 0 && now.Sub(time.Unix(se.FileMtime, 0)) < sessionActiveWindow {
		v.Status, v.StatusLabel = "active", "Active"
	}
	return v
}

func sessionViews(list []store.Session, now time.Time) []sessionView {
	out := make([]sessionView, len(list))
	for i, se := range list {
		out[i] = newSessionView(se, now)
	}
	return out
}

// navItem is the data for the "nav-item" partial.
type navItem struct {
	Key     string // count element id suffix: count-<Key>
	Href    string
	Icon    string
	Label   string
	Title   string
	Count   int
	Current bool
}

// shell is everything the layout needs around a page: theme, sidebar and the
// palette's initial results.
type shell struct {
	Theme    string
	IsMac    bool
	Nav      []navItem
	Projects []navItem
	Palette  paletteView
}

// page renders a full page with the sidebar and palette. data["Active"] names
// the current section; data["Project"] the project filter, if any.
func (s *Server) page(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	active, _ := data["Active"].(string)
	project, _ := data["Project"].(string)
	data["Shell"] = s.shell(r, active, project)
	s.render(w, name, data)
}

func (s *Server) shell(r *http.Request, active, project string) shell {
	sh := shell{
		Theme:   themeFromCookie(r),
		IsMac:   strings.Contains(r.UserAgent(), "Macintosh"),
		Nav:     s.nav(active, project),
		Palette: s.palette(""),
	}
	// Counts are decoration: a failed count hides the number, not the page.
	projects, _ := s.store.ProjectCounts()
	for i, p := range projects {
		sh.Projects = append(sh.Projects, navItem{
			Key:     fmt.Sprintf("project-%d", i),
			Href:    "/sessions?project=" + url.QueryEscape(p.Path),
			Icon:    "i-folder",
			Label:   filepath.Base(p.Path),
			Title:   p.Path,
			Count:   p.Sessions,
			Current: active == "sessions" && project == p.Path,
		})
	}
	return sh
}

// nav builds the top-level sidebar items with their counts.
func (s *Server) nav(active, project string) []navItem {
	sessionsN, _ := s.store.CountSessions()
	trashN, _ := s.store.CountTrash()
	files, _ := s.inventoryFiles()
	return []navItem{
		{Key: "sessions", Href: "/sessions", Icon: "i-sessions", Label: "Sessions", Count: sessionsN,
			Current: active == "sessions" && project == ""},
		{Key: "memory", Href: "/memory", Icon: "i-memory", Label: "Memory", Count: len(files),
			Current: active == "memory"},
		{Key: "trash", Href: "/trash", Icon: "i-trash", Label: "Trash", Count: trashN,
			Current: active == "trash"},
	}
}

// themeFromCookie returns an explicit theme override, or "" to follow the OS.
func themeFromCookie(r *http.Request) string {
	c, err := r.Cookie("theme")
	if err != nil {
		return ""
	}
	if c.Value == "light" || c.Value == "dark" {
		return c.Value
	}
	return ""
}

func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// redirectAfterPost sends the browser to url once a POST succeeds. An htmx
// request gets HX-Redirect instead of a 303: XHR would follow the 303 itself
// and swap the whole next page into the requesting element.
func redirectAfterPost(w http.ResponseWriter, r *http.Request, url string) {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", url)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// paletteItem, paletteGroup and paletteView are the data for "palette-results".
type paletteItem struct {
	ID        string
	Href      string
	Icon      string
	LabelHTML template.HTML
	Meta      string
	Disabled  bool
}

type paletteGroup struct {
	Key   string
	Label string
	Items []paletteItem
}

type paletteView struct {
	Query  string
	Groups []paletteGroup
}

const paletteGroupLimit = 6

var paletteCommands = []struct{ Key, Label, Href string }{
	{"sessions", "Go to sessions", "/sessions"},
	{"memory", "Go to memory inventory", "/memory"},
	{"resolve", "Resolve memory for a directory", "/memory/resolve"},
	{"trash", "Go to trash", "/trash"},
}

// palette builds the grouped results for a query. With no query it lists the
// most recent sessions and every command, so the palette opens with no request.
func (s *Server) palette(q string) paletteView {
	v := paletteView{Query: q}
	terms := strings.Fields(q)

	if g := s.paletteSessions(q, terms); len(g.Items) > 0 {
		v.Groups = append(v.Groups, g)
	}
	if len(terms) > 0 {
		g := paletteGroup{Key: "memory", Label: "Memory"}
		files, _ := s.inventoryFiles()
		dirs, _ := s.store.ProjectPathsByDir()
		for _, f := range files {
			if !containsAll(tildePath(f.Path), terms) {
				continue
			}
			// Paths truncate from the end, which hides the file name, so the
			// name is the label and the folder or project is the meta.
			meta := tildePath(filepath.Dir(f.Path))
			if isAutoMemoryKind(f.Kind) {
				meta = autoMemoryProject(s.cfg.ProjectsDir, f.Path)
				if path, ok := dirs[meta]; ok {
					meta = filepath.Base(path)
				}
			}
			id := memoryID(f.Path)
			g.Items = append(g.Items, paletteItem{
				ID:        "m-" + id,
				Href:      "/memory?open=" + id + "#memory-" + id,
				Icon:      "i-memory",
				LabelHTML: markMatches(filepath.Base(f.Path), q),
				Meta:      meta,
			})
			if len(g.Items) == paletteGroupLimit {
				break
			}
		}
		if len(g.Items) > 0 {
			v.Groups = append(v.Groups, g)
		}
	}
	g := paletteGroup{Key: "commands", Label: "Commands"}
	for _, c := range paletteCommands {
		if containsAll(c.Label, terms) {
			g.Items = append(g.Items, paletteItem{
				ID: "c-" + c.Key, Href: c.Href, Icon: "i-command", LabelHTML: markMatches(c.Label, q),
			})
		}
	}
	if len(g.Items) > 0 {
		v.Groups = append(v.Groups, g)
	}
	return v
}

// paletteSessions finds sessions whose name or ID matches first, then sessions
// whose transcripts match through the full-text index. Lookups are best
// effort: a failed query only leaves its sessions out.
func (s *Server) paletteSessions(q string, terms []string) paletteGroup {
	g := paletteGroup{Key: "sessions", Label: "Sessions"}
	var found []store.Session
	if len(terms) == 0 {
		found, _ = s.store.RecentSessions(paletteGroupLimit)
	} else {
		all, _ := s.store.ListSessions("activity", "")
		for _, se := range all {
			if containsAll(se.DisplayName+" "+se.SessionID, terms) {
				found = append(found, se)
			}
		}
		hits, _ := s.store.SearchFTS(ftsQuery(q), 50)
		for _, h := range hits {
			found = append(found, store.Session{SessionID: h.SessionID, DisplayName: h.DisplayName})
		}
	}
	seen := map[string]bool{}
	for _, se := range found {
		if seen[se.SessionID] || se.SessionID == "" {
			continue
		}
		seen[se.SessionID] = true
		label := se.DisplayName
		if label == "" {
			label = se.SessionID
		}
		g.Items = append(g.Items, paletteItem{
			ID:        "s-" + se.SessionID,
			Href:      "/sessions/" + url.PathEscape(se.SessionID),
			Icon:      "i-sessions",
			LabelHTML: markMatches(label, q),
			Meta:      shortID(se.SessionID),
		})
		if len(g.Items) == paletteGroupLimit {
			break
		}
	}
	return g
}

// containsAll reports whether s contains every term, ignoring case.
func containsAll(s string, terms []string) bool {
	lower := strings.ToLower(s)
	for _, t := range terms {
		if !strings.Contains(lower, strings.ToLower(t)) {
			return false
		}
	}
	return true
}

// markMatches escapes label and wraps each case-insensitive match of a query
// term in <mark>, the same escape-then-mark approach as highlightSnippet. The
// matches are found on the raw text and each piece is escaped on its own, so a
// term can never match inside an entity like &amp;.
func markMatches(label, q string) template.HTML {
	terms := strings.Fields(q)
	if len(terms) == 0 {
		return template.HTML(template.HTMLEscapeString(label))
	}
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = regexp.QuoteMeta(t)
	}
	re := regexp.MustCompile("(?i)" + strings.Join(quoted, "|"))
	re.Longest()
	var b strings.Builder
	last := 0
	for _, m := range re.FindAllStringIndex(label, -1) {
		b.WriteString(template.HTMLEscapeString(label[last:m[0]]))
		b.WriteString("<mark>")
		b.WriteString(template.HTMLEscapeString(label[m[0]:m[1]]))
		b.WriteString("</mark>")
		last = m[1]
	}
	b.WriteString(template.HTMLEscapeString(label[last:]))
	return template.HTML(b.String())
}

// memoryID is a short, stable, id-safe key for a memory file path.
func memoryID(path string) string {
	h := fnv.New32a()
	h.Write([]byte(path))
	return fmt.Sprintf("%08x", h.Sum32())
}

// tildePath shortens a path under the home directory to ~/…, for display.
func tildePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~/" + rest
	}
	return path
}
