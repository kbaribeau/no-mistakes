//go:build e2e

package e2e

import (
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
)

// TestOperatorOwnedInstructionsJourney is the end-to-end proof for a repository
// the operator cannot commit a .no-mistakes.yaml to: no repo config exists at
// all, and the review rubric still arrives - the global block for every gated
// repository and the repo_instructions block for this one, each attributed to
// the configuration it came from.
func TestOperatorOwnedInstructionsJourney(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude"})
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("nm init: %v\n%s", err, out)
	}

	// Written after init because repo_instructions is keyed by the checkout
	// path init registers, which is the same key worktree_roots uses.
	appendGlobalConfig(t, h, fmt.Sprintf(`review:
  path_instructions:
    - path: 'internal/**'
      instructions: |
        %s
repo_instructions:
  %q:
    review:
      path_instructions:
        - path: 'internal/scm/**'
          instructions: |
            %s
    document:
      instructions: |
        %s
`, operatorGlobalRule, h.WorkDir, operatorScopedRule, operatorDocPolicy))

	branch := "operator-owned-instructions"
	h.CommitChange(branch, "internal/scm/github/github.go", "package github\n\n// changed\n", "touch scm")
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
	if !strings.Contains(docPrompt, "source: "+string(config.InstructionSourceOperatorRepo)+"\n"+operatorDocPolicy) {
		t.Errorf("document prompt is missing the attributed operator policy:\n%s", docPrompt)
	}

	t.Logf("review prompt tail:\n%s", promptTail(prompt))
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
