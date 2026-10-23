package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dansarpong/ccmanager/internal/advisor"
	"github.com/dansarpong/ccmanager/internal/safeio"
)

// advisorTimeout bounds a single claude invocation so a hung CLI cannot wedge a
// request.
const advisorTimeout = 90 * time.Second

func (s *Server) handleAdvisorAnalyze(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	dir := r.FormValue("dir")
	kind := r.FormValue("kind")
	if dir == "" {
		http.Error(w, "missing dir", http.StatusBadRequest)
		return
	}
	if !advisor.Available(s.cfg.ClaudeBin) {
		s.render(w, "_advisor.html", map[string]any{
			"Kind":        kind,
			"Dir":         dir,
			"Unavailable": "The claude CLI was not found on PATH. LLM analysis is unavailable; static lint still works.",
		})
		return
	}

	stack, err := resolveStackForDir(s, dir)
	if err != nil {
		s.fail(w, "resolving memory", err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), advisorTimeout)
	defer cancel()

	data := map[string]any{"Kind": kind, "Dir": dir}

	switch kind {
	case advisor.KindContradiction:
		found, err := s.advisor.Contradictions(ctx, stack)
		if err != nil {
			data["Unavailable"] = advisorMessage(err)
		} else {
			data["Contradictions"] = found
		}
	default: // rewrite
		var edits []editView
		for _, src := range stack.Sources {
			if src.Excluded || src.Raw == "" || !s.memoryScope(src.Path).editable() {
				continue
			}
			sugs, err := s.advisor.RewriteFile(ctx, src.Path, src.Raw)
			if err != nil {
				data["Unavailable"] = advisorMessage(err)
				edits = nil
				break
			}
			for _, sug := range sugs {
				edit := advisor.ApplyRewrite(src.Raw, sug)
				if !edit.Applied {
					continue // suggestion's original text no longer matches; skip
				}
				edits = append(edits, editView{
					File:       sug.File,
					Original:   sug.Original,
					Suggestion: sug.Suggestion,
					Reason:     sug.Reason,
					Diff:       edit.Diff,
				})
			}
		}
		data["Edits"] = edits
	}
	s.render(w, "_advisor.html", data)
}

// editView is a proposed rewrite rendered for review and re-application. The
// original/suggestion fields let the apply handler re-derive the exact edit
// (the rewrite is deterministic) without stashing large blobs in the form.
type editView struct {
	File       string
	Original   string
	Suggestion string
	Reason     string
	Diff       string
}

func (s *Server) handleAdvisorApply(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	file := r.FormValue("file")
	original := r.FormValue("original")
	suggestion := r.FormValue("suggestion")
	if file == "" || original == "" {
		http.Error(w, "missing fields", http.StatusBadRequest)
		return
	}
	if sc := s.memoryScope(file); !sc.editable() {
		http.Error(w, sc.editRefusal(), http.StatusForbidden)
		return
	}

	current, err := os.ReadFile(file)
	if err != nil {
		s.fail(w, "reading file", err)
		return
	}
	edit := advisor.ApplyRewrite(string(current), advisor.RewriteSuggestion{
		File:       file,
		Original:   original,
		Suggestion: suggestion,
	})
	if !edit.Applied {
		http.Error(w, "the original text was not found (the file changed since analysis)", http.StatusConflict)
		return
	}

	now := time.Now()
	if _, err := s.trash.BackupBeforeWrite(file, now); err != nil {
		s.fail(w, "backing up before write", err)
		return
	}
	body := []byte(strings.ReplaceAll(edit.NewContent, "\r\n", "\n"))
	if err := safeio.AtomicWrite(file, body, fileMode(file)); err != nil {
		s.fail(w, "applying edit", err)
		return
	}
	http.Redirect(w, r, "/memory/resolve?dir="+pathQuery(r.FormValue("dir")), http.StatusSeeOther)
}

func advisorMessage(err error) string {
	if errors.Is(err, advisor.ErrClaudeUnavailable) {
		return "claude is unavailable (it may not be logged in). Static lint still works. Details: " + err.Error()
	}
	return "Analysis failed: " + err.Error()
}
