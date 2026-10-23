package memory

import (
	"strings"
	"testing"
)

func TestEncodeProjectDir(t *testing.T) {
	cases := map[string]string{
		"/home/dansarpong/ARMS/arms-infrastructure-as-code":   "-home-dansarpong-ARMS-arms-infrastructure-as-code",
		"/home/dansarpong/Personal/others/claude-tests/uigen": "-home-dansarpong-Personal-others-claude-tests-uigen",
		"/home/dansarpong": "-home-dansarpong",
		"/home/u/Personal/others/boafo/.claude/worktrees/bold-noyce-8797c8": "-home-u-Personal-others-boafo--claude-worktrees-bold-noyce-8797c8",
		"/home/u/my_proj.v2/": "-home-u-my-proj-v2",
		"/home/u/café @ work": "-home-u-caf----work",
		"/home/u/🚀app":        "-home-u---app",
	}
	for in, want := range cases {
		if got := EncodeProjectDir(in); got != want {
			t.Errorf("EncodeProjectDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHeadOfLineLimit(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 300; i++ {
		b.WriteString("line\n")
	}
	head, truncated := headOf(b.String())
	if !truncated {
		t.Error("expected truncation past 200 lines")
	}
	if got := strings.Count(head, "\n"); got != autoMemoryHeadLines {
		t.Errorf("head has %d lines, want %d", got, autoMemoryHeadLines)
	}
}

func TestHeadOfByteLimit(t *testing.T) {
	// One very long line beyond the byte cap, no newlines.
	big := strings.Repeat("x", autoMemoryHeadBytes+500)
	head, truncated := headOf(big)
	if !truncated {
		t.Error("expected truncation past the byte cap")
	}
	if len(head) != autoMemoryHeadBytes {
		t.Errorf("head is %d bytes, want %d", len(head), autoMemoryHeadBytes)
	}
}

func TestHeadOfSmallContent(t *testing.T) {
	head, truncated := headOf("just a little\nmemory\n")
	if truncated {
		t.Error("small content should not be truncated")
	}
	if head != "just a little\nmemory\n" {
		t.Errorf("head = %q", head)
	}
}

func TestParsePathsFrontmatterBlock(t *testing.T) {
	raw := "---\npaths:\n  - \"src/**/*.ts\"\n  - 'lib/**/*.ts'\n---\nbody"
	paths, ok := parsePathsFrontmatter(raw)
	if !ok {
		t.Fatal("expected paths frontmatter")
	}
	if strings.Join(paths, ",") != "src/**/*.ts,lib/**/*.ts" {
		t.Errorf("paths = %v", paths)
	}
}

func TestParsePathsFrontmatterInline(t *testing.T) {
	raw := "---\npaths: [\"a/**\", \"b.md\"]\n---\n"
	paths, ok := parsePathsFrontmatter(raw)
	if !ok || strings.Join(paths, ",") != "a/**,b.md" {
		t.Errorf("inline paths = %v ok=%v", paths, ok)
	}
}

func TestParsePathsFrontmatterNone(t *testing.T) {
	if _, ok := parsePathsFrontmatter("# no frontmatter\nbody"); ok {
		t.Error("did not expect frontmatter")
	}
	if _, ok := parsePathsFrontmatter("---\ntitle: x\n---\n"); ok {
		t.Error("frontmatter without paths: should report ok=false")
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"**/CLAUDE.md", "/a/b/CLAUDE.md", true},
		{"/home/*/CLAUDE.md", "/home/x/CLAUDE.md", true},
		{"/home/*/CLAUDE.md", "/home/x/y/CLAUDE.md", false},
		{"src/**/*.ts", "src/a/b/c.ts", true},
		{"src/**/*.ts", "lib/a.ts", false},
		{"**/rules/**", "/p/.claude/rules/x.md", true},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.path); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestStripBlockComments(t *testing.T) {
	in := "keep\n<!-- drop me -->\nalso keep\ntext <!-- inline --> more\n"
	out := stripBlockComments(in)
	if strings.Contains(out, "drop me") || strings.Contains(out, "inline") {
		t.Errorf("comments not stripped: %q", out)
	}
	if !strings.Contains(out, "keep") || !strings.Contains(out, "also keep") {
		t.Errorf("content lost: %q", out)
	}
	if !strings.Contains(out, "text  more") && !strings.Contains(out, "text more") {
		t.Errorf("inline-comment line mangled: %q", out)
	}
}

func TestStripBlockCommentsPreservesFence(t *testing.T) {
	in := "before\n```\n<!-- keep inside fence -->\n```\nafter\n"
	out := stripBlockComments(in)
	if !strings.Contains(out, "keep inside fence") {
		t.Errorf("comment inside fence should be preserved: %q", out)
	}
}

func TestStripBlockCommentsMultiline(t *testing.T) {
	in := "a\n<!-- start\nstill comment\nend -->\nb\n"
	out := stripBlockComments(in)
	if strings.Contains(out, "still comment") {
		t.Errorf("multiline comment body leaked: %q", out)
	}
	if !strings.Contains(out, "a") || !strings.Contains(out, "b") {
		t.Errorf("surrounding content lost: %q", out)
	}
}
