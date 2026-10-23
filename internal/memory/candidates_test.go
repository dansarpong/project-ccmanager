package memory

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestCandidatesMatchResolveOrder(t *testing.T) {
	in, workdir, _ := setupTree(t)
	stack, err := Resolve(in, workdir)
	if err != nil {
		t.Fatal(err)
	}
	cands := Candidates(in, workdir)
	if len(cands) != len(stack.Sources) {
		t.Fatalf("%d candidates, %d sources:\n%s", len(cands), len(stack.Sources), dumpSources(stack))
	}
	for i, c := range cands {
		if s := stack.Sources[i]; c.Path != s.Path || c.Layer != s.Layer {
			t.Errorf("position %d: candidate %s (%s), source %s (%s)", i, c.Path, c.Layer, s.Path, s.Layer)
		}
	}
}

func TestCandidatesAcrossWorkdirs(t *testing.T) {
	in, workdir, projectRoot := setupTree(t)
	single := Candidates(in, workdir)

	if got := CandidatesAcross(in, []string{workdir, workdir}); !reflect.DeepEqual(got, single) {
		t.Errorf("a repeated workdir changed the result:\n got %v\nwant %v", got, single)
	}

	// An ancestor of workdir adds nothing; a sibling adds only its own files,
	// after everything already found.
	if got := CandidatesAcross(in, []string{workdir, filepath.Join(projectRoot, "a")}); !reflect.DeepEqual(got, single) {
		t.Errorf("an ancestor workdir duplicated files:\n got %v\nwant %v", got, single)
	}
	sibling := filepath.Join(projectRoot, "a", "sibling")
	writeFile(t, filepath.Join(sibling, "CLAUDE.md"), "# Sibling\n")
	want := append(append([]Candidate{}, single...), Candidate{filepath.Join(sibling, "CLAUDE.md"), LayerProject, sibling})
	if got := CandidatesAcross(in, []string{workdir, sibling}); !reflect.DeepEqual(got, want) {
		t.Errorf("sibling workdir:\n got %v\nwant %v", got, want)
	}

	dirs := map[string]string{}
	for _, c := range single {
		dirs[c.Path] = c.Dir
	}
	wantDirs := map[string]string{
		filepath.Join(in.ConfigRoot, "CLAUDE.md"):                    "",
		filepath.Join(projectRoot, "CLAUDE.md"):                      projectRoot,
		filepath.Join(workdir, "CLAUDE.local.md"):                    workdir,
		filepath.Join(workdir, ".claude", "rules", "scoped.md"):      workdir,
		filepath.Join(in.ConfigRoot, "managed", "CLAUDE.md"):         "",
		filepath.Join(in.ConfigRoot, "rules", "userrule.md"):         "",
		filepath.Join(projectRoot, "a", "CLAUDE.md"):                 filepath.Join(projectRoot, "a"),
		filepath.Join(projectRoot, "a", "b", "workdir", "CLAUDE.md"): workdir,
	}
	if !reflect.DeepEqual(dirs, wantDirs) {
		t.Errorf("candidate dirs = %v, want %v", dirs, wantDirs)
	}
}
