package advisor

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCLIArgsIsolateTheRun(t *testing.T) {
	for _, strict := range []bool{false, true} {
		args := cliArgs("be terse", strict)
		pairs := [][2]string{
			{"--output-format", "json"},
			{"--tools", ""},
			{"--allowed-tools", ""},
			{"--disallowed-tools", "mcp__*"},
			{"--settings", `{"disableAllHooks":true}`},
			{"--append-system-prompt", "be terse"},
		}
		for _, p := range pairs {
			i := slices.Index(args, p[0])
			if i < 0 || i+1 >= len(args) || args[i+1] != p[1] {
				t.Errorf("strict=%v: want %s %q in %q", strict, p[0], p[1], args)
			}
		}
		for _, flag := range []string{"-p", "--no-session-persistence", "--disable-slash-commands"} {
			if !slices.Contains(args, flag) {
				t.Errorf("strict=%v: missing %s in %q", strict, flag, args)
			}
		}
		for _, flag := range []string{"--bare", "--setting-sources"} {
			if slices.Contains(args, flag) {
				t.Errorf("strict=%v: %s must never be passed: %q", strict, flag, args)
			}
		}
		if got := slices.Contains(args, "--strict-mcp-config"); got != strict {
			t.Errorf("strict=%v: --strict-mcp-config present = %v", strict, got)
		}
	}
	if slices.Contains(cliArgs("", true), "--append-system-prompt") {
		t.Error("an empty system prompt should not be passed")
	}
}

func TestCommandRunsInPrivateWorkDir(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, "managed")
	if err := os.MkdirAll(managed, 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "ccmanager", "advisor")
	c := cliRunner{bin: "claude", workDir: work, managedDir: managed}

	cmd, err := c.command(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Dir != work {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, work)
	}
	info, err := os.Stat(work)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("work dir mode = %o, want 700", perm)
	}
	if !slices.Contains(cmd.Args, "--strict-mcp-config") {
		t.Errorf("no managed-mcp.json: want --strict-mcp-config in %q", cmd.Args)
	}

	// An existing, looser work dir is tightened; a deployed managed-mcp.json
	// drops the flag, since claude would exit at startup with it.
	if err := os.Chmod(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managed, "managed-mcp.json"), []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd, err = c.command(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(cmd.Args, "--strict-mcp-config") {
		t.Errorf("managed-mcp.json deployed: --strict-mcp-config must be left out: %q", cmd.Args)
	}
	if info, err = os.Stat(work); err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("existing work dir mode = %o, want 700", perm)
	}
}
