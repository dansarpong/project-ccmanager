package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// settings is the subset of a Claude Code settings file the engine reads.
type settings struct {
	ClaudeMdExcludes    []string `json:"claudeMdExcludes"`
	AutoMemoryDirectory string   `json:"autoMemoryDirectory"`
}

// ManagedDir returns the directory Claude Code reads managed-policy files
// (CLAUDE.md and managed-settings.json) from on goos, a runtime.GOOS value.
func ManagedDir(goos string) string {
	switch goos {
	case "darwin":
		return "/Library/Application Support/ClaudeCode"
	case "windows":
		return `C:\Program Files\ClaudeCode`
	default:
		return "/etc/claude-code"
	}
}

// loadSettings reads every settings layer that applies to workdir, highest
// precedence first: managed, local, project, user. Missing or malformed files
// are skipped.
func loadSettings(in Input, workdir string, r repo) []settings {
	var out []settings
	for _, path := range settingsPaths(in, workdir, r) {
		if s, ok := readSettings(path); ok {
			out = append(out, s)
		}
	}
	return out
}

// settingsPaths lists the settings files in loadSettings' order. Local settings
// live at the main checkout's root, except outside git, on Windows or when that
// root is the home dir; the copy that versions before 2.1.211 kept in the
// working dir is still read, below the root's.
func settingsPaths(in Input, workdir string, r repo) []string {
	var paths []string
	if in.ManagedSettingsPath != "" {
		paths = append(paths, in.ManagedSettingsPath)
	}
	legacy := filepath.Join(workdir, ".claude", "settings.local.json")
	if r.inGit && in.goos() != "windows" && r.main != in.home() {
		if root := filepath.Join(r.main, ".claude", "settings.local.json"); root != legacy {
			paths = append(paths, root)
		}
	}
	paths = append(paths, legacy, filepath.Join(workdir, ".claude", "settings.json"))
	if in.ConfigRoot != "" {
		paths = append(paths, filepath.Join(in.ConfigRoot, "settings.json"))
	}
	return paths
}

// mergeExcludes merges claudeMdExcludes across layers (arrays merge, per the
// docs).
func mergeExcludes(layers []settings) []string {
	var out []string
	for _, s := range layers {
		out = append(out, s.ClaudeMdExcludes...)
	}
	return out
}

// autoMemoryDir returns the autoMemoryDirectory override from the
// highest-precedence layer that sets one, or "" if none does or the value is
// neither absolute nor ~/-relative. A leading ~/ is expanded against home.
func autoMemoryDir(layers []settings, home string) string {
	for _, s := range layers {
		dir := s.AutoMemoryDirectory
		if dir == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(dir, "~/"); ok && home != "" {
			dir = filepath.Join(home, rest)
		}
		if !filepath.IsAbs(dir) {
			return ""
		}
		return filepath.Clean(dir)
	}
	return ""
}

func readSettings(path string) (settings, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return settings{}, false
	}
	var s settings
	if err := json.Unmarshal(data, &s); err != nil {
		return settings{}, false
	}
	return s, true
}

func expandHome(p string) string {
	if p == "~" || len(p) >= 2 && p[:2] == "~/" {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
