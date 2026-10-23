package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupTree copies the testdata fixture tree into a temp directory and returns
// the temp paths the tests need. Copying lets us add the .git marker and the
// encoded auto-memory directory (whose name depends on the absolute temp path)
// without polluting the committed fixtures.
func setupTree(t *testing.T) (in Input, workdir, projectRoot string) {
	t.Helper()
	root := t.TempDir()

	copyTree(t, "testdata", root)

	configRoot := filepath.Join(root, "configroot")
	projectRoot = filepath.Join(root, "project")
	workdir = filepath.Join(projectRoot, "a", "b", "workdir")

	// Mark the project root as a git repo so findRepo stops there.
	if err := os.MkdirAll(filepath.Join(projectRoot, ".git"), 0o755); err != nil {
		t.Fatalf("creating .git marker: %v", err)
	}

	// Place auto-memory at projects/<encoded(projectRoot)>/memory so the
	// encoded-dir lookup resolves with a real absolute path.
	projectsDir := filepath.Join(configRoot, "projects")
	memDir := filepath.Join(projectsDir, EncodeProjectDir(projectRoot), "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatalf("creating auto-memory dir: %v", err)
	}
	copyTree(t, filepath.Join("testdata", "automemory"), memDir)

	in = Input{
		ConfigRoot:  configRoot,
		ProjectsDir: projectsDir,
		ManagedPath: filepath.Join(configRoot, "managed", "CLAUDE.md"),
	}
	return in, workdir, projectRoot
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copying %s -> %s: %v", src, dst, err)
	}
}

func TestResolveOrdering(t *testing.T) {
	in, workdir, _ := setupTree(t)
	stack, err := Resolve(in, workdir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Expected layers in load order: managed, user, user-rule, then the
	// project walk from root down to workdir.
	wantLayers := []string{
		LayerManaged,
		LayerUser,
		LayerUserRule,
		LayerProject,      // project/CLAUDE.md
		LayerProject,      // project/a/CLAUDE.md
		LayerProject,      // workdir/CLAUDE.md
		LayerProjectLocal, // workdir/CLAUDE.local.md
		LayerProjectRule,  // workdir/.claude/rules/scoped.md
	}
	if len(stack.Sources) != len(wantLayers) {
		t.Fatalf("got %d sources, want %d:\n%s", len(stack.Sources), len(wantLayers), dumpSources(stack))
	}
	for i, want := range wantLayers {
		if stack.Sources[i].Layer != want {
			t.Errorf("source %d: layer = %q, want %q", i, stack.Sources[i].Layer, want)
		}
		if stack.Sources[i].Order != i {
			t.Errorf("source %d: Order = %d, want %d", i, stack.Sources[i].Order, i)
		}
	}

	// CLAUDE.local.md must come after CLAUDE.md at the workdir level.
	localIdx := indexOfPathSuffix(stack, "workdir/CLAUDE.local.md")
	mdIdx := indexOfPathSuffix(stack, "workdir/CLAUDE.md")
	if localIdx <= mdIdx {
		t.Errorf("CLAUDE.local.md (idx %d) should come after CLAUDE.md (idx %d)", localIdx, mdIdx)
	}
}

func TestResolveConditionalRule(t *testing.T) {
	in, workdir, _ := setupTree(t)
	stack, _ := Resolve(in, workdir)

	idx := indexOfPathSuffix(stack, "scoped.md")
	if idx < 0 {
		t.Fatal("scoped rule not found")
	}
	s := stack.Sources[idx]
	if !s.Conditional {
		t.Error("scoped rule should be Conditional (has paths: frontmatter)")
	}
	wantPaths := []string{"src/**/*.ts", "lib/**/*.ts"}
	if strings.Join(s.Paths, ",") != strings.Join(wantPaths, ",") {
		t.Errorf("paths = %v, want %v", s.Paths, wantPaths)
	}

	// The user rule has no paths: and must be unconditional.
	uidx := indexOfPathSuffix(stack, "userrule.md")
	if stack.Sources[uidx].Conditional {
		t.Error("user rule should not be Conditional")
	}
}

func TestResolveImportChainDepthCut(t *testing.T) {
	in, workdir, _ := setupTree(t)
	stack, _ := Resolve(in, workdir)

	exp := workdirExpanded(t, stack)
	for _, hop := range []string{"IMP_A hop1", "IMP_B hop2", "IMP_C hop3", "IMP_D hop4"} {
		if !strings.Contains(exp, hop) {
			t.Errorf("expansion missing %q (should inline 4 hops)\n%s", hop, exp)
		}
	}
	if strings.Contains(exp, "IMP_E hop5") {
		t.Errorf("expansion included hop 5; depth cap of %d not enforced\n%s", MaxImportDepth, exp)
	}
}

func TestResolveCircularImport(t *testing.T) {
	in, workdir, _ := setupTree(t)
	stack, _ := Resolve(in, workdir)
	exp := workdirExpanded(t, stack)

	if !strings.Contains(exp, "CYC_X content") {
		t.Errorf("cyc_x should be inlined once\n%s", exp)
	}
	if !strings.Contains(exp, "CYC_Y content") {
		t.Errorf("cyc_y should be inlined once\n%s", exp)
	}
	// The cycle back to X must be cut, not expanded again.
	if strings.Count(exp, "CYC_X content") != 1 {
		t.Errorf("cyc_x expanded %d times; cycle guard failed\n%s", strings.Count(exp, "CYC_X content"), exp)
	}
	if !strings.Contains(exp, "circular") {
		t.Errorf("expected a circular-import marker\n%s", exp)
	}
}

func TestResolveExternalImportFlagged(t *testing.T) {
	in, workdir, _ := setupTree(t)
	stack, _ := Resolve(in, workdir)

	var found bool
	for _, e := range stack.ImportEdges {
		if e.External {
			found = true
		}
	}
	if !found {
		t.Errorf("expected at least one external import edge\nedges: %+v", stack.ImportEdges)
	}
}

func TestResolveCodeBlockAndBacktickSkipped(t *testing.T) {
	in, workdir, _ := setupTree(t)
	stack, _ := Resolve(in, workdir)
	exp := workdirExpanded(t, stack)

	// The fenced block mentions imp_a but must not cause a second expansion,
	// and the literal fenced text must survive verbatim.
	if !strings.Contains(exp, "but it must NOT import") {
		t.Errorf("fenced code block text was altered\n%s", exp)
	}
	// imp_a is imported once legitimately (outside the fence); the fence
	// mention must not add another inline copy.
	if got := strings.Count(exp, "IMP_A hop1"); got != 1 {
		t.Errorf("IMP_A expanded %d times; code fence import should be ignored\n%s", got, exp)
	}
	// The backticked `@README` must remain literal text.
	if !strings.Contains(exp, "`@README`") {
		t.Errorf("backticked @README should stay literal\n%s", exp)
	}

	// A comment inside the code fence is preserved; the maintainer comment
	// outside it is stripped.
	if strings.Contains(exp, "this comment must be stripped") {
		t.Errorf("block comment outside code was not stripped\n%s", exp)
	}
	if !strings.Contains(exp, "this comment is inside a code fence and must be preserved") {
		t.Errorf("comment inside code fence should be preserved\n%s", exp)
	}
}

func TestResolveAutoMemory(t *testing.T) {
	in, workdir, _ := setupTree(t)
	stack, _ := Resolve(in, workdir)

	if stack.AutoMemory == nil {
		t.Fatal("expected auto-memory to be found")
	}
	if !strings.Contains(stack.AutoMemory.Head, "Topic one") {
		t.Errorf("MEMORY.md head missing expected content: %q", stack.AutoMemory.Head)
	}
	if len(stack.AutoMemory.TopicFiles) == 0 {
		t.Error("expected topic files to be listed as on-demand")
	}
}

// helpers

func workdirExpanded(t *testing.T, stack *ResolvedStack) string {
	t.Helper()
	idx := indexOfPathSuffix(stack, "workdir/CLAUDE.md")
	if idx < 0 {
		t.Fatal("workdir CLAUDE.md not found in stack")
	}
	return stack.Sources[idx].Expanded
}

func indexOfPathSuffix(stack *ResolvedStack, suffix string) int {
	suffix = filepath.FromSlash(suffix)
	for i, s := range stack.Sources {
		if strings.HasSuffix(s.Path, suffix) {
			return i
		}
	}
	return -1
}

func dumpSources(stack *ResolvedStack) string {
	var b strings.Builder
	for i, s := range stack.Sources {
		b.WriteString(s.Layer + "  " + s.Path)
		if i < len(stack.Sources)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
