package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
)

func TestReplayConfig_PreservesSelectedGuidanceThroughAgentNeutralCapture(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "registered-checkout")
	other := filepath.Join(t.TempDir(), "unrelated-checkout")
	global, err := config.LoadGlobalFromBytes([]byte(fmt.Sprintf(`agent: codex
agent_config:
  codex:
    model: old-model
review:
  path_instructions:
    - path: '*.go'
      instructions: global historical rule
document:
  instructions: global historical document policy
repo_instructions:
  %q:
    review:
      path_instructions:
        - path: 'internal/**'
          instructions: scoped historical rule
    document:
      instructions: scoped document policy
  %q:
    document:
      instructions: unrelated private guidance
`, checkout, other)))
	if err != nil {
		t.Fatal(err)
	}
	repo := &config.RepoConfig{
		Review:   config.ReviewRaw{Conversation: true, PathInstructions: []config.PathInstruction{{Path: "docs/**", Instructions: "trusted historical rule"}}},
		Document: config.DocumentRaw{Instructions: "trusted document policy"},
	}
	live, err := config.ResolveForRepository(global, repo, "", checkout)
	if err != nil {
		t.Fatal(err)
	}
	if len(live.Document.Instructions) != 3 || live.Document.Instructions[0].Source != config.InstructionSourceOperatorGlobal {
		t.Fatalf("global document policy missing before capture: %+v", live.Document)
	}
	if err := live.EnableEvalProvenance(global, repo); err != nil {
		t.Fatal(err)
	}
	neutral, err := agentNeutralGlobalConfig(live.ReplayGlobalYAML, live.ReplayRepoYAML)
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"registered-checkout", "unrelated-checkout", "unrelated private guidance", "old-model", "agent_config:", "repo_instructions:"} {
		if strings.Contains(string(neutral), unwanted) {
			t.Fatalf("capture retained %q", unwanted)
		}
	}
	c := Case{Dir: t.TempDir()}
	configDir := filepath.Join(c.Dir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"global.yaml": neutral, "repo-config.yaml": live.ReplayRepoYAML} {
		if err := os.WriteFile(filepath.Join(configDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Today's machine config must not enter replay at all.
	home := t.TempDir()
	t.Setenv("NM_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("not: valid configuration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	replayed, err := replayConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed.Review, live.Review) || !reflect.DeepEqual(replayed.Document, live.Document) {
		t.Fatalf("replay changed historical guidance: review=%+v document=%+v", replayed.Review, replayed.Document)
	}
	if len(replayed.AgentConfig) != 0 {
		t.Fatal("captured model leaked into the candidate config")
	}
	// Already-exported legacy cases cannot be assumed faithful: the old
	// capture path may have deleted a nonempty checkout map.
	if err := os.WriteFile(filepath.Join(configDir, "global.yaml"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := replayConfig(c); err == nil || !strings.Contains(err.Error(), "no selected-guidance snapshot") {
		t.Fatalf("legacy case silently replayed without its rubric: %v", err)
	}
	if err := os.Remove(filepath.Join(configDir, "global.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := replayConfig(c); err == nil || !strings.Contains(err.Error(), "read captured global config") {
		t.Fatalf("missing case config fell back to live defaults: %v", err)
	}
}
