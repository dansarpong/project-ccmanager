package advisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrClaudeUnavailable means the local claude CLI could not be used (not
// installed, errored, or not logged in). Callers should fall back to the
// always-available static lint and show a clear banner. The static lint never
// depends on claude.
var ErrClaudeUnavailable = errors.New("claude CLI unavailable")

// Analysis kinds the advisor asks claude for.
const (
	KindRewrite       = "rewrite"
	KindContradiction = "contradiction"
)

// resultEnvelope is the JSON shape `claude -p --output-format json` prints.
// Verified against v2.1.186: on success .result holds the assistant text; on
// failure .is_error is true and .result holds the error message (e.g.
// "Not logged in · Please run /login").
type resultEnvelope struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
}

// claudeRunner runs the CLI; swapped out in tests.
type claudeRunner interface {
	run(ctx context.Context, stdin string) (resultEnvelope, error)
}

// cliRunner shells out to the real claude binary with no tools granted, so it
// can only analyze the text passed on stdin and cannot touch the filesystem.
type cliRunner struct {
	bin          string
	systemPrompt string
	workDir      string
	managedDir   string
}

// cliArgs is the print-mode command line: no built-in or MCP tools, no saved
// transcript, no skills or slash commands, and no hooks.
//
// No --bare: it breaks subscription auth (you get "Not logged in" even when
// logged in), and no --setting-sources, which would drop apiKeyHelper and env
// from user settings. Without --bare, claude loads the hooks, .mcp.json and
// CLAUDE.md of its working directory, so command pins that to an empty one.
func cliArgs(systemPrompt string, strictMCP bool) []string {
	args := []string{
		"-p",
		"--output-format", "json",
		"--tools", "",
		"--allowed-tools", "",
		"--disallowed-tools", "mcp__*",
		"--no-session-persistence",
		"--disable-slash-commands",
		"--settings", `{"disableAllHooks":true}`,
	}
	// Claude Code exits at startup on --strict-mcp-config while a managed-mcp.json is deployed.
	if strictMCP {
		args = append(args, "--strict-mcp-config")
	}
	if systemPrompt != "" {
		args = append(args, "--append-system-prompt", systemPrompt)
	}
	return args
}

// command prepares the private work dir and builds the claude invocation.
func (c cliRunner) command(ctx context.Context) (*exec.Cmd, error) {
	if err := os.MkdirAll(c.workDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating advisor work dir: %w", err)
	}
	if err := os.Chmod(c.workDir, 0o700); err != nil {
		return nil, fmt.Errorf("securing advisor work dir: %w", err)
	}
	cmd := exec.CommandContext(ctx, c.bin, cliArgs(c.systemPrompt, !managedMCPDeployed(c.managedDir))...)
	cmd.Dir = c.workDir
	return cmd, nil
}

// managedMCPDeployed reports whether managedDir may hold a managed-mcp.json;
// anything but "does not exist" counts, so a stat error never adds the flag.
func managedMCPDeployed(managedDir string) bool {
	if managedDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(managedDir, "managed-mcp.json"))
	return !errors.Is(err, fs.ErrNotExist)
}

func (c cliRunner) run(ctx context.Context, stdin string) (resultEnvelope, error) {
	cmd, err := c.command(ctx)
	if err != nil {
		return resultEnvelope{}, err
	}
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	if err := cmd.Run(); err != nil {
		// A non-zero exit with parseable JSON still tells us is_error/result.
		if env, perr := parseEnvelope(out.Bytes()); perr == nil {
			return env, nil
		}
		return resultEnvelope{}, fmt.Errorf("%w: %v: %s", ErrClaudeUnavailable, err, strings.TrimSpace(errOut.String()))
	}
	env, err := parseEnvelope(out.Bytes())
	if err != nil {
		return resultEnvelope{}, fmt.Errorf("%w: %v", ErrClaudeUnavailable, err)
	}
	return env, nil
}

func parseEnvelope(b []byte) (resultEnvelope, error) {
	var env resultEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(b), &env); err != nil {
		return resultEnvelope{}, fmt.Errorf("parsing claude output: %w", err)
	}
	return env, nil
}

// callClaude runs the given runner and returns the assistant text, mapping an
// errored envelope (e.g. not logged in) to ErrClaudeUnavailable.
func callClaude(ctx context.Context, r claudeRunner, stdin string) (string, error) {
	env, err := r.run(ctx, stdin)
	if err != nil {
		return "", err
	}
	if env.IsError {
		return "", fmt.Errorf("%w: %s", ErrClaudeUnavailable, strings.TrimSpace(env.Result))
	}
	return env.Result, nil
}
