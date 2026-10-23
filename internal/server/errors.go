package server

import (
	"bytes"
	"net/http"
	"strings"
)

// friendlyErrors re-renders the plain-text errors handlers send through
// http.Error, http.NotFound and s.fail: a full error page for a browser
// request, or a flash message for htmx. Every other response, HTML 4xx
// included, passes through untouched.
func (s *Server) friendlyErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ew := &errorWriter{ResponseWriter: w}
		next.ServeHTTP(ew, r)
		if ew.code != 0 {
			s.renderError(w, r, ew.code, strings.TrimSpace(ew.body.String()))
		}
	})
}

// errorWriter holds back a response whose status is 400 or above and whose
// Content-Type is text/plain, which is what http.Error sets.
type errorWriter struct {
	http.ResponseWriter
	decided bool
	code    int // the held-back status; 0 while passing through
	body    bytes.Buffer
}

func (e *errorWriter) WriteHeader(code int) {
	if e.decided {
		if e.code == 0 {
			e.ResponseWriter.WriteHeader(code)
		}
		return
	}
	e.decided = true
	if code >= 400 && strings.HasPrefix(e.Header().Get("Content-Type"), "text/plain") {
		e.code = code
		return
	}
	e.ResponseWriter.WriteHeader(code)
}

func (e *errorWriter) Write(b []byte) (int, error) {
	if !e.decided {
		e.WriteHeader(http.StatusOK)
	}
	if e.code != 0 {
		return e.body.Write(b)
	}
	return e.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (e *errorWriter) Unwrap() http.ResponseWriter { return e.ResponseWriter }

// errorBack is the section an error page links back to.
type errorBack struct {
	Active string // nav item to mark current; "" for none
	Href   string
	Label  string
	Icon   string
}

func errorBackFor(path string) errorBack {
	section, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	switch section {
	case "sessions":
		return errorBack{"sessions", "/sessions", "sessions", "i-sessions"}
	case "memory", "advisor":
		return errorBack{"memory", "/memory", "memory", "i-memory"}
	case "trash":
		return errorBack{"trash", "/trash", "trash", "i-trash"}
	}
	return errorBack{"", "/sessions", "sessions", "i-sessions"}
}

// renderError writes the error with its original status. If the template
// fails, the plain-text error goes out as the handler sent it.
func (s *Server) renderError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	h := w.Header()
	if r.Method == http.MethodHead {
		h.Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(code)
		return
	}
	back := errorBackFor(r.URL.Path)
	data := map[string]any{
		"Title":      http.StatusText(code) + " — ccmanager",
		"StatusText": http.StatusText(code),
		"Message":    msg,
		"Back":       back,
	}
	name := "error-flash"
	if !isHTMX(r) {
		name = "error.html"
		data["Shell"] = s.shell(r, back.Active, "")
	}
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		if isHTMX(r) {
			// htmx swaps 4xx/5xx, and would insert this raw text as HTML.
			h.Set("HX-Reswap", "none")
		}
		http.Error(w, msg, code)
		return
	}
	h.Set("Content-Type", "text/html; charset=utf-8")
	if isHTMX(r) {
		// The requesting element's hx-target, hx-swap and hx-select are for
		// its own content, so the flash overrides all three.
		h.Set("HX-Retarget", "#flash")
		h.Set("HX-Reswap", "innerHTML")
		h.Set("HX-Reselect", ".flash__message")
	}
	w.WriteHeader(code)
	buf.WriteTo(w)
}
