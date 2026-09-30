package config

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEvalProvenance_SelectedHistoricalGuidanceSurvivesRecoveryReread(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "selected")
	other := filepath.Join(t.TempDir(), "unrelated-private-checkout")
	source := fmt.Sprintf(`agent: claude
review:
  path_instructions:
    - path: '*.go'
      instructions: global rule
repo_instructions:
  %q:
    review:
      path_instructions:
        - path: 'internal/**'
          instructions: historical checkout rule
    document:
      instructions: historical checkout document policy
  %q:
    review:
      path_instructions:
        - path: '*.secret'
          instructions: unrelated private rubric
`, checkout, other)
	global := loadGlobalOrFail(t, source)
	repo := &RepoConfig{
		Review:   ReviewRaw{Conversation: true, PathInstructions: []PathInstruction{{Path: "docs/**", Instructions: "trusted repository rule"}}},
		Document: DocumentRaw{Instructions: "trusted repository document policy"},
	}
	live, err := ResolveForRepository(global, repo, "", checkout)
	if err != nil {
		t.Fatal(err)
	}
	if err := live.EnableEvalProvenance(global, repo); err != nil {
		t.Fatal(err)
	}
	oldSnapshot := append([]byte(nil), live.ReplayGlobalYAML...)
	for _, unwanted := range []string{checkout, other, "unrelated-private-checkout", "unrelated private rubric", "repo_instructions:"} {
		if strings.Contains(string(oldSnapshot), unwanted) {
			t.Fatalf("snapshot retained unrelated guidance or a selection path %q", unwanted)
		}
	}
	replayed, err := LoadEvalConfig(oldSnapshot, live.ReplayRepoYAML)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed.Review, live.Review) || !reflect.DeepEqual(replayed.Document, live.Document) {
		t.Fatalf("guidance/order/sources changed: replay review=%+v document=%+v", replayed.Review, replayed.Document)
	}
	// Model recovery's actual resolver with changed operator config. It must
	// see edits without mutating the still-live executor or the older record.
	current := loadGlobalOrFail(t, strings.ReplaceAll(source, "historical checkout", "updated checkout"))
	recovered, err := ResolveForRepository(current, repo, "", checkout)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.EnableEvalProvenance(current, repo); err != nil {
		t.Fatal(err)
	}
	if got := recovered.Review.PathInstructions[1].Instructions; got != "updated checkout rule" {
		t.Fatalf("recovery did not reread: %q", got)
	}
	if got := live.Review.PathInstructions[1].Instructions; got != "historical checkout rule" {
		t.Fatalf("uninterrupted config changed: %q", got)
	}
	again, err := LoadEvalConfig(oldSnapshot, live.ReplayRepoYAML)
	if err != nil || !reflect.DeepEqual(again.Review, live.Review) || !reflect.DeepEqual(oldSnapshot, live.ReplayGlobalYAML) {
		t.Fatalf("older round no longer preserves its historical input: %v", err)
	}
	// Snapshot metadata must not become a user-controlled config surface.
	loadGlobalWantError(t, string(oldSnapshot), evalGuidanceKey, "not found in type")
}

func TestPrepareEvalGlobal_LegacyWithoutCheckoutGuidanceIsUnambiguous(t *testing.T) {
	global := []byte("review:\n  path_instructions:\n    - path: '*.go'\n      instructions: old global rule\n")
	repo := []byte("review:\n  path_instructions:\n    - path: 'docs/**'\n      instructions: old repository rule\n")
	prepared, err := PrepareEvalGlobal(global, repo)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadEvalConfig(prepared, repo)
	if err != nil || len(cfg.Review.PathInstructions) != 2 {
		t.Fatalf("legacy round upgrade: cfg=%+v err=%v", cfg, err)
	}
	if cfg.Review.PathInstructions[0].Source != InstructionSourceOperatorGlobal || cfg.Review.PathInstructions[1].Source != InstructionSourceRepository {
		t.Fatalf("legacy source labels lost: %+v", cfg.Review.PathInstructions)
	}
	again, err := PrepareEvalGlobal(prepared, repo)
	if err != nil || string(again) != string(prepared) {
		t.Fatalf("snapshot preparation not idempotent: %v", err)
	}
	// The same bytes in an already-exported legacy CASE are not proof: its
	// checkout map might already have been stripped by the old exporter.
	if _, err := LoadEvalConfig(global, repo); err == nil {
		t.Fatal("case without a snapshot replayed silently")
	}
}

func TestLoadEvalConfig_RefusesIncompleteOrInvalidGuidanceSnapshot(t *testing.T) {
	valid := "no_mistakes_selected_guidance:\n  version: 1\n  review: []\n  document: []\n"
	if _, err := LoadEvalConfig([]byte(valid), []byte("{}\n")); err != nil {
		t.Fatalf("explicit empty snapshot should be valid: %v", err)
	}
	for _, tc := range []struct{ name, data string }{
		{"missing snapshot", "{}\n"},
		{"unknown version", strings.Replace(valid, "version: 1", "version: 2", 1)},
		{"missing review list", strings.Replace(valid, "  review: []\n", "", 1)},
		{"missing document list", strings.Replace(valid, "  document: []\n", "", 1)},
		{"unknown field", valid + "  typo: wrong\n"},
		{"unstamped rule", strings.Replace(valid, "review: []", "review:\n    - path: '*.go'\n      instructions: rule", 1)},
		{"forged source", strings.Replace(valid, "review: []", "review:\n    - path: '*.go'\n      instructions: rule\n      source: someone else", 1)},
		{"unknown nested field", strings.Replace(valid, "review: []", "review:\n    - path: '*.go'\n      instruction: ignored", 1)},
		{"checkout map retained", valid + "repo_instructions: {}\n"},
		{"global document snapshot", strings.Replace(valid, "document: []", "document:\n    - text: invalid scope\n      source: operator configuration for every repository", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadEvalConfig([]byte(tc.data), []byte("{}\n")); err == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
	var rules strings.Builder
	for i := 0; i < 33; i++ {
		fmt.Fprintf(&rules, "    - path: '*.go'\n      instructions: rule\n      source: %s\n", InstructionSourceRepository)
	}
	oversized := strings.Replace(valid, "  review: []\n", "  review:\n"+rules.String(), 1)
	if _, err := LoadEvalConfig([]byte(oversized), []byte("{}\n")); err == nil || !strings.Contains(err.Error(), "33 entries") {
		t.Fatalf("snapshot bypassed shared budget: %v", err)
	}
}
