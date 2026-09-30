package steps

import (
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
)

func TestReviewStep_CombinedBudgetRefusesBeforeAgent(t *testing.T) {
	for _, fixing := range []bool{false, true} {
		cfg := &config.Config{}
		for i := 0; i <= config.MaxReviewPathInstructions; i++ {
			cfg.Review.PathInstructions = append(cfg.Review.PathInstructions, config.PathInstruction{
				Path: "currently-unmatched/**", Instructions: "still an obligation", Source: config.InstructionSourceOperatorGlobal,
			})
		}
		// No worktree or agent: overflow must refuse before Git, path matching,
		// deduplication, or either a fixer or reviewer invocation can occur.
		sctx := &pipeline.StepContext{Config: cfg, Fixing: fixing}
		if _, err := (&ReviewStep{}).Execute(sctx); err == nil || !strings.Contains(err.Error(), "combined review.path_instructions") {
			t.Fatalf("fixing=%v: want aggregate refusal, got %v", fixing, err)
		}
		if len(cfg.Review.PathInstructions) != 33 {
			t.Fatal("overflow silently discarded rules")
		}
	}
}
