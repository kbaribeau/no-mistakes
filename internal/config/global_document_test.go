package config

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGlobalDocumentInstructions_EmptyAndUnusable(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		wantError   bool
	}{
		{"absent", "{}\n", false},
		{"empty", "document:\n  instructions: ''\n", false},
		{"whitespace", "document:\n  instructions: '   '\n", false},
		{"conflict markers", "document:\n  instructions: '<<<<<<< ======= >>>>>>>'\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			global, err := LoadGlobalFromBytes([]byte(tc.value))
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "document.instructions is left empty") {
					t.Fatalf("want explicit unusable-policy error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := Merge(global, &RepoConfig{}).Document.Instructions; len(got) != 0 {
				t.Fatalf("empty guidance must leave built-in policy alone: %+v", got)
			}
		})
	}
}

func TestGlobalDocumentInstructions_ValidAliasKeepsBothSources(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "checkout")
	global, err := LoadGlobalFromBytes([]byte(fmt.Sprintf(`document: &policy
  instructions: Keep each fact with its authoritative owner.
repo_instructions:
  %q:
    document: *policy
`, checkout)))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveForRepository(global, &RepoConfig{}, "", checkout)
	if err != nil {
		t.Fatal(err)
	}
	want := []DocumentInstruction{
		{Source: InstructionSourceOperatorGlobal, Text: global.Document.Instructions},
		{Source: InstructionSourceOperatorRepo, Text: global.Document.Instructions},
	}
	if !reflect.DeepEqual(cfg.Document.Instructions, want) {
		t.Fatalf("aliased document blocks lost provenance: %+v", cfg.Document)
	}
}

func TestGlobalDocumentInstructions_KeepRepositoryTrustBoundary(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "checkout")
	global := DefaultGlobalConfig()
	global.Document = DocumentRaw{Instructions: "global policy"}
	global.RepoInstructions = map[string]RepoInstructions{
		checkout: {Document: DocumentRaw{Instructions: "checkout policy"}},
	}
	pushed := &RepoConfig{Document: DocumentRaw{Instructions: "discard documentation duties"}}
	trusted := &RepoConfig{Document: DocumentRaw{Instructions: "trusted policy"}}
	for _, allowCommands := range []bool{false, true} {
		for _, trustedCopy := range []*RepoConfig{trusted, nil} {
			effective := EffectiveRepoConfig(pushed, trustedCopy, allowCommands)
			cfg, err := ResolveForRepository(global, effective, "", checkout)
			if err != nil {
				t.Fatal(err)
			}
			want := []DocumentInstruction{
				{Source: InstructionSourceOperatorGlobal, Text: "global policy"},
				{Source: InstructionSourceOperatorRepo, Text: "checkout policy"},
			}
			if trustedCopy != nil {
				want = append(want, DocumentInstruction{Source: InstructionSourceRepository, Text: "trusted policy"})
			}
			if !reflect.DeepEqual(cfg.Document.Instructions, want) {
				t.Fatalf("allowCommands=%v trusted=%v: document trust boundary changed: %+v", allowCommands, trustedCopy != nil, cfg.Document)
			}
		}
	}
}

// Document has no byte ceiling, independently of Review's unchanged quotas.
// Preserve all three blocks through parsing, selection and historical replay;
// this is not a claim that an arbitrary-sized prompt fits any particular model.
func TestGlobalDocumentInstructions_UncappedAndIndependentOfReview(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "checkout")
	large := strings.Repeat("x", 65536)
	global, err := LoadGlobalFromBytes([]byte(fmt.Sprintf(`document:
  instructions: %q
repo_instructions:
  %q:
    document:
      instructions: %q
`, "global "+large, checkout, "checkout "+large)))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := LoadRepoFromBytes([]byte(fmt.Sprintf("document:\n  instructions: %q\n", "repository "+large)))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveForRepository(global, repo, "", checkout)
	if err != nil {
		t.Fatal(err)
	}
	want := []DocumentInstruction{
		{Source: InstructionSourceOperatorGlobal, Text: "global " + large},
		{Source: InstructionSourceOperatorRepo, Text: "checkout " + large},
		{Source: InstructionSourceRepository, Text: "repository " + large},
	}
	if !reflect.DeepEqual(cfg.Document.Instructions, want) {
		t.Fatal("document policies were omitted or shortened")
	}
	if len(cfg.Review.PathInstructions) != 0 {
		t.Fatal("document policy entered the review quota")
	}
	if err := cfg.EnableEvalProvenance(global, repo); err != nil {
		t.Fatal(err)
	}
	replayed, err := LoadEvalConfig(cfg.ReplayGlobalYAML, cfg.ReplayRepoYAML)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed.Document.Instructions, want) {
		t.Fatal("replay omitted or shortened document policy")
	}
}
