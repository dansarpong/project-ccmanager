// Package server wires the HTTP routes and renders templates. It serves only
// on 127.0.0.1 so the tool — which can edit memory and delete sessions — is
// never reachable on the LAN.
package server

import (
	"fmt"
	"html/template"
	"net"
	"net/http"
	"path/filepath"
	"runtime"

	"github.com/dansarpong/ccmanager/internal/advisor"
	"github.com/dansarpong/ccmanager/internal/config"
	"github.com/dansarpong/ccmanager/internal/sessions"
	"github.com/dansarpong/ccmanager/internal/store"
	"github.com/dansarpong/ccmanager/internal/trash"
)

// Server holds the dependencies shared by all handlers.
type Server struct {
	cfg     config.Config
	store   *store.Store
	trash   *trash.Trash
	advisor *advisor.Advisor
	tmpl    *template.Template
}

// New builds a Server with its templates parsed.
func New(cfg config.Config, st *store.Store, tr *trash.Trash) (*Server, error) {
	tmpl, err := parseTemplates()
	if err != nil {
		return nil, fmt.Errorf("parsing templates: %w", err)
	}
	cli := advisor.CLI{
		Bin:        cfg.ClaudeBin,
		WorkDir:    filepath.Join(cfg.DataDir, "advisor"),
		ManagedDir: managedDir(runtime.GOOS),
	}
	return &Server{
		cfg:     cfg,
		store:   st,
		trash:   tr,
		advisor: advisor.NewAdvisor(cli, st),
		tmpl:    tmpl,
	}, nil
}

// managedDir is where Claude Code reads managed (policy) settings on goos.
func managedDir(goos string) string {
	switch goos {
	case "linux":
		return "/etc/claude-code"
	case "darwin":
		return "/Library/Application Support/ClaudeCode"
	case "windows":
		return `C:\Program Files\ClaudeCode`
	}
	return ""
}

// Routes returns the HTTP handler with all routes registered, using Go 1.22+
// method-based routing, behind the loopback Host check and cross-origin
// protection for state-changing requests.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/sessions", http.StatusFound)
	})
	mux.HandleFunc("GET /sessions", s.handleSessions)
	mux.HandleFunc("GET /sessions/{id}", s.handleSessionDetail)
	mux.HandleFunc("GET /sessions/{id}/transcript", s.handleTranscript)
	mux.HandleFunc("GET /search", s.handleSearch)
	mux.HandleFunc("GET /palette", s.handlePalette)
	mux.HandleFunc("POST /refresh", s.handleRefresh)

	mux.HandleFunc("GET /memory", s.handleMemoryInventory)
	mux.HandleFunc("GET /memory/resolve", s.handleMemoryResolve)
	mux.HandleFunc("GET /memory/lint", s.handleMemoryLint)
	mux.HandleFunc("GET /memory/edit", s.handleMemoryEdit)
	mux.HandleFunc("POST /memory/save", s.handleMemorySave)
	mux.HandleFunc("POST /memory/delete", s.handleMemoryDelete)

	mux.HandleFunc("GET /sessions/{id}/delete", s.handleSessionDeleteConfirm)
	mux.HandleFunc("POST /sessions/{id}/delete", s.handleSessionDelete)

	mux.HandleFunc("GET /trash", s.handleTrashList)
	mux.HandleFunc("POST /trash/{id}/restore", s.handleTrashRestore)
	mux.HandleFunc("POST /trash/empty", s.handleTrashEmpty)

	mux.HandleFunc("POST /advisor/analyze", s.handleAdvisorAnalyze)
	mux.HandleFunc("POST /advisor/apply", s.handleAdvisorApply)

	return localOnly(http.NewCrossOriginProtection().Handler(mux))
}

// localOnly rejects requests not addressed to a loopback host name. Binding
// 127.0.0.1 doesn't stop DNS rebinding: a page on a domain re-pointed at
// 127.0.0.1 is same-origin to the browser, and only its Host header gives it away.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" {
			http.Error(w, "ccmanager only answers requests addressed to 127.0.0.1 or localhost", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// reindex re-runs the indexer; used by /refresh and at startup.
func (s *Server) reindex() (sessions.IndexResult, error) {
	return sessions.Index(s.store, s.cfg)
}
