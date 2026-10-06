package eval

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/kunchenguid/no-mistakes/internal/config"
)

// Regression for the public capture/replay path dropping the resolved remote
// selection while preserving the global and trusted rules.
func TestReplayPreservesSelectedReviewGuidance(t *testing.T) {
	global, err := config.LoadGlobalFromBytes([]byte(`review:
  path_instructions:
    - {path: '*.go', instructions: global rule}
repository_overrides:
  https://github.com/example/selected:
    review:
      path_instructions:
        - {path: '*.go', instructions: selected rule}
        - {path: 'docs/**', instructions: selected but unmatched rule}
  https://github.com/example/other:
    review:
      path_instructions:
        - {path: '*.go', instructions: unselected rule}
`))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := config.LoadRepoFromBytes([]byte("review:\n  conversation: true\n  path_instructions:\n    - {path: '*.go', instructions: trusted rule}\n"))
	if err != nil {
		t.Fatal(err)
	}
	live := config.MergeForRemote(global, repo, "git@github.com:example/selected.git")
	if len(live.Review.RepositoryPathInstructions) != 2 {
		t.Fatalf("live selection = %#v", live.Review)
	}
	if err := live.EnableEvalProvenance(global, repo); err != nil {
		t.Fatal(err)
	}
	neutral, err := agentNeutralGlobalConfig(live.ReplayGlobalYAML)
	if err != nil {
		t.Fatal(err)
	}
	c := guidanceCase(t, neutral, live.ReplayRepoYAML)
	replayed, err := replayConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed.Review, live.Review) {
		t.Fatalf("replayed Review = %#v; want historical inputs %#v", replayed.Review, live.Review)
	}
}

func guidanceCase(t *testing.T, global, repo []byte) Case {
	t.Helper()
	c := Case{Dir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(c.Dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"global.yaml": global, "repo-config.yaml": repo} {
		if err := os.WriteFile(filepath.Join(c.Dir, "config", name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestCaptureSelectedReviewGuidanceIsCandidateNeutral(t *testing.T) {
	global, err := config.LoadGlobalFromBytes([]byte(`agent: codex
agent_args_override: {codex: [-m, historical-model]}
agent_config: {codex: {model: historical-model, effort: high}}
review_agents:
  reviewer: {agent: pi, model: historical-reviewer, effort: max}
  fixer: {agent: pi, model: historical-fixer, effort: high}
log_level: warn
review:
  path_instructions: [{path: '*.go', instructions: global rule}]
repository_overrides:
  https://github.com/example/selected:
    review:
      path_instructions: [{path: '*.go', instructions: selected rule}]
    document: {instructions: unrelated document policy}
    commands: {test: {additional: ['go test ./...']}}
    pr: {title_format: 'topic: {title}'}
  https://github.com/example/other:
    review:
      path_instructions: [{path: '*.go', instructions: unselected secret rule}]
`))
	if err != nil {
		t.Fatal(err)
	}
	repo := &config.RepoConfig{}
	cfg := config.MergeForRemote(global, repo, "https://github.com/example/selected")
	if err := cfg.EnableEvalProvenance(global, repo); err != nil {
		t.Fatal(err)
	}
	original := append([]byte(nil), cfg.ReplayGlobalYAML...)
	neutral, err := agentNeutralGlobalConfig(original)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(neutral, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"agent", "agent_args_override", "agent_config", "review_agents"} {
		if _, ok := raw[key]; ok {
			t.Errorf("export retained harness pin %q", key)
		}
	}
	for _, value := range []string{"unselected secret rule", "historical-model", "historical-reviewer", "historical-fixer"} {
		if strings.Contains(string(neutral), value) {
			t.Errorf("export retained %q", value)
		}
	}
	for _, value := range []string{"global rule", "selected rule", "unrelated document policy", "go test ./...", "topic: {title}", "log_level: warn"} {
		if !strings.Contains(string(neutral), value) {
			t.Errorf("export lost %q", value)
		}
	}
	overrides := raw["repository_overrides"].(map[string]any)
	for _, value := range overrides {
		if _, ok := value.(map[string]any)["review"]; ok {
			t.Fatal("raw override remains a second Review authority")
		}
	}
	got, err := config.LoadEvalConfig(neutral, cfg.ReplayRepoYAML)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Review, cfg.Review) {
		t.Fatalf("capture changed Review: %#v", got.Review)
	}
	if !bytes.Equal(cfg.ReplayGlobalYAML, original) {
		t.Fatal("capture modified original round")
	}
	second, err := agentNeutralGlobalConfig(neutral)
	if err != nil || !bytes.Equal(neutral, second) {
		t.Fatalf("capture is not idempotent: %v", err)
	}
}

func TestReplayGuidanceMissingOrUnsupportedArtifactsStayUnchanged(t *testing.T) {
	for _, tc := range []struct{ name, global, remove, want string }{
		{"missing global", "{}\n", "global.yaml", "read captured global config"},
		{"missing repo", "{}\n", "repo-config.yaml", "read captured repo config"},
		{"private snapshot", "no_mistakes_selected_guidance: {version: 1, review: [], document: []}\n", "", "unsupported private"},
		{"ambiguous legacy", "repository_overrides:\n  https://github.com/example/repo:\n    review:\n      path_instructions: [{path: '*.go', instructions: historical}]\n", "", "capture a new review"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := guidanceCase(t, []byte(tc.global), []byte("{}\n"))
			if tc.remove != "" {
				if err := os.Remove(filepath.Join(c.Dir, "config", tc.remove)); err != nil {
					t.Fatal(err)
				}
			}
			before := guidanceArtifactBytes(t, c)
			if _, err := replayConfig(c); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if after := guidanceArtifactBytes(t, c); !reflect.DeepEqual(before, after) {
				t.Fatal("replay changed rejected artifacts")
			}
		})
	}
}

func guidanceArtifactBytes(t *testing.T, c Case) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, name := range []string{"global.yaml", "repo-config.yaml"} {
		data, err := os.ReadFile(filepath.Join(c.Dir, "config", name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(data)
	}
	return out
}
