package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadGlobal_DocumentGuidanceRequiresACheckout(t *testing.T) {
	loadGlobalWantError(t, "document:\n  instructions: all projects use docs/config.md\n", "document", "not found in type")
	global := loadGlobalOrFail(t, "agent: claude\n")
	if global.Agent != "claude" {
		t.Fatalf("legitimate top-level agent setting lost: %q", global.Agent)
	}
	repo := &RepoConfig{Document: DocumentRaw{Instructions: "repository policy"}}
	cfg := Merge(global, repo)
	if len(cfg.Document.Instructions) != 1 || cfg.Document.Instructions[0].Source != InstructionSourceRepository {
		t.Fatalf("trusted repository document policy lost: %+v", cfg.Document)
	}
}

// A custom Node.Decode inside RepoInstructions used to escape the outer
// KnownFields(true). Keep this at the real global loader boundary, including
// aliases/merge keys, not just a direct struct decoder exercise.
func TestLoadGlobal_RepoInstructionsRejectsNestedUnknownKeys(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "checkout")
	for _, tc := range []struct{ name, block, field string }{
		{"document typo", "document:\n  instruction: hidden rule\n", "instruction"},
		{"document extra", "document:\n  instructions: valid\n  commands: ignored\n", "commands"},
		{"review typo", "review:\n  path_instruction: []\n", "path_instruction"},
		{"trusted-only conversation", "review:\n  conversation: true\n", "conversation"},
		{"rule typo", "review:\n  path_instructions:\n    - path: '*.go'\n      instruction: hidden rule\n", "instruction"},
		{"forged provenance", "review:\n  path_instructions:\n    - path: '*.go'\n      instructions: valid\n      source: operator\n", "source"},
		{"merged unknown key", "document:\n  <<: &policy\n    instruction: hidden rule\n  instructions: valid\n", "instruction"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block := "    " + strings.ReplaceAll(strings.TrimSuffix(tc.block, "\n"), "\n", "\n    ") + "\n"
			loadGlobalWantError(t, "repo_instructions:\n  "+yamlPath(checkout)+":\n"+block, tc.field, "not found in type")
		})
	}
}

func TestLoadGlobal_RepoInstructionsStrictnessPreservesValidAliases(t *testing.T) {
	first := filepath.Join(t.TempDir(), "one")
	second := filepath.Join(t.TempDir(), "two")
	global := loadGlobalOrFail(t, fmt.Sprintf(`repo_instructions:
  %q: &guidance
    review:
      path_instructions:
        - path: '*.go'
          instructions: check errors
    document:
      instructions: keep the reference accurate
  %q: *guidance
`, first, second))
	for _, checkout := range []string{first, second} {
		cfg := MergeForCheckout(global, &RepoConfig{}, checkout)
		if len(cfg.Review.PathInstructions) != 1 || len(cfg.Document.Instructions) != 1 {
			t.Fatalf("valid alias lost guidance for %q: review=%+v document=%+v", checkout, cfg.Review, cfg.Document)
		}
	}
}
