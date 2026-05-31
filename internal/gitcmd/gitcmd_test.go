package gitcmd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/friday-james/vibelog/internal/gitcmd"
	"github.com/friday-james/vibelog/internal/initcmd"
	"github.com/friday-james/vibelog/internal/model"
	"github.com/friday-james/vibelog/internal/store"
)

// makeGitRepo initializes a git repo in tmp with a small commit history.
func makeGitRepo(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = tmp
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	run("git", "init", "-q")
	run("git", "config", "commit.gpgsign", "false")
	for i, msg := range []string{"first", "second", "third"} {
		os.WriteFile(filepath.Join(tmp, "f"+string(rune('0'+i))+".txt"), []byte(msg), 0o644)
		run("git", "add", ".")
		run("git", "commit", "-q", "-m", msg)
	}
	return tmp
}

func TestRun_IngestsCommitsChronologically(t *testing.T) {
	tmp := makeGitRepo(t)
	if err := initcmd.Run(tmp); err != nil {
		t.Fatal(err)
	}
	res, err := gitcmd.Run(tmp, 0)
	if err != nil {
		t.Fatalf("ingest-git: %v", err)
	}
	if res.Added != 3 {
		t.Errorf("expected 3 commits added, got %d", res.Added)
	}
	state, err := store.Load(tmp)
	if err != nil {
		t.Fatal(err)
	}
	commits := []model.Iteration{}
	for _, it := range state.Iterations {
		if it.Kind == model.KindCommit {
			commits = append(commits, it)
		}
	}
	if len(commits) != 3 {
		t.Fatalf("expected 3 commit entries, got %d", len(commits))
	}
	for i, c := range commits {
		if c.ID != i+1 {
			t.Errorf("commit %d: expected id=%d, got %d", i, i+1, c.ID)
		}
	}
	if commits[0].Summary != "first" || commits[2].Summary != "third" {
		t.Errorf("commits out of chronological order: %v", []string{commits[0].Summary, commits[2].Summary})
	}
}

func TestRun_Idempotent(t *testing.T) {
	tmp := makeGitRepo(t)
	if err := initcmd.Run(tmp); err != nil {
		t.Fatal(err)
	}
	if _, err := gitcmd.Run(tmp, 0); err != nil {
		t.Fatal(err)
	}
	res, err := gitcmd.Run(tmp, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 0 {
		t.Errorf("second run added %d (expected 0)", res.Added)
	}
	if res.Skipped != 3 {
		t.Errorf("expected 3 skipped, got %d", res.Skipped)
	}
}
