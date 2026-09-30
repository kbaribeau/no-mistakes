package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func budgetRules(n int, prefix string) []PathInstruction {
	var rules []PathInstruction
	for i := 0; i < n; i++ {
		rules = append(rules, PathInstruction{Path: fmt.Sprintf("%s%d/**", prefix, i), Instructions: "check this"})
	}
	return rules
}

func assertSourceBudgets(t *testing.T, global *GlobalConfig, repo *RepoConfig) {
	t.Helper()
	if err := validateReviewRaw(repo.Review); err != nil {
		t.Fatal(err)
	}
	if err := validateReviewPathInstructions("global", global.Review.PathInstructions, MaxOperatorReviewPathInstructions, MaxOperatorReviewPathInstructionsBytes); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRepoInstructions(global.RepoInstructions); err != nil {
		t.Fatal(err)
	}
}

func TestResolveForRepository_CombinedEntryBoundary(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "repo")
	repo := &RepoConfig{Review: ReviewRaw{PathInstructions: budgetRules(20, "repo")}}
	global := DefaultGlobalConfig()
	global.Review.PathInstructions = budgetRules(8, "global")
	global.RepoInstructions = map[string]RepoInstructions{checkout: {Review: OperatorReviewRaw{PathInstructions: budgetRules(4, "scoped")}}}
	assertSourceBudgets(t, global, repo)
	cfg, err := ResolveForRepository(global, repo, "", checkout)
	if err != nil || len(cfg.Review.PathInstructions) != 32 {
		t.Fatalf("32 combined entries: cfg=%+v err=%v", cfg, err)
	}
	global.RepoInstructions[checkout] = RepoInstructions{Review: OperatorReviewRaw{PathInstructions: budgetRules(5, "scoped")}}
	assertSourceBudgets(t, global, repo) // Every source still fits on its own.
	cfg, err = ResolveForRepository(global, repo, "", checkout)
	if err == nil || cfg != nil || !strings.Contains(err.Error(), "combined review.path_instructions") || !strings.Contains(err.Error(), "33 entries") {
		t.Fatalf("one over must refuse the combination, not drop a rule: cfg=%+v err=%v", cfg, err)
	}
	if cfg, err := ResolveForRepository(global, repo, "", filepath.Join(t.TempDir(), "other")); err != nil || len(cfg.Review.PathInstructions) != 28 {
		t.Fatalf("unselected checkout consumed budget: cfg=%+v err=%v", cfg, err)
	}
}

func TestResolveForRepository_CombinedByteBoundary(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "repo")
	global := DefaultGlobalConfig()
	global.Review.PathInstructions = budgetRules(1, "global")
	global.RepoInstructions = map[string]RepoInstructions{checkout: {Review: OperatorReviewRaw{PathInstructions: budgetRules(1, "scoped")}}}
	repo := &RepoConfig{Review: ReviewRaw{PathInstructions: budgetRules(1, "repo")}}
	entries := MergeForCheckout(global, repo, checkout).Review.PathInstructions
	extra := ReviewPathInstructionsBudgetBytes(entries) - ReviewPathInstructionsBytes(entries)
	repo.Review.PathInstructions[0].Instructions += strings.Repeat("x", extra)
	assertSourceBudgets(t, global, repo)
	cfg, err := ResolveForRepository(global, repo, "", checkout)
	if err != nil {
		t.Fatalf("exact aggregate byte boundary: %v", err)
	}
	if got, want := ReviewPathInstructionsBytes(cfg.Review.PathInstructions), ReviewPathInstructionsBudgetBytes(cfg.Review.PathInstructions); got != want {
		t.Fatalf("fixture does not reach byte boundary: %d != %d", got, want)
	}
	repo.Review.PathInstructions[0].Instructions += "x"
	assertSourceBudgets(t, global, repo)
	if cfg, err := ResolveForRepository(global, repo, "", checkout); cfg != nil || err == nil || !strings.Contains(err.Error(), "would add up to") {
		t.Fatalf("one byte over must refuse: cfg=%+v err=%v", cfg, err)
	}
}

// Independent reference to the pre-feature format: saturate the OLD budget,
// not merely a comfortably-small fixture measured by the new accounting.
func legacyInstructionBytes(entries []PathInstruction) int {
	heading := "Repository review instructions for the changed paths (trusted, from the default branch). Each block below applies only to the files listed under its path, and adds to the requirements above:"
	n := len("\n\n" + heading + "\n")
	for i, e := range entries {
		if i > 0 {
			n += len("\n\n")
		}
		n += len("path: "+e.Path+"\nmatched files: ") + 192 + len("\ninstructions:\n"+e.Instructions)
	}
	return n
}

func TestParseRepoConfig_ConfigAtTheOriginalBudgetStillLoads(t *testing.T) {
	for _, count := range []int{1, 32} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			entries := budgetRules(count, "repo")
			entries[0].Instructions += strings.Repeat("x", 16384-legacyInstructionBytes(entries))
			if got := legacyInstructionBytes(entries); got != 16384 {
				t.Fatalf("legacy fixture is not exactly full: %d", got)
			}
			for _, over := range []bool{false, true} {
				if over {
					entries[0].Instructions += "x"
				}
				data, err := yaml.Marshal(&RepoConfig{Review: ReviewRaw{PathInstructions: entries}})
				if err != nil {
					t.Fatal(err)
				}
				repo, err := LoadRepoFromBytes(data)
				if over {
					if err == nil {
						t.Fatal("provenance allowance silently enlarged the prose budget")
					}
					continue
				}
				if err != nil {
					t.Fatalf("previously valid repo-only config rejected: %v", err)
				}
				if _, err := ResolveForRepository(DefaultGlobalConfig(), repo, "", ""); err != nil {
					t.Fatalf("repo-only resolver rejected old boundary: %v", err)
				}
			}
		})
	}
}
