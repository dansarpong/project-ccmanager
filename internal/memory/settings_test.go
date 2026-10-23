package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// settingsTree is a repo with a subdirectory working dir and one settings file
// per layer, all initially empty.
type settingsTree struct {
	in                                             Input
	repo, workdir                                  string
	managed, rootLocal, legacyLocal, project, user string
}

func newSettingsTree(t *testing.T) settingsTree {
	t.Helper()
	root := t.TempDir()
	st := settingsTree{repo: filepath.Join(root, "repo")}
	st.workdir = filepath.Join(st.repo, "sub")
	st.in = Input{
		ConfigRoot:          filepath.Join(root, "config"),
		ProjectsDir:         filepath.Join(root, "config", "projects"),
		ManagedPath:         filepath.Join(root, "managed", "CLAUDE.md"),
		ManagedSettingsPath: filepath.Join(root, "managed", "managed-settings.json"),
		HomeDir:             filepath.Join(root, "home"),
		GOOS:                "linux",
	}
	st.managed = st.in.ManagedSettingsPath
	st.rootLocal = filepath.Join(st.repo, ".claude", "settings.local.json")
	st.legacyLocal = filepath.Join(st.workdir, ".claude", "settings.local.json")
	st.project = filepath.Join(st.workdir, ".claude", "settings.json")
	st.user = filepath.Join(st.in.ConfigRoot, "settings.json")

	mkdirs(t, filepath.Join(st.repo, ".git"))
	writeFile(t, st.in.ManagedPath, "# Managed\n")
	writeFile(t, filepath.Join(st.in.ConfigRoot, "CLAUDE.md"), "# User\n")
	writeFile(t, filepath.Join(st.workdir, "CLAUDE.md"), "# Sub\n")
	return st
}

func writeSettings(t *testing.T, path string, s settings) {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))
}

func excludedLayers(t *testing.T, in Input, workdir string) map[string]bool {
	t.Helper()
	stack, err := Resolve(in, workdir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, s := range stack.Sources {
		out[s.Layer] = s.Excluded
	}
	return out
}

func TestManagedCLAUDEmdCannotBeExcluded(t *testing.T) {
	st := newSettingsTree(t)
	writeSettings(t, st.managed, settings{ClaudeMdExcludes: []string{"**/CLAUDE.md"}})

	got := excludedLayers(t, st.in, st.workdir)
	if got[LayerManaged] {
		t.Error("managed CLAUDE.md was excluded; claudeMdExcludes must not apply to it")
	}
	if !got[LayerUser] || !got[LayerProject] {
		t.Errorf("managed-settings excludes should apply to other files: %v", got)
	}
}

func TestExcludesMergeEveryLayer(t *testing.T) {
	for _, layer := range []string{"managed", "rootLocal", "legacyLocal", "project", "user"} {
		st := newSettingsTree(t)
		path := map[string]string{
			"managed": st.managed, "rootLocal": st.rootLocal, "legacyLocal": st.legacyLocal,
			"project": st.project, "user": st.user,
		}[layer]
		writeSettings(t, path, settings{ClaudeMdExcludes: []string{filepath.Join(st.workdir, "CLAUDE.md")}})
		if !excludedLayers(t, st.in, st.workdir)[LayerProject] {
			t.Errorf("%s settings (%s): exclude not applied", layer, path)
		}
	}
}

func TestRootLocalSettingsApplyFromSubdirectory(t *testing.T) {
	exclude := settings{ClaudeMdExcludes: []string{"**/sub/CLAUDE.md"}}
	cases := []struct {
		name    string
		mutate  func(st *settingsTree)
		applies bool
	}{
		{"subdirectory of a repo", func(*settingsTree) {}, true},
		{"repo root is the home dir", func(st *settingsTree) { st.in.HomeDir = st.repo }, false},
		{"windows", func(st *settingsTree) { st.in.GOOS = "windows" }, false},
		{"outside git", func(st *settingsTree) { os.RemoveAll(filepath.Join(st.repo, ".git")) }, false},
	}
	for _, c := range cases {
		st := newSettingsTree(t)
		writeSettings(t, st.rootLocal, exclude)
		c.mutate(&st)
		if got := excludedLayers(t, st.in, st.workdir)[LayerProject]; got != c.applies {
			t.Errorf("%s: root settings.local.json applied = %v, want %v", c.name, got, c.applies)
		}
	}
}

func TestProjectSettingsAreNotReadFromParents(t *testing.T) {
	st := newSettingsTree(t)
	writeSettings(t, filepath.Join(st.repo, ".claude", "settings.json"), settings{ClaudeMdExcludes: []string{"**/CLAUDE.md"}})
	if excludedLayers(t, st.in, st.workdir)[LayerProject] {
		t.Error("a parent directory's .claude/settings.json was read; only the working dir's applies")
	}
}

func TestAutoMemoryDirectoryPrecedence(t *testing.T) {
	st := newSettingsTree(t)
	layers := []struct{ name, path string }{
		{"managed", st.managed},
		{"rootLocal", st.rootLocal},
		{"legacyLocal", st.legacyLocal},
		{"project", st.project},
		{"user", st.user},
	}
	dirOf := func(name string) string { return filepath.Join(st.in.HomeDir, "mem-"+name) }
	for _, l := range layers {
		mkdirs(t, dirOf(l.name))
		writeSettings(t, l.path, settings{AutoMemoryDirectory: "~/mem-" + l.name})
	}
	defaultDir := filepath.Join(st.in.ProjectsDir, EncodeProjectDir(st.repo), "memory")
	mkdirs(t, defaultDir)

	for _, l := range layers {
		stack, _ := Resolve(st.in, st.workdir)
		if stack.AutoMemory == nil || stack.AutoMemory.Dir != dirOf(l.name) {
			t.Errorf("with %s the highest layer set: auto memory = %+v, want %s", l.name, stack.AutoMemory, dirOf(l.name))
		}
		if err := os.Remove(l.path); err != nil {
			t.Fatal(err)
		}
	}
	stack, _ := Resolve(st.in, st.workdir)
	if stack.AutoMemory == nil || stack.AutoMemory.Dir != defaultDir {
		t.Errorf("with no override: auto memory = %+v, want %s", stack.AutoMemory, defaultDir)
	}

	writeSettings(t, st.user, settings{AutoMemoryDirectory: "relative/mem"})
	stack, _ = Resolve(st.in, st.workdir)
	if stack.AutoMemory == nil || stack.AutoMemory.Dir != defaultDir {
		t.Errorf("a relative override must be ignored: auto memory = %+v, want %s", stack.AutoMemory, defaultDir)
	}
}

func TestManagedDir(t *testing.T) {
	cases := map[string]string{
		"darwin":  "/Library/Application Support/ClaudeCode",
		"linux":   "/etc/claude-code",
		"windows": `C:\Program Files\ClaudeCode`,
	}
	for goos, want := range cases {
		if got := ManagedDir(goos); got != want {
			t.Errorf("ManagedDir(%q) = %q, want %q", goos, got, want)
		}
	}
}
