//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const (
	operatorGlobalRule = "Every changed file needs a reason recorded in the commit message."
	operatorScopedRule = "Sync wording is always \"sync from ATTAINS\" - it is a one-way overwrite."
	operatorDocPolicy  = "Configuration keys are owned by docs/reference/config.md."
	operatorGlobalDoc  = "Keep durable lessons with their owning documentation and regression test."
)

// TestOperatorOwnedInstructionsJourney is the end-to-end proof for a repository
// the operator cannot commit a .no-mistakes.yaml to: no repo config exists at
// all, and the review rubric still arrives - the global block for every gated
// repository and the repo_instructions block for this one, each attributed to
// the configuration it came from.
func TestOperatorOwnedInstructionsJourney(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		t.Run(fmt.Sprintf("scoped=%v", scoped), func(t *testing.T) {
			testOperatorOwnedInstructionsJourney(t, scoped)
		})
	}
}

func testOperatorOwnedInstructionsJourney(t *testing.T, scoped bool) {
	h := NewHarness(t, SetupOpts{Agent: "claude", NoRepoConfig: true})
	assertNoRepositoryConfig(t, h)
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("nm init: %v\n%s", err, out)
	}

	// Written after init because repo_instructions is keyed by the checkout
	// path init registers, which is the same key worktree_roots uses.
	guidance := fmt.Sprintf(`review:
  path_instructions:
    - path: 'internal/**'
      instructions: |
        %s
document:
  instructions: |
    %s
`, operatorGlobalRule, operatorGlobalDoc)
	if scoped {
		guidance += fmt.Sprintf(`repo_instructions:
  %q:
    review:
      path_instructions:
        - path: 'internal/scm/**'
          instructions: |
            %s
    document:
      instructions: |
        %s
`, h.WorkDir, operatorScopedRule, operatorDocPolicy)
	}
	appendGlobalConfig(t, h, guidance)

	branch := "operator-owned-instructions"
	h.CommitChange(branch, "internal/scm/github/github.go", "package github\n\n// changed\n", "touch scm")
	assertNoRepositoryConfig(t, h)
	h.PushToGate(branch)

	run := h.WaitForRun(branch, 120*time.Second)
	if run.Status != types.RunCompleted {
		t.Fatalf("run did not complete: status=%s error=%v", run.Status, deref(run.Error))
	}

	prompt := reviewPrompt(t, h)
	if !strings.Contains(prompt, config.ReviewPathInstructionsHeading) {
		t.Fatalf("review prompt carries no path-instructions section:\n%s", promptTail(prompt))
	}

	for _, want := range []struct {
		source config.InstructionSource
		glob   string
		rule   string
	}{
		{config.InstructionSourceOperatorGlobal, "internal/**", operatorGlobalRule},
		{config.InstructionSourceOperatorRepo, "internal/scm/**", operatorScopedRule},
	} {
		if !scoped && want.source == config.InstructionSourceOperatorRepo {
			if strings.Contains(prompt, want.rule) {
				t.Fatal("unconfigured scoped review rule reached the prompt")
			}
			continue
		}
		block := config.ReviewPathInstructionsPathLabel + want.glob + "\n" +
			config.ReviewPathInstructionsSourceLabel + string(want.source) + "\n" +
			config.ReviewPathInstructionsFilesLabel + "internal/scm/github/github.go\n" +
			config.ReviewPathInstructionsRulesLabel + "\n" + want.rule
		if !strings.Contains(prompt, block) {
			t.Errorf("review prompt is missing the %s block\n%q\ngot:\n%s", want.source, block, promptTail(prompt))
		}
	}

	// The operator's documentation policy reaches the document gate the same
	// way, and says where it came from.
	docPrompt := documentPrompt(t, h)
	if !strings.Contains(docPrompt, "source: "+string(config.InstructionSourceOperatorGlobal)+"\n"+operatorGlobalDoc) {
		t.Errorf("document prompt is missing the attributed global policy:\n%s", docPrompt)
	}
	if got := strings.Contains(docPrompt, "source: "+string(config.InstructionSourceOperatorRepo)+"\n"+operatorDocPolicy); got != scoped {
		t.Errorf("scoped document policy presence=%v, want %v", got, scoped)
	}
	if strings.Contains(docPrompt, "source: "+string(config.InstructionSourceRepository)+"\n") {
		t.Fatal("repository with no config gained an attributed repository policy")
	}

	assertNoRepositoryConfig(t, h)
	t.Logf("review prompt tail:\n%s", promptTail(prompt))
	t.Logf("document prompt carries global policy with scoped=%v: %s", scoped, operatorGlobalDoc)
}

// assertNoRepositoryConfig checks positive tree reads, not a failed git show:
// an unreadable ref is a fixture failure, never evidence of an absent config.
// Check the authoritative default branch AND the branch being reviewed, since
// deleting only the working copy would leave the daemon reading trusted config.
func assertNoRepositoryConfig(t *testing.T, h *Harness) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, tree := range []struct {
		name, dir, ref string
	}{
		{"trusted default branch", h.UpstreamDir, "refs/heads/main"},
		{"reviewed branch", h.WorkDir, "HEAD"},
	} {
		out, err := h.runGit(ctx, tree.dir, "ls-tree", "-r", "--name-only", tree.ref, "--", ".no-mistakes.yaml")
		if err != nil {
			t.Fatalf("cannot inspect %s: %v\n%s", tree.name, err, out)
		}
		if strings.TrimSpace(string(out)) != "" {
			t.Errorf("%s contains repository config: %s", tree.name, out)
		}
	}
	if _, err := os.Stat(filepath.Join(h.WorkDir, ".no-mistakes.yaml")); !os.IsNotExist(err) {
		t.Errorf("working checkout must have no repository config; stat error = %v", err)
	}
	if t.Failed() {
		t.FailNow()
	}
	t.Log("repository config absent in trusted default branch, reviewed branch and working checkout")
}

// appendGlobalConfig adds operator-owned settings to the harness's generated
// global config. The daemon reads the global config when a run starts, so an
// edit made here reaches the next run without a restart.
func appendGlobalConfig(t *testing.T, h *Harness, extra string) {
	t.Helper()
	path := filepath.Join(h.NMHome, "config.yaml")
	existing, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read global config: %v", err)
	}
	if err := os.WriteFile(path, append(existing, []byte("\n"+extra)...), 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
	}
}

// documentPrompt returns the prompt of the document step's agent invocation.
func documentPrompt(t *testing.T, h *Harness) string {
	t.Helper()
	for _, inv := range h.AgentInvocations() {
		if strings.Contains(inv.Prompt, "Documentation ownership policy (trusted;") {
			return inv.Prompt
		}
	}
	t.Fatal("document step never received a documentation ownership policy")
	return ""
}
