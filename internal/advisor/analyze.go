package advisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/dansarpong/ccmanager/internal/memory"
)

// RewriteSuggestion proposes a concrete replacement for a weak instruction.
type RewriteSuggestion struct {
	File       string `json:"-"`
	Original   string `json:"original"`
	Suggestion string `json:"suggestion"`
	Reason     string `json:"reason"`
}

// Contradiction reports two memory statements that conflict.
type Contradiction struct {
	FileA       string `json:"fileA"`
	FileB       string `json:"fileB"`
	ClaimA      string `json:"claimA"`
	ClaimB      string `json:"claimB"`
	Explanation string `json:"explanation"`
}

// Cache abstracts the advisor result cache (implemented by store.Store).
type Cache interface {
	GetAdvisorCache(inputHash, kind string) (string, bool, error)
	PutAdvisorCache(scopeKey, kind, inputHash, resultJSON, createdAt string) error
}

// Advisor runs LLM-backed analysis via the local claude CLI. It is propose-only
// and never writes to disk.
type Advisor struct {
	runner claudeRunner
	cache  Cache
	clock  func() time.Time
}

// CLI says how the Advisor runs claude.
type CLI struct {
	Bin        string // the claude binary
	WorkDir    string // private, empty working directory, created on first use
	ManagedDir string // Claude Code's managed-settings directory; "" if none
}

// NewAdvisor returns an Advisor that runs claude as cli describes. If cli.Bin
// is empty, the runner is still constructed; calls will surface
// ErrClaudeUnavailable when the binary cannot be executed.
func NewAdvisor(cli CLI, cache Cache) *Advisor {
	return &Advisor{
		runner: cliRunner{bin: cli.Bin, workDir: cli.WorkDir, managedDir: cli.ManagedDir},
		cache:  cache,
		clock:  time.Now,
	}
}

// Available reports whether a claude binary is on PATH (a cheap pre-check so the
// UI can show the right affordance without spending a full analysis call).
func Available(bin string) bool {
	if bin == "" {
		return false
	}
	_, err := exec.LookPath(bin)
	return err == nil
}

// RewriteFile asks claude to propose rewrites of weak instructions in one
// file's content. Results are cached by content hash.
func (a *Advisor) RewriteFile(ctx context.Context, file, content string) ([]RewriteSuggestion, error) {
	prompt := rewritePrompt + "\n\n---\n" + content
	raw, err := a.invoke(ctx, KindRewrite, file, prompt)
	if err != nil {
		return nil, err
	}
	var out []RewriteSuggestion
	if err := json.Unmarshal([]byte(extractJSON(raw)), &out); err != nil {
		return nil, fmt.Errorf("parsing rewrite suggestions: %w", err)
	}
	for i := range out {
		out[i].File = file
	}
	return out, nil
}

// Contradictions asks claude to find conflicting instructions across the whole
// resolved stack. The stack is flattened into a labeled document.
func (a *Advisor) Contradictions(ctx context.Context, stack *memory.ResolvedStack) ([]Contradiction, error) {
	return a.contradictionsFromDoc(ctx, stack.Workdir, flattenStack(stack))
}

// contradictionsFromDoc runs contradiction analysis over an already-flattened
// document (split out so it is testable without building a full stack).
func (a *Advisor) contradictionsFromDoc(ctx context.Context, scopeKey, doc string) ([]Contradiction, error) {
	prompt := contradictionPrompt + "\n\n---\n" + doc
	raw, err := a.invoke(ctx, KindContradiction, scopeKey, prompt)
	if err != nil {
		return nil, err
	}
	var out []Contradiction
	if err := json.Unmarshal([]byte(extractJSON(raw)), &out); err != nil {
		return nil, fmt.Errorf("parsing contradictions: %w", err)
	}
	return out, nil
}

// invoke returns the claude result for a prompt, using the cache when possible.
func (a *Advisor) invoke(ctx context.Context, kind, scopeKey, prompt string) (string, error) {
	hash := hashInput(prompt)
	if a.cache != nil {
		if cached, ok, err := a.cache.GetAdvisorCache(hash, kind); err != nil {
			return "", err
		} else if ok {
			return cached, nil
		}
	}
	raw, err := callClaude(ctx, a.runner, prompt)
	if err != nil {
		return "", err
	}
	if a.cache != nil {
		_ = a.cache.PutAdvisorCache(scopeKey, kind, hash, raw, a.clock().UTC().Format(time.RFC3339))
	}
	return raw, nil
}

func hashInput(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// extractJSON pulls the first JSON array out of a model response, tolerating
// stray prose or a ```json fence around it.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "["); i >= 0 {
		if j := strings.LastIndex(s, "]"); j >= i {
			return s[i : j+1]
		}
	}
	return "[]"
}

func flattenStack(stack *memory.ResolvedStack) string {
	var b strings.Builder
	for _, src := range stack.Sources {
		fmt.Fprintf(&b, "=== FILE: %s ===\n%s\n\n", src.Path, src.Raw)
	}
	return b.String()
}

const rewritePrompt = `You are reviewing a Claude Code CLAUDE.md memory file for vague instructions.
Find instructions that are too vague to follow consistently (e.g. "format properly",
"keep it clean", "use good names") and propose a concrete, verifiable rewrite for each.
Respond with ONLY a JSON array, no prose, of objects:
[{"original": "<the vague line, verbatim>", "suggestion": "<a concrete replacement>", "reason": "<why>"}]
If there is nothing to improve, respond with [].`

const contradictionPrompt = `You are reviewing a stack of Claude Code memory files for instructions that
contradict each other. Each file is delimited by "=== FILE: <path> ===".
Find pairs of statements (in the same or different files) that give conflicting guidance.
Respond with ONLY a JSON array, no prose, of objects:
[{"fileA":"<path>","fileB":"<path>","claimA":"<statement>","claimB":"<conflicting statement>","explanation":"<why they conflict>"}]
If there are no contradictions, respond with [].`
