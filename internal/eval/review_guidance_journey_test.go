package eval

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// Exercise the production writer -> round DB -> Capture -> on-disk case ->
// Replay -> Claude adapter -> scripted process path. This checks configured
// inputs and current source sections, deliberately not old full prompt bytes.
// A/A/B models an uninterrupted executor followed by a recovery config reread;
// C is the current machine config at export AND replay time, never an input.
func TestReviewGuidanceCaptureReplayReceivesHistoricalInputs(t *testing.T) {
	ctx := context.Background()
	p, database, seedRun, repo, seedRound := setupCapturedRun(t, ctx)
	defer database.Close()
	t.Setenv("NM_HOME", p.Root())
	run, err := database.InsertRun(repo.ID, seedRun.Branch, seedRun.HeadSHA, seedRun.BaseSHA)
	if err != nil {
		t.Fatal(err)
	}
	step, err := database.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := config.LoadRepoFromBytes(seedRound.RepoConfigYAML)
	if err != nil {
		t.Fatal(err)
	}

	writeCurrent := func(name, globalPath, selectedPath string) {
		t.Helper()
		data := journeyGuidanceYAML(name, globalPath, selectedPath)
		if err := os.WriteFile(p.ConfigFile(), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	resolveCurrent := func() *config.Config {
		t.Helper()
		global, err := config.LoadGlobal(p.ConfigFile())
		if err != nil {
			t.Fatal(err)
		}
		cfg := config.MergeForRemote(global, trusted, repo.UpstreamURL)
		if err := cfg.EnableEvalProvenance(global, trusted); err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	type expected struct {
		name               string
		global, selected   bool
		configuredSelected int
	}
	wants := []expected{
		{"A", true, true, 2}, {"A", true, true, 2}, {"B", true, true, 2},
		{"empty", false, false, 0}, {"unmatched", false, false, 2}, {"global-only", true, false, 0},
	}
	byRound := map[string]expected{}
	writeCurrent("A", "*.go", "*.go")
	liveA := resolveCurrent()
	originalA := append([]byte(nil), liveA.ReplayGlobalYAML...)
	for i, want := range wants {
		cfg := liveA
		switch i {
		case 1:
			// A live executor keeps A even after disk changes to B.
			writeCurrent("B", "*.go", "*.go")
		case 2:
			cfg = resolveCurrent() // recovery constructs a fresh config from B
		case 3:
			writeCurrent(want.name, "", "")
			cfg = resolveCurrent()
		case 4:
			writeCurrent(want.name, "", "docs/**")
			cfg = resolveCurrent()
		case 5:
			writeCurrent(want.name, "*.go", "")
			cfg = resolveCurrent()
		}
		round, err := database.InsertReviewStepRoundWithProvenance(step.ID, i+1, "initial", seedRound.FindingsJSON, nil,
			run.HeadSHA, run.HeadSHA, run.BaseSHA, cfg.ReplayGlobalYAML, cfg.ReplayRepoYAML, 1)
		if err != nil {
			t.Fatal(err)
		}
		byRound[round.ID] = want
	}
	writeCurrent("C", "*.go", "*.go")
	if !bytes.Equal(liveA.ReplayGlobalYAML, originalA) {
		t.Fatal("reread mutated A")
	}
	roundsBefore, err := database.GetRoundsByStep(step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(roundsBefore[0].GlobalConfigYAML, roundsBefore[1].GlobalConfigYAML) || bytes.Equal(roundsBefore[1].GlobalConfigYAML, roundsBefore[2].GlobalConfigYAML) {
		t.Fatal("recorded rounds do not hold A/A/B")
	}

	store, err := Open(p.EvalDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cases, err := Capture(ctx, store, p, database, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != len(wants) {
		t.Fatalf("captured %d cases, want %d", len(cases), len(wants))
	}
	byCase := map[string]expected{}
	artifactsBefore := map[string]map[string]string{}
	for _, c := range cases {
		want := byRound[c.SourceRoundID]
		byCase[c.ID] = want
		artifactsBefore[c.ID] = guidanceArtifactBytes(t, c)
		cfg, err := replayConfig(c)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Review.RepositoryPathInstructions) != want.configuredSelected {
			t.Fatalf("case %s lost configured-but-unmatched/empty selection: %#v", c.ID, cfg.Review)
		}
		if strings.Contains(artifactsBefore[c.ID]["global.yaml"], "never-selected-rule") {
			t.Fatal("unselected guidance exported")
		}
	}
	promptPath := installGuidanceReceiptAgent(t)
	seen := map[string]bool{}
	_, evaluations, err := Replay(ctx, store, ReplayOptions{
		Set: "all", Candidate: Candidate{Agent: types.AgentClaude, Model: "test"}, Repeats: 1,
		OnResult: func(e Evaluation, _, _ int) {
			want := byCase[e.CaseID]
			if e.Status != "completed" {
				t.Fatalf("replay failed: %#v", e)
			}
			data, err := os.ReadFile(promptPath)
			if err != nil {
				t.Fatal(err)
			}
			prompt := string(data)
			assertGuidancePrompt(t, prompt, want.name, want.global, want.selected)
			seen[e.CaseID] = true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluations) != len(wants) || len(seen) != len(wants) {
		t.Fatalf("missing receipts: %d evaluations / %d prompts", len(evaluations), len(seen))
	}
	for _, c := range cases {
		if !reflect.DeepEqual(artifactsBefore[c.ID], guidanceArtifactBytes(t, c)) {
			t.Fatal("replay rewrote historical case")
		}
	}
	roundsAfter, err := database.GetRoundsByStep(step.ID)
	if err != nil || !reflect.DeepEqual(roundsBefore, roundsAfter) {
		t.Fatalf("capture/replay changed source rounds: %v", err)
	}
}

func TestReplayRejectsAmbiguousOrInvalidGuidanceBeforeAgentLaunch(t *testing.T) {
	ctx := context.Background()
	p, database, run, _, _ := setupCapturedRun(t, ctx)
	defer database.Close()
	store, err := Open(p.EvalDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cases, err := Capture(ctx, store, p, database, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	promptPath := installGuidanceReceiptAgent(t)
	for _, data := range []string{
		"no_mistakes_selected_review_guidance: {version: 99, repository_path_instructions: []}\n",
		"no_mistakes_selected_review_guidance: null\n",
		"no_mistakes_selected_guidance: {version: 1, review: [], document: []}\n",
		journeyGuidanceYAML("ambiguous-legacy", "*.go", "*.go"),
	} {
		path := filepath.Join(cases[0].Dir, "config", "global.yaml")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		_, evaluations, err := Replay(ctx, store, ReplayOptions{Set: "all", Candidate: Candidate{Agent: types.AgentClaude, Model: "test"}, Repeats: 1})
		if err == nil || !strings.Contains(err.Error(), "1 replay invocation(s) failed") {
			t.Fatalf("replay did not report its failed invocation: %v", err)
		}
		if len(evaluations) != 1 || evaluations[0].Status != "failed" || evaluations[0].Error == "" {
			t.Fatalf("invalid guidance did not fail: %#v", evaluations)
		}
		if _, err := os.Stat(promptPath); !os.IsNotExist(err) {
			t.Fatalf("candidate launched for invalid guidance: %v", err)
		}
		unchanged, err := os.ReadFile(path)
		if err != nil || string(unchanged) != data {
			t.Fatal("rejected artifact modified")
		}
	}
}

func journeyGuidanceYAML(name, globalPath, selectedPath string) string {
	var b strings.Builder
	if globalPath != "" {
		fmt.Fprintf(&b, "review:\n  path_instructions: [{path: '%s', instructions: historical-global-%s}]\n", globalPath, name)
	}
	b.WriteString("repository_overrides:\n  https://example.test/org/repo:\n    review:\n      path_instructions:")
	if selectedPath == "" {
		b.WriteString(" []\n")
	} else {
		fmt.Fprintf(&b, "\n        - {path: '%s', instructions: historical-selected-%s}\n        - {path: 'never/**', instructions: retained-unmatched-%s}\n", selectedPath, name, name)
	}
	b.WriteString("  https://example.test/org/other:\n    review:\n      path_instructions: [{path: '*.go', instructions: never-selected-rule}]\n")
	return b.String()
}

func installGuidanceReceiptAgent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "prompt.txt")
	fake := filepath.Join(dir, "claude")
	const reply = `{"type":"result","subtype":"success","is_error":false,"structured_output":{"findings":[],"risk_level":"low","risk_rationale":"clean","risk_scope":"source-or-external"},"usage":{"input_tokens":1,"output_tokens":1}}`
	var script string
	if runtime.GOOS == "windows" {
		fake += ".cmd"
		script = "@echo off\r\nmore >\"" + promptPath + "\"\r\necho " + reply + "\r\n"
	} else {
		script = "#!/bin/sh\ncat > '" + promptPath + "'\ncat <<'EOF'\n" + reply + "\nEOF\n"
	}
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return promptPath
}

func assertGuidancePrompt(t *testing.T, prompt, name string, global, selected bool) {
	t.Helper()
	last := -1
	for _, source := range []struct {
		heading, text string
		present       bool
	}{
		{config.ReviewGlobalPathInstructionsHeading, "historical-global-" + name, global},
		{config.ReviewRepositoryPathInstructionsHeading, "historical-selected-" + name, selected},
		{config.ReviewPathInstructionsHeading, "review error paths", true},
	} {
		if !source.present {
			if strings.Contains(prompt, source.heading) || strings.Contains(prompt, source.text) {
				t.Fatalf("unmatched/empty source rendered: %q", source.heading)
			}
			continue
		}
		index := strings.Index(prompt, source.heading)
		textIndex := strings.Index(prompt, source.text)
		if index <= last || textIndex <= index || strings.Count(prompt, source.heading) != 1 {
			t.Fatalf("missing/duplicate/out-of-order current section %q (heading=%d text=%d previous=%d)", source.heading, index, textIndex, last)
		}
		last = textIndex
	}
	for _, forbidden := range []string{"historical-global-C", "historical-selected-C", "never-selected-rule", "retained-unmatched-"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("replay prompt received %q", forbidden)
		}
	}
}
