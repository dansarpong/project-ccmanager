package server

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dansarpong/ccmanager/internal/safeio"
	"github.com/dansarpong/ccmanager/internal/store"
	"github.com/dansarpong/ccmanager/internal/trash"
)

func (s *Server) handleMemoryEdit(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	if fileMissing(w, path) {
		return
	}
	f := s.memoryScope(path)
	if !f.editable() {
		http.Error(w, f.editRefusal(), http.StatusForbidden)
		return
	}
	data, err := os.ReadFile(f.target)
	if err != nil {
		s.fail(w, "reading file", err)
		return
	}
	snap, err := safeio.Snapshot(f.target)
	if err != nil {
		s.fail(w, "snapshotting file", err)
		return
	}
	view := map[string]any{
		"Title":   "Edit — ccmanager",
		"Active":  "memory",
		"Path":    path,
		"Content": string(data),
		"Saved":   r.URL.Query().Get("saved") == "1",
		// The snapshot token lets save detect on-disk changes since open.
		"Token": fmt.Sprintf("%d:%d", snap.Mtime, snap.Size),
	}
	s.page(w, r, "memory_edit.html", view)
}

func (s *Server) handleMemorySave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	path := r.FormValue("path")
	content := r.FormValue("content")
	token := r.FormValue("token")
	force := r.FormValue("force") == "1"

	if path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	if fileMissing(w, path) {
		return
	}
	f := s.memoryScope(path)
	if !f.editable() {
		http.Error(w, f.editRefusal(), http.StatusForbidden)
		return
	}

	prev, ok := parseToken(token)
	if !ok {
		http.Error(w, "missing or invalid change token", http.StatusBadRequest)
		return
	}

	// Detect an on-disk change since the file was opened, unless the user chose
	// to overwrite anyway.
	changed, err := safeio.Changed(f.target, prev)
	if err != nil {
		s.fail(w, "checking for on-disk changes", err)
		return
	}
	if changed && !force {
		cur, _ := safeio.Snapshot(f.target)
		s.page(w, r, "_save_conflict.html", map[string]any{
			"Title":   "Conflict — ccmanager",
			"Active":  "memory",
			"Path":    path,
			"Content": content,
			"Token":   fmt.Sprintf("%d:%d", cur.Mtime, cur.Size),
		})
		return
	}

	now := time.Now()
	// Back up under the edited path, not its target: a roll back re-checks that path.
	if _, err := s.trash.BackupBeforeWrite(path, now); err != nil {
		s.fail(w, "backing up before write", err)
		return
	}
	// Normalize CRLF that browsers may submit, and preserve file mode.
	body := []byte(strings.ReplaceAll(content, "\r\n", "\n"))
	if err := safeio.AtomicWrite(f.target, body, fileMode(f.target)); err != nil {
		s.fail(w, "writing file", err)
		return
	}

	http.Redirect(w, r, "/memory/edit?path="+pathQuery(path)+"&saved=1", http.StatusSeeOther)
}

func (s *Server) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	path := r.FormValue("path")
	if path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	if fileMissing(w, path) {
		return
	}
	if sc := s.memoryScope(path); !sc.deletable() {
		http.Error(w, sc.deleteRefusal(), http.StatusForbidden)
		return
	}
	kind := trash.KindMemoryDelete
	if _, err := s.trash.MoveToTrash(kind, path, "", "", time.Now()); err != nil {
		s.fail(w, "moving file to trash", err)
		return
	}
	if isHTMX(r) {
		// An empty body removes the entry (outerHTML); the sidebar counts
		// update out of band.
		s.render(w, "nav-counts-oob", s.nav("memory", ""))
		return
	}
	http.Redirect(w, r, "/memory", http.StatusSeeOther)
}

func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	se, err := s.store.GetSession(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		s.fail(w, "getting session", err)
		return
	}

	// Guard: warn before deleting the most-recently-active session, unless the
	// user has confirmed.
	if r.FormValue("confirm") != "1" {
		mostRecent, _ := s.store.MostRecentlyActiveID()
		if mostRecent == id {
			if isHTMX(r) {
				w.Header().Set("HX-Redirect", "/sessions/"+url.PathEscape(id)+"/delete")
				return
			}
			s.renderDeleteConfirm(w, r, se, true)
			return
		}
	}

	if _, err := s.trash.MoveToTrash(trash.KindSession, se.FilePath, se.SessionID, "", time.Now()); err != nil {
		s.fail(w, "moving session to trash", err)
		return
	}
	if err := s.store.DeleteSessionRows(se.SessionID); err != nil {
		s.fail(w, "removing session from index", err)
		return
	}
	redirectAfterPost(w, r, "/sessions")
}

// handleSessionDeleteConfirm shows the delete confirmation page, which an
// htmx delete of the most recent session is redirected to.
func (s *Server) handleSessionDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	se, err := s.store.GetSession(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		s.fail(w, "getting session", err)
		return
	}
	mostRecent, _ := s.store.MostRecentlyActiveID()
	s.renderDeleteConfirm(w, r, se, mostRecent == id)
}

func (s *Server) renderDeleteConfirm(w http.ResponseWriter, r *http.Request, se store.Session, mostRecent bool) {
	data := map[string]any{
		"Title":   "Confirm delete — ccmanager",
		"Active":  "sessions",
		"Session": se,
	}
	if mostRecent {
		data["Warning"] = "This is your most recently active session."
	}
	s.page(w, r, "_delete_confirm.html", data)
}

func (s *Server) handleTrashList(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.ListTrash()
	if err != nil {
		s.fail(w, "listing trash", err)
		return
	}
	rows := make([]trashRow, len(entries))
	for i, e := range entries {
		rows[i] = trashRow{TrashEntry: e}
		if e.Kind == trash.KindMemoryBackup {
			_, err := os.Lstat(e.OriginalPath)
			rows[i].RollBack = err == nil
		}
	}
	s.page(w, r, "trash.html", map[string]any{
		"Title":   "Trash — ccmanager",
		"Active":  "trash",
		"Entries": rows,
	})
}

// trashRow is a trash entry as listed. RollBack marks a backup whose original
// still exists, so restoring it replaces the current version.
type trashRow struct {
	store.TrashEntry
	RollBack bool
}

func (s *Server) handleTrashRestore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	// A roll back writes through the backup's original path, which may since
	// have left scope. A broken link there is refused by Restore itself.
	if e, err := s.store.GetTrash(id); err == nil && e.Kind == trash.KindMemoryBackup {
		if _, err := os.Stat(e.OriginalPath); !errors.Is(err, os.ErrNotExist) {
			if f := s.memoryScope(e.OriginalPath); !f.editable() {
				http.Error(w, f.editRefusal(), http.StatusForbidden)
				return
			}
		}
	}
	entry, err := s.trash.Restore(id, time.Now())
	if errors.Is(err, trash.ErrExists) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		s.fail(w, "restoring", err)
		return
	}
	// A restored transcript must go back into the search index.
	if entry.Kind == trash.KindSession {
		if _, err := s.reindex(); err != nil {
			s.fail(w, "re-indexing restored session", err)
			return
		}
	}
	redirectAfterPost(w, r, "/trash")
}

func (s *Server) handleTrashEmpty(w http.ResponseWriter, r *http.Request) {
	if _, err := s.trash.EmptyNow(); err != nil {
		s.fail(w, "emptying trash", err)
		return
	}
	redirectAfterPost(w, r, "/trash")
}

// helpers

// fileMissing answers 404 when nothing is at path; the scope check would
// otherwise report a missing CLAUDE.md as one ccmanager refuses to touch.
func fileMissing(w http.ResponseWriter, path string) bool {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		http.Error(w, "no file at "+path, http.StatusNotFound)
		return true
	}
	return false
}

func parseToken(token string) (safeio.FileState, bool) {
	parts := strings.SplitN(token, ":", 2)
	if len(parts) != 2 {
		return safeio.FileState{}, false
	}
	mtime, err1 := strconv.ParseInt(parts[0], 10, 64)
	size, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil {
		return safeio.FileState{}, false
	}
	return safeio.FileState{Mtime: mtime, Size: size}, true
}

func fileMode(path string) os.FileMode {
	if info, err := os.Stat(path); err == nil {
		return info.Mode().Perm()
	}
	return 0o644
}

// pathQuery escapes a filesystem path for use in a query string value.
func pathQuery(p string) string {
	return url.QueryEscape(p)
}
