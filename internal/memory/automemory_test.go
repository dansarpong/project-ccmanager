package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	mkdirs(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAutoMemoryWorktreeUsesMainCheckout(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "main")
	wt := filepath.Join(root, "wt")
	gitdir := filepath.Join(main, ".git", "worktrees", "wt")
	writeFile(t, filepath.Join(gitdir, "commondir"), "../..\n")
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+gitdir+"\n")
	writeFile(t, filepath.Join(main, "shared.md"), "shared\n")
	writeFile(t, filepath.Join(wt, "notes.md"), "notes\n")
	writeFile(t, filepath.Join(wt, "CLAUDE.md"), "@notes.md\n@../main/shared.md\n")
	mkdirs(t, filepath.Join(wt, "sub"))

	// A submodule's .git file has no commondir, so it keeps its own root.
	sm := filepath.Join(root, "sm")
	mkdirs(t, filepath.Join(main, ".git", "modules", "sm"))
	writeFile(t, filepath.Join(sm, ".git"), "gitdir: ../main/.git/modules/sm\n")

	projects := filepath.Join(root, "projects")
	for _, dir := range []string{main, wt, sm} {
		writeFile(t, filepath.Join(projects, EncodeProjectDir(dir), "memory", "MEMORY.md"), "# "+filepath.Base(dir)+"\n")
	}
	in := Input{ConfigRoot: filepath.Join(root, "config"), ProjectsDir: projects}

	cases := []struct{ workdir, wantRoot string }{
		{filepath.Join(wt, "sub"), main},
		{wt, main},
		{sm, sm},
		{main, main},
	}
	for _, c := range cases {
		stack, err := Resolve(in, c.workdir)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(projects, EncodeProjectDir(c.wantRoot), "memory")
		if stack.AutoMemory == nil || stack.AutoMemory.Dir != want {
			t.Errorf("%s: auto memory = %+v, want dir %s", c.workdir, stack.AutoMemory, want)
		}
	}

	// Imports are still judged against the worktree's own root.
	stack, _ := Resolve(in, wt)
	external := map[string]bool{}
	for _, e := range stack.ImportEdges {
		external[filepath.Base(e.To)] = e.External
	}
	if external["notes.md"] || !external["shared.md"] {
		t.Errorf("external flags = %v, want notes.md inside and shared.md outside the worktree", external)
	}
}

func TestAutoMemoryLongProjectName(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, strings.Repeat("deeply_nested.segment-", 4), strings.Repeat("another.long_one-", 5), "project")
	mkdirs(t, filepath.Join(workdir, ".git"))
	projects := filepath.Join(root, "projects")
	in := Input{ConfigRoot: filepath.Join(root, "config"), ProjectsDir: projects}

	encoded := EncodeProjectDir(workdir)
	if len(encoded) <= maxProjectDirName {
		t.Fatalf("fixture path encodes to %d chars; it must exceed %d", len(encoded), maxProjectDirName)
	}
	prefix := encoded[:maxProjectDirName]
	mkdirs(t, filepath.Join(projects, prefix, "memory")) // the exact prefix is some other, shorter path
	writeFile(t, filepath.Join(projects, prefix+"-1a2b3c", "memory", "MEMORY.md"), "# Long\n")

	stack, err := Resolve(in, workdir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(projects, prefix+"-1a2b3c", "memory")
	if stack.AutoMemory == nil || stack.AutoMemory.Dir != want {
		t.Fatalf("auto memory = %+v, want dir %s", stack.AutoMemory, want)
	}

	mkdirs(t, filepath.Join(projects, prefix+"-4d5e6f", "memory"))
	stack, _ = Resolve(in, workdir)
	if stack.AutoMemory != nil {
		t.Errorf("two folders share the prefix, so auto memory is ambiguous; got %s", stack.AutoMemory.Dir)
	}
}
