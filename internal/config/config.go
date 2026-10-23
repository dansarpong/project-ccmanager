// Package config resolves where Claude Code keeps its data on this machine
// and where ccmanager keeps its own state.
package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Config holds the resolved filesystem locations and the server port.
type Config struct {
	ConfigRoot  string // CLAUDE_CONFIG_DIR or ~/.claude
	ProjectsDir string // ConfigRoot/projects — where transcripts and auto-memory live
	DataDir     string // ConfigRoot/ccmanager — this app's own state
	TrashDir    string // DataDir/trash
	DBPath      string // DataDir/index.db
	ClaudeBin   string // path to the claude CLI ("" if not found on PATH)
	Port        int
}

// Resolve determines all paths. It does not create any directories.
func Resolve(port int) (Config, error) {
	root := os.Getenv("CLAUDE_CONFIG_DIR")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Config{}, fmt.Errorf("locating home dir: %w", err)
		}
		root = filepath.Join(home, ".claude")
	}

	data := filepath.Join(root, "ccmanager")
	cfg := Config{
		ConfigRoot:  root,
		ProjectsDir: filepath.Join(root, "projects"),
		DataDir:     data,
		TrashDir:    filepath.Join(data, "trash"),
		DBPath:      filepath.Join(data, "index.db"),
		ClaudeBin:   findClaude(),
		Port:        port,
	}
	return cfg, nil
}

// findClaude locates the claude CLI on PATH. It returns "" if not found, in
// which case the advisor's LLM analysis is unavailable (the static lint still
// works).
func findClaude() string {
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	return ""
}

// EnsureDataDirs creates the app's own directories (DataDir and TrashDir) and
// makes them private to the user: they hold the text of every transcript.
func (c Config) EnsureDataDirs() error {
	for _, dir := range []string{c.DataDir, c.TrashDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("creating data dirs: %w", err)
		}
		// MkdirAll leaves an existing directory's mode alone.
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("restricting %s: %w", dir, err)
		}
	}
	return nil
}
