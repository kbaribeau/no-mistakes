package steps

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
)

// Operator guidance reaches the reviewer, and every block names the
// configuration it came from, so a rule the operator wrote on this machine is
// never presented as the repository's own committed rubric.
func TestReviewPathInstructionsSection_AttributesEveryBlockToItsSource(t *testing.T) {
	t.Parallel()

	rules := []config.PathInstruction{
		{Path: "**/*.vue", Instructions: "Repeated components come from a computed.", Source: config.InstructionSourceOperatorGlobal},
		{Path: "**/*.cs", Instructions: "Sync wording is always \"sync from ATTAINS\".", Source: config.InstructionSourceOperatorRepo},
		{Path: "internal/**", Instructions: "Credential-carrying URLs go through internal/safeurl.", Source: config.InstructionSourceRepository},
	}
	changed := []string{"app/Widget.vue", "src/Sync.cs", "internal/scm/github.go"}

	got := reviewPathInstructionsSection(matchPathInstructions(changed, rules))
	want := wantSection(
		wantBlockFrom(config.InstructionSourceOperatorGlobal, "**/*.vue", "app/Widget.vue", rules[0].Instructions),
		wantBlockFrom(config.InstructionSourceOperatorRepo, "**/*.cs", "src/Sync.cs", rules[1].Instructions),
		wantBlockFrom(config.InstructionSourceRepository, "internal/**", "internal/scm/github.go", rules[2].Instructions),
	)
	if got != want {
		t.Fatalf("section =\n%q\nwant\n%q", got, want)
	}
}

// A rule that reached the step without a source is labelled unattributed rather
// than credited to a source it may not have come from.
func TestReviewPathInstructionsSection_UnstampedRuleIsNotCreditedToASource(t *testing.T) {
	t.Parallel()

	got := reviewPathInstructionsSection(matchPathInstructions(
		[]string{"docs/index.md"},
		[]config.PathInstruction{{Path: "docs/**", Instructions: "Prose changes only."}},
	))
	for _, source := range []config.InstructionSource{
		config.InstructionSourceRepository,
		config.InstructionSourceOperatorGlobal,
		config.InstructionSourceOperatorRepo,
	} {
		if strings.Contains(got, config.ReviewPathInstructionsSourceLabel+string(source)) {
			t.Fatalf("an unstamped rule was credited to %q:\n%s", source, got)
		}
	}
	if !strings.Contains(got, config.ReviewPathInstructionsSourceLabel+"unattributed configuration") {
		t.Fatalf("block carries no source line at all:\n%s", got)
	}
}

// End to end through the real review step: an operator who cannot commit a
// .no-mistakes.yaml to the repository still gets their rubric into the prompt,
// appended and attributed, with the repository's own rules alongside it.
func TestReviewStep_OperatorInstructionsReachTheReviewPrompt(t *testing.T) {
	t.Parallel()

	unconfigured := reviewPromptFor(t, nil)

	prompt := reviewPromptForResolved(t, config.Review{PathInstructions: []config.PathInstruction{
		{Path: "*.txt", Instructions: "Fixture files carry no product behavior.", Source: config.InstructionSourceOperatorGlobal},
		{Path: "feature.txt", Instructions: "This repository's fixtures are shared.", Source: config.InstructionSourceOperatorRepo},
	}})

	want := strings.TrimSuffix(unconfigured, agent.MemoryFilesRule) + wantSection(
		wantBlockFrom(config.InstructionSourceOperatorGlobal, "*.txt", "feature.txt", "Fixture files carry no product behavior."),
		wantBlockFrom(config.InstructionSourceOperatorRepo, "feature.txt", "feature.txt", "This repository's fixtures are shared."),
	) + agent.MemoryFilesRule
	if prompt != want {
		t.Fatalf("review prompt =\n%q\nwant\n%q", prompt, want)
	}
}

// The document gate reads scoped operator and repository policy, each attributed. The
// framing must stay "augments the defaults", because operator guidance can only
// add to what a pass requires.
func TestDocumentStep_PolicyBlocksFromEverySourceAreAttributed(t *testing.T) {
	dir, baseSHA, headSHA := setupGitRepo(t)

	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			return &agent.Result{Output: json.RawMessage(`{"findings":[],"summary":"docs current"}`)}, nil
		},
	}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Config.Document.Instructions = []config.DocumentInstruction{
		{Source: config.InstructionSourceOperatorRepo, Text: "Configuration keys are owned by docs/reference/config.md."},
		{Source: config.InstructionSourceRepository, Text: "docs/architecture.md owns the daemon lifecycle facts."},
	}

	if _, err := (&DocumentStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}

	prompt := ag.calls[0].Prompt
	for _, block := range sctx.Config.Document.Instructions {
		want := "source: " + string(block.Source) + "\n" + block.Text
		if !strings.Contains(prompt, want) {
			t.Errorf("document prompt is missing the attributed block %q:\n%s", want, prompt)
		}
	}
	if !strings.Contains(prompt, "augments the defaults above and cannot weaken them") {
		t.Error("document policy must stay framed as augmenting, not replacing, the defaults")
	}
	if strings.Contains(prompt, "trusted, from the default branch") {
		t.Error("operator policy must not be presented as coming from the repository's default branch")
	}
}
