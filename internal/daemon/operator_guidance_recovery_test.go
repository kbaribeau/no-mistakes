package daemon

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/paths"
)

// Real on-disk operator config, local Git fetch and registered checkout
// selection. No daemon or model is launched by this regression.
func TestLoadRecoveredConfig_OperatorGuidanceRereadsAndChecksSharedBudget(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo, _ := setupTestGitRepo(t, p, d, "guidance-recovery")
	var trusted strings.Builder
	trusted.WriteString("document:\n  instructions: trusted document policy\nreview:\n  path_instructions:\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&trusted, "    - path: 'repo%d/**'\n      instructions: trusted rule\n", i)
	}
	head := commitDefaultBranchConfig(t, repo.WorkingPath, trusted.String())
	run, err := d.InsertRun(repo.ID, "feature", head, head)
	if err != nil {
		t.Fatal(err)
	}
	dir := p.WorktreeDir(repo.ID, run.ID)
	gitCmd(t, p.RepoDir(repo.ID), "worktree", "add", "--detach", dir, head)
	writeGlobal := func(scopedCount int, wording string) {
		t.Helper()
		var text strings.Builder
		fmt.Fprintf(&text, "document:\n  instructions: %s global policy\nreview:\n  path_instructions:\n", wording)
		for i := 0; i < 8; i++ {
			fmt.Fprintf(&text, "    - path: 'global%d/**'\n      instructions: global rule\n", i)
		}
		fmt.Fprintf(&text, "repo_instructions:\n  %q:\n    review:\n      path_instructions:\n", repo.WorkingPath)
		for i := 0; i < scopedCount; i++ {
			fmt.Fprintf(&text, "        - path: 'local%d/**'\n          instructions: %s\n", i, wording)
		}
		fmt.Fprintf(&text, "    document:\n      instructions: %s checkout policy\n", wording)
		if err := os.WriteFile(p.ConfigFile(), []byte(text.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mgr := NewRunManager(d, p, nil)
	writeGlobal(4, "historical guidance")
	uninterrupted, err := mgr.loadRecoveredConfig(context.Background(), run, repo, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(uninterrupted.Review.PathInstructions) != 32 {
		t.Fatalf("registered checkout not selected from the run worktree: %+v", uninterrupted.Review)
	}
	wantDocs := []config.DocumentInstruction{
		{Source: config.InstructionSourceOperatorGlobal, Text: "historical guidance global policy"},
		{Source: config.InstructionSourceOperatorRepo, Text: "historical guidance checkout policy"},
		{Source: config.InstructionSourceRepository, Text: "trusted document policy"},
	}
	if !reflect.DeepEqual(uninterrupted.Document.Instructions, wantDocs) {
		t.Fatalf("recovery lost document selection: %+v", uninterrupted.Document)
	}
	oldSnapshot := append([]byte(nil), uninterrupted.ReplayGlobalYAML...)
	writeGlobal(4, "updated guidance")
	recovered, err := mgr.loadRecoveredConfig(context.Background(), run, repo, dir)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Review.PathInstructions[8].Instructions != "updated guidance" || uninterrupted.Review.PathInstructions[8].Instructions != "historical guidance" {
		t.Fatal("recovery must reread without rewriting the earlier loaded config")
	}
	if recovered.Document.Instructions[0].Text != "updated guidance global policy" || recovered.Document.Instructions[1].Text != "updated guidance checkout policy" {
		t.Fatalf("recovery did not reread operator document policies: %+v", recovered.Document)
	}
	historical, err := config.LoadEvalConfig(oldSnapshot, uninterrupted.ReplayRepoYAML)
	if err != nil || !reflect.DeepEqual(historical.Review, uninterrupted.Review) || !reflect.DeepEqual(historical.Document.Instructions, wantDocs) {
		t.Fatalf("recorded historical guidance changed on recovery: %v", err)
	}
	writeGlobal(5, "updated guidance")
	if _, err := config.LoadGlobal(p.ConfigFile()); err != nil {
		t.Fatalf("over-combination fixture must still parse individually: %v", err)
	}
	if cfg, err := mgr.loadRecoveredConfig(context.Background(), run, repo, dir); cfg != nil || err == nil || !strings.Contains(err.Error(), "33 entries") {
		t.Fatalf("recovery bypassed the aggregate: cfg=%+v err=%v", cfg, err)
	}
}
