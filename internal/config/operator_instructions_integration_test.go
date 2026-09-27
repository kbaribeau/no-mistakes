package config

import (
	"fmt"
	"path/filepath"
	"testing"
)

// The feature predates remote-keyed overrides and review.conversation. A daemon
// must resolve both identities without dropping either current-main behavior.
func TestMergeForRepository_OperatorGuidancePreservesCurrentPolicy(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "checkout")
	global := loadGlobalOrFail(t, fmt.Sprintf(`repository_overrides:
  https://github.com/example/repo.git:
    commit:
      fix_message: 'fix: {{.Summary}}'
    pr:
      title_format: '{{.Branch}}: {{.Title}}'
repo_instructions:
  %q:
    review:
      path_instructions:
        - path: 'internal/**'
          instructions: local review guidance
    document:
      instructions: local documentation guidance
`, checkout))
	repo := &RepoConfig{Review: ReviewRaw{Conversation: true}}

	cfg := MergeForRepository(global, repo, "git@github.com:example/repo.git", checkout)
	if cfg.Commit.FixMessage != "fix: {{.Summary}}" || cfg.PR.TitleFormat != "{{.Branch}}: {{.Title}}" {
		t.Fatalf("remote overrides lost: commit=%+v pr=%+v", cfg.Commit, cfg.PR)
	}
	if !cfg.Review.Conversation {
		t.Fatal("trusted review.conversation lost")
	}
	if len(cfg.Review.PathInstructions) != 1 || cfg.Review.PathInstructions[0].Source != InstructionSourceOperatorRepo {
		t.Fatalf("checkout guidance lost or misattributed: %+v", cfg.Review)
	}
	if len(cfg.Document.Instructions) != 1 || cfg.Document.Instructions[0].Source != InstructionSourceOperatorRepo {
		t.Fatalf("checkout document guidance lost or misattributed: %+v", cfg.Document)
	}

	// An operator rule alone must not opt a repository into a conversation.
	if cfg := MergeForRepository(global, &RepoConfig{}, "git@github.com:example/repo.git", checkout); cfg.Review.Conversation {
		t.Fatal("operator guidance enabled the trusted-only conversation")
	}
}

func TestLoadGlobal_OperatorReviewDoesNotGainRepositoryConversationControl(t *testing.T) {
	loadGlobalWantError(t, "review:\n  conversation: true\n", "conversation")
}
