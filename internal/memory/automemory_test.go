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
