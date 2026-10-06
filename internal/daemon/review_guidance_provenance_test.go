package daemon

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/paths"
)

// Recovery resolves current operator inputs for FUTURE rounds without changing
// a previously enabled executor's provenance. No daemon process is started.
func TestRecoveredReviewGuidanceProvenanceKeepsEarlierInputs(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo, _ := setupTestGitRepo(t, p, d, "guidance-provenance")
	trustedYAML := []byte("review:\n  path_instructions: [{path: '*.go', instructions: trusted rule}]\n")
	if err := os.WriteFile(filepath.Join(repo.WorkingPath, ".no-mistakes.yaml"), trustedYAML, 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo.WorkingPath, "commit", "-am", "trusted review rules")
	gitCmd(t, repo.WorkingPath, "push", "gate", "HEAD:refs/heads/main")
	head := gitOutput(t, repo.WorkingPath, "rev-parse", "HEAD")
	run, err := d.InsertRun(repo.ID, "feature", head, head)
	if err != nil {
		t.Fatal(err)
	}
	workDir := p.WorktreeDir(repo.ID, run.ID)
	gitCmd(t, p.RepoDir(repo.ID), "worktree", "add", "--detach", workDir, head)
	mgr := NewRunManager(d, p, nil)
	writeGlobal := func(name string) {
		t.Helper()
		data := fmt.Sprintf("repository_overrides:\n  %q:\n    review:\n      path_instructions: [{path: '*.go', instructions: historical-%s}]\n", repo.UpstreamURL, name)
		if err := os.WriteFile(p.ConfigFile(), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeGlobal("A")
	a, err := mgr.loadRecoveredConfig(context.Background(), run, repo, workDir)
	if err != nil {
		t.Fatal(err)
	}
	aBytes := append([]byte(nil), a.ReplayGlobalYAML...)
	writeGlobal("B")
	b, err := mgr.loadRecoveredConfig(context.Background(), run, repo, workDir)
	if err != nil {
		t.Fatal(err)
	}
	writeGlobal("C")
	for _, tc := range []struct {
		cfg  *config.Config
		want string
	}{{a, "historical-A"}, {a, "historical-A"}, {b, "historical-B"}} {
		if !tc.cfg.CaptureEvalProvenance {
			t.Fatal("recovery omitted eval provenance")
		}
		replayed, err := config.LoadEvalConfig(tc.cfg.ReplayGlobalYAML, tc.cfg.ReplayRepoYAML)
		if err != nil {
			t.Fatal(err)
		}
		if len(replayed.Review.RepositoryPathInstructions) != 1 || replayed.Review.RepositoryPathInstructions[0].Instructions != tc.want {
			t.Fatalf("selected inputs = %#v, want %s", replayed.Review, tc.want)
		}
		if len(replayed.Review.PathInstructions) != 1 || replayed.Review.PathInstructions[0].Instructions != "trusted rule" {
			t.Fatal("lost trusted input")
		}
	}
	if !bytes.Equal(a.ReplayGlobalYAML, aBytes) {
		t.Fatal("recovery mutated earlier provenance")
	}
}
