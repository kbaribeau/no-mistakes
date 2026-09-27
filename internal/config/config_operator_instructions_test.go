package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeOperatorConfig(t *testing.T, yaml string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadGlobalOrFail(t *testing.T, yaml string) *GlobalConfig {
	t.Helper()
	cfg, err := LoadGlobal(writeOperatorConfig(t, yaml))
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	return cfg
}

func loadGlobalWantError(t *testing.T, yaml string, wantSubstrings ...string) {
	t.Helper()
	_, err := LoadGlobal(writeOperatorConfig(t, yaml))
	if err == nil {
		t.Fatalf("LoadGlobal accepted a config it must refuse:\n%s", yaml)
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// An operator gating branches in a repository they cannot commit a
// .no-mistakes.yaml to still gets a rubric: the global block applies to every
// gated repository and reaches a run that has no repo config at all.
func TestMerge_GlobalOperatorInstructionsApplyToEveryRepository(t *testing.T) {
	global := loadGlobalOrFail(t, `
review:
  path_instructions:
    - path: "**/*.vue"
      instructions: |
        Repeated components come from a computed, not stacked v-ifs.
document:
  instructions: |
    Never write a postmortem into AGENTS.md.
`)

	cfg := Merge(global, &RepoConfig{})

	if len(cfg.Review.PathInstructions) != 1 {
		t.Fatalf("path_instructions = %v, want the operator's entry", cfg.Review.PathInstructions)
	}
	entry := cfg.Review.PathInstructions[0]
	if entry.Path != "**/*.vue" || !strings.Contains(entry.Instructions, "stacked v-ifs") {
		t.Errorf("path_instructions[0] = %+v", entry)
	}
	if entry.Source != InstructionSourceOperatorGlobal {
		t.Errorf("source = %q, want %q", entry.Source, InstructionSourceOperatorGlobal)
	}

	if len(cfg.Document.Instructions) != 1 {
		t.Fatalf("document instructions = %v, want the operator's block", cfg.Document.Instructions)
	}
	if got := cfg.Document.Instructions[0]; got.Source != InstructionSourceOperatorGlobal || !strings.Contains(got.Text, "postmortem") {
		t.Errorf("document instructions[0] = %+v", got)
	}
}

// The per-repository half is the reason global-only would not do: a rule true
// for one application must not be applied while reviewing a sibling.
func TestMergeForCheckout_ScopedInstructionsReachOnlyTheirOwnCheckout(t *testing.T) {
	dir := t.TempDir()
	lams := filepath.Join(dir, "repos", "lams")
	sibling := filepath.Join(dir, "repos", "other")

	global := loadGlobalOrFail(t, "repo_instructions:\n"+
		"  "+yamlPath(lams)+":\n"+
		"    review:\n"+
		"      path_instructions:\n"+
		"        - path: \"**/*.cs\"\n"+
		"          instructions: |\n"+
		"            Sync wording is always \"sync from ATTAINS\".\n"+
		"    document:\n"+
		"      instructions: |\n"+
		"        Configuration keys are owned by docs/reference/config.md.\n")

	matched := MergeForCheckout(global, &RepoConfig{}, lams)
	if len(matched.Review.PathInstructions) != 1 {
		t.Fatalf("path_instructions = %v, want the scoped entry", matched.Review.PathInstructions)
	}
	if got := matched.Review.PathInstructions[0]; got.Source != InstructionSourceOperatorRepo {
		t.Errorf("source = %q, want %q", got.Source, InstructionSourceOperatorRepo)
	}
	if len(matched.Document.Instructions) != 1 || !strings.Contains(matched.Document.Instructions[0].Text, "docs/reference/config.md") {
		t.Errorf("document instructions = %v, want the scoped block", matched.Document.Instructions)
	}

	other := MergeForCheckout(global, &RepoConfig{}, sibling)
	if len(other.Review.PathInstructions) != 0 || len(other.Document.Instructions) != 0 {
		t.Fatalf("a sibling repository received another repository's rules: review=%v document=%v",
			other.Review.PathInstructions, other.Document.Instructions)
	}

	// A caller with no repository in hand resolves the global half only, the
	// same outcome as a key naming some other checkout.
	if unscoped := Merge(global, &RepoConfig{}); len(unscoped.Review.PathInstructions) != 0 {
		t.Fatalf("Merge without a checkout applied a scoped entry: %v", unscoped.Review.PathInstructions)
	}
}

// A key and a recorded checkout path that name the same directory in different
// spellings must match, exactly as worktree_roots resolves placement.
func TestMergeForCheckout_KeyMatchingFollowsWorktreeRootsCanonicalization(t *testing.T) {
	dir := t.TempDir()
	checkout := filepath.Join(dir, "repos", "lams")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}

	global := loadGlobalOrFail(t, "repo_instructions:\n"+
		"  "+yamlPath(checkout)+":\n"+
		"    document:\n"+
		"      instructions: owned by docs\n")

	spelled := filepath.Join(checkout, "sub", "..") + string(filepath.Separator)
	cfg := MergeForCheckout(global, &RepoConfig{}, spelled)
	if len(cfg.Document.Instructions) != 1 {
		t.Fatalf("a different spelling of the same checkout matched nothing: %q", spelled)
	}
}

// Three configurations compose; none replaces another, and each block keeps the
// configuration it came from so the reviewer is never told operator policy is
// the repository's own.
func TestMergeForCheckout_ConcatenatesEverySourceWidestFirst(t *testing.T) {
	dir := t.TempDir()
	checkout := filepath.Join(dir, "repo")

	global := loadGlobalOrFail(t, "review:\n"+
		"  path_instructions:\n"+
		"    - path: \"**/*.vue\"\n"+
		"      instructions: every repository\n"+
		"document:\n"+
		"  instructions: global doc policy\n"+
		"repo_instructions:\n"+
		"  "+yamlPath(checkout)+":\n"+
		"    review:\n"+
		"      path_instructions:\n"+
		"        - path: \"**/*.cs\"\n"+
		"          instructions: this repository\n"+
		"    document:\n"+
		"      instructions: scoped doc policy\n")

	repo := &RepoConfig{
		Review:   ReviewRaw{PathInstructions: []PathInstruction{{Path: "internal/**", Instructions: "committed rubric"}}},
		Document: DocumentRaw{Instructions: "repo doc policy"},
	}

	cfg := MergeForCheckout(global, repo, checkout)

	wantReview := []struct {
		path   string
		source InstructionSource
	}{
		{"**/*.vue", InstructionSourceOperatorGlobal},
		{"**/*.cs", InstructionSourceOperatorRepo},
		{"internal/**", InstructionSourceRepository},
	}
	if len(cfg.Review.PathInstructions) != len(wantReview) {
		t.Fatalf("path_instructions = %v, want all three sources", cfg.Review.PathInstructions)
	}
	for i, want := range wantReview {
		got := cfg.Review.PathInstructions[i]
		if got.Path != want.path || got.Source != want.source {
			t.Errorf("path_instructions[%d] = {%q, %q}, want {%q, %q}", i, got.Path, got.Source, want.path, want.source)
		}
	}

	wantDocs := []DocumentInstruction{
		{Source: InstructionSourceOperatorGlobal, Text: "global doc policy"},
		{Source: InstructionSourceOperatorRepo, Text: "scoped doc policy"},
		{Source: InstructionSourceRepository, Text: "repo doc policy"},
	}
	if len(cfg.Document.Instructions) != len(wantDocs) {
		t.Fatalf("document instructions = %v, want all three sources", cfg.Document.Instructions)
	}
	for i, want := range wantDocs {
		if cfg.Document.Instructions[i] != want {
			t.Errorf("document instructions[%d] = %+v, want %+v", i, cfg.Document.Instructions[i], want)
		}
	}
}

// The allowlist is the point of the feature: operator configuration may add
// review and documentation guidance to a repository, never change what a pass
// requires of it. Anything that could weaken a pass is refused by name rather
// than ignored, so an operator who tries is told it did not take.
func TestLoadGlobal_RepoInstructionsRefusesEveryFieldThatCouldWeakenAPass(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "repo")
	for _, block := range []string{
		"    commands:\n      test: rm -rf /\n",
		"    no_ci: true\n",
		"    allow_repo_commands: true\n",
		"    pr:\n      base_branch: not-main\n",
		"    agent: claude\n",
		"    disable_project_settings: true\n",
		"    ignore_patterns:\n      - \"**\"\n",
	} {
		yaml := "repo_instructions:\n  " + yamlPath(checkout) + ":\n" + block
		loadGlobalWantError(t, yaml, "repo_instructions supports review and document only")
	}
}

// The same boundary at the top level, where the global config's strict decoding
// already refuses an unknown key outright: adding review and document to it must
// not have opened a door for the fields beside them in a repository config.
func TestLoadGlobal_TopLevelOperatorSurfaceCarriesNoExecutingOrGateFields(t *testing.T) {
	for _, yaml := range []string{
		"commands:\n  test: rm -rf /\n",
		"no_ci: true\n",
		"allow_repo_commands: true\n",
		"pr:\n  base_branch: not-main\n",
		"disable_project_settings: true\n",
		"ignore_patterns:\n  - \"**\"\n",
	} {
		loadGlobalWantError(t, yaml, "not found in type")
	}
}

// Keys follow worktree_roots: absolute, and one spelling per checkout, because
// the daemon resolving them has an unrelated working directory and because at
// most one entry may apply to a run.
func TestLoadGlobal_RepoInstructionsKeysFollowWorktreeRootsRules(t *testing.T) {
	loadGlobalWantError(t,
		"repo_instructions:\n  relative/path:\n    document:\n      instructions: x\n",
		"is not absolute")

	dir := t.TempDir()
	checkout := filepath.Join(dir, "repo")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	loadGlobalWantError(t,
		"repo_instructions:\n"+
			"  "+yamlPath(checkout)+":\n    document:\n      instructions: a\n"+
			"  "+yamlPath(checkout+string(filepath.Separator))+":\n    document:\n      instructions: b\n",
		"already names the same checkout")
}

// Each source is budgeted where it is parsed, so an over-budget rule aborts
// before a run starts rather than failing an agent invocation at review time.
func TestLoadGlobal_OperatorReviewPathInstructionsAreBudgetedPerSource(t *testing.T) {
	var many strings.Builder
	many.WriteString("review:\n  path_instructions:\n")
	for i := 0; i <= MaxOperatorReviewPathInstructions; i++ {
		many.WriteString("    - path: \"a/**\"\n      instructions: rule\n")
	}
	loadGlobalWantError(t, many.String(), "review.path_instructions has", "at most 16")

	oversized := "review:\n  path_instructions:\n    - path: \"a/**\"\n      instructions: " +
		strings.Repeat("x", MaxOperatorReviewPathInstructionsBytes) + "\n"
	loadGlobalWantError(t, oversized, "review.path_instructions would add up to", "stays within budget")

	checkout := filepath.Join(t.TempDir(), "repo")
	scopedOversized := "repo_instructions:\n  " + yamlPath(checkout) + ":\n" +
		"    review:\n      path_instructions:\n        - path: \"a/**\"\n          instructions: " +
		strings.Repeat("x", MaxOperatorReviewPathInstructionsBytes) + "\n"
	loadGlobalWantError(t, scopedOversized, "repo_instructions[", "would add up to")

	// Unusable guidance is refused the same way the repository's own copy is.
	loadGlobalWantError(t,
		"review:\n  path_instructions:\n    - path: \"[\"\n      instructions: rule\n",
		"is not a valid glob")
	loadGlobalWantError(t,
		"document:\n  instructions: \"=======\"\n",
		"document.instructions is left empty once merge-conflict markers are removed")
}

// The combined section is bounded by construction rather than by a check in
// Merge: at most three sources can apply to one run, and each was budgeted when
// its own file was parsed.
func TestReviewPathInstructions_CombinedSectionStaysWithinTheSummedBudgets(t *testing.T) {
	fill := func(max, budget int) []PathInstruction {
		var entries []PathInstruction
		for len(entries) < max {
			entries = append(entries, PathInstruction{Path: "internal/**", Instructions: "guidance"})
		}
		if got := ReviewPathInstructionsBytes(entries); got > budget {
			t.Fatalf("fixture is already over its own budget: %d > %d", got, budget)
		}
		return entries
	}

	repo := &RepoConfig{Review: ReviewRaw{PathInstructions: fill(MaxReviewPathInstructions, MaxReviewPathInstructionsBytes)}}
	scoped := RepoInstructions{Review: OperatorReviewRaw{PathInstructions: fill(MaxOperatorReviewPathInstructions, MaxOperatorReviewPathInstructionsBytes)}}
	checkout := filepath.Join(t.TempDir(), "repo")
	global := &GlobalConfig{
		Review:           OperatorReviewRaw{PathInstructions: fill(MaxOperatorReviewPathInstructions, MaxOperatorReviewPathInstructionsBytes)},
		RepoInstructions: map[string]RepoInstructions{checkout: scoped},
	}

	cfg := MergeForCheckout(global, repo, checkout)
	wantEntries := MaxReviewPathInstructions + 2*MaxOperatorReviewPathInstructions
	if len(cfg.Review.PathInstructions) != wantEntries {
		t.Fatalf("combined entries = %d, want %d", len(cfg.Review.PathInstructions), wantEntries)
	}
	wantBytes := MaxReviewPathInstructionsBytes + 2*MaxOperatorReviewPathInstructionsBytes
	if got := ReviewPathInstructionsBytes(cfg.Review.PathInstructions); got > wantBytes {
		t.Fatalf("combined section accounts for %d bytes, past the summed budgets of %d", got, wantBytes)
	}
}

// Provenance framing was granted its own room rather than taken out of the
// repository's share, so a .no-mistakes.yaml that filled the original 16384-byte
// budget still loads.
func TestParseRepoConfig_ConfigAtTheOriginalBudgetStillLoads(t *testing.T) {
	const originalBudget = 16384

	var entries []PathInstruction
	var yaml strings.Builder
	yaml.WriteString("review:\n  path_instructions:\n")
	for i := 0; i < MaxReviewPathInstructions; i++ {
		entry := PathInstruction{Path: "internal/**", Instructions: strings.Repeat("guidance ", 25)}
		entries = append(entries, entry)
		yaml.WriteString("    - path: \"" + entry.Path + "\"\n      instructions: \"" + entry.Instructions + "\"\n")
	}

	// The original accounting: the same measurement without the source line the
	// provenance framing added.
	original := ReviewPathInstructionsBytes(entries) -
		MaxReviewPathInstructions*(len(ReviewPathInstructionsSourceLabel)+ReviewPathInstructionsMaxSourceBytes+len("\n"))
	if original > originalBudget {
		t.Fatalf("fixture measures %d bytes under the original accounting, past the %d it must sit inside", original, originalBudget)
	}

	if _, err := LoadRepoFromBytes([]byte(yaml.String())); err != nil {
		t.Fatalf("a config that fit the original budget is now rejected: %v", err)
	}
}
