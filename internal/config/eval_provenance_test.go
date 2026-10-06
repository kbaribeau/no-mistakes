package config

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const guidanceGlobal = `review:
  path_instructions:
    - {path: '*.go', instructions: shared rule}
repository_overrides:
  https://github.com/example/selected:
    review:
      path_instructions:
        - {path: '*.go', instructions: shared rule}
        - {path: 'docs/**', instructions: selected unmatched rule}
  https://github.com/example/empty:
    review: {path_instructions: []}
  https://github.com/example/other:
    review:
      path_instructions:
        - {path: '*.go', instructions: unselected rule}
`

const guidanceRepo = `review:
  conversation: true
  path_instructions:
    - {path: '*.go', instructions: shared rule}
`

func TestEvalProvenanceSelectedReviewGuidance(t *testing.T) {
	for _, tc := range []struct {
		name, remote string
		count        int
	}{
		{"selected", "git@github.com:example/selected.git", 2},
		{"explicit empty", "https://github.com/example/empty", 0},
		{"no matching remote", "https://github.com/example/absent", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			global, repo := guidanceConfigs(t, guidanceGlobal, guidanceRepo)
			live := MergeForRemote(global, repo, tc.remote)
			originalGlobal := append([]byte(nil), global.SourceYAML...)
			if err := live.EnableEvalProvenance(global, repo); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(global.SourceYAML, originalGlobal) {
				t.Fatal("writer mutated original global config")
			}
			if tc.count == 0 && !bytes.Contains(live.ReplayGlobalYAML, []byte("repository_path_instructions: []")) {
				t.Fatalf("no explicit empty selection: %s", live.ReplayGlobalYAML)
			}
			if _, err := LoadGlobalFromBytes(live.ReplayGlobalYAML); err == nil {
				t.Fatal("private metadata became a public config setting")
			}
			for _, exported := range []bool{false, true} {
				data := live.ReplayGlobalYAML
				if exported {
					var err error
					data, err = PrepareEvalGlobal(data)
					if err != nil {
						t.Fatal(err)
					}
					if bytes.Contains(data, []byte("unselected rule")) {
						t.Fatal("export retained unselected Review authority")
					}
				}
				got, err := LoadEvalConfig(data, live.ReplayRepoYAML)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got.Review, live.Review) {
					t.Fatalf("exported=%v: got %#v want %#v", exported, got.Review, live.Review)
				}
				if len(got.Review.RepositoryPathInstructions) != tc.count {
					t.Fatal("wrong selected list")
				}
				var labels []string
				for _, source := range got.Review.PathInstructionSources() {
					labels = append(labels, source.Label)
				}
				wantLabels := []string{"machine-local global", "trusted"}
				if tc.count > 0 {
					wantLabels = []string{"machine-local global", "machine-local per-repository", "trusted"}
				}
				if !reflect.DeepEqual(labels, wantLabels) {
					t.Fatalf("source order = %v, want %v", labels, wantLabels)
				}
			}
		})
	}
}

func TestEvalProvenancePreservesPublicYAMLAliases(t *testing.T) {
	// Sorting top-level YAML nodes would put repository_overrides BEFORE the
	// anchor it references. Public config aliases remain valid for provenance.
	global, repo := guidanceConfigs(t, `review: &rules
  path_instructions: [{path: '*.go', instructions: shared rule}]
repository_overrides:
  https://github.com/example/selected:
    review: *rules
`, "{}\n")
	live := MergeForRemote(global, repo, "https://github.com/example/selected")
	if err := live.EnableEvalProvenance(global, repo); err != nil {
		t.Fatal(err)
	}
	exported, err := PrepareEvalGlobal(live.ReplayGlobalYAML)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadEvalConfig(exported, live.ReplayRepoYAML)
	if err != nil || !reflect.DeepEqual(got.Review, live.Review) {
		t.Fatalf("aliased config changed: %#v / %v", got, err)
	}
}

func TestEvalProvenanceRecordsResolvedSelectionRatherThanReselecting(t *testing.T) {
	global, repo := guidanceConfigs(t, guidanceGlobal, guidanceRepo)
	live := MergeForRemote(global, repo, "https://github.com/example/selected")
	// Deliberately distinguish the already-resolved list from the raw map.
	live.Review.RepositoryPathInstructions[0].Instructions = "resolved historical rule"
	if err := live.EnableEvalProvenance(global, repo); err != nil {
		t.Fatal(err)
	}
	global.Review.PathInstructions[0].Instructions = "changed after recording"
	live.Review.RepositoryPathInstructions[0].Instructions = "changed after recording"
	got, err := LoadEvalConfig(live.ReplayGlobalYAML, live.ReplayRepoYAML)
	if err != nil {
		t.Fatal(err)
	}
	if got.Review.RepositoryPathInstructions[0].Instructions != "resolved historical rule" || got.Review.GlobalPathInstructions[0].Instructions != "shared rule" {
		t.Fatalf("snapshot was not immutable/resolved: %#v", got.Review)
	}
}

func TestEvalProvenanceLegacyCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		allowed    bool
	}{
		{"empty", "{}\n", true},
		{"null public config", "null\n", true},
		{"global only", "review:\n  path_instructions: [{path: '*.go', instructions: legacy global}]\n", true},
		{"empty remote rules", "repository_overrides:\n  https://github.com/example/repo:\n    review: {path_instructions: []}\n", true},
		{"unrelated remote fields", "repository_overrides:\n  https://github.com/example/repo:\n    commands: {test: {additional: ['go test ./...']}}\n    document: {instructions: keep docs concise}\n", true},
		{"one nonempty remote", "repository_overrides:\n  https://github.com/example/repo:\n    review:\n      path_instructions: [{path: '*.go', instructions: ambiguous}]\n", false},
		{"several remotes", guidanceGlobal, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(tc.data)
			got, err := LoadEvalConfig(data, []byte(guidanceRepo))
			prepared, captureErr := PrepareEvalGlobal(data)
			if !tc.allowed {
				for _, err := range []error{err, captureErr} {
					if err == nil || !strings.Contains(err.Error(), "selected per-repository Review guidance was not recorded") || !strings.Contains(err.Error(), "capture a new review") {
						t.Fatalf("missing actionable ambiguity error: %v", err)
					}
				}
			} else {
				if err != nil || captureErr != nil {
					t.Fatalf("legacy rejected: %v / %v", err, captureErr)
				}
				if !got.Review.Conversation || len(got.Review.PathInstructions) != 1 {
					t.Fatalf("lost trusted config: %#v", got.Review)
				}
				upgraded, err := LoadEvalConfig(prepared, []byte(guidanceRepo))
				if err != nil || !reflect.DeepEqual(got.Review, upgraded.Review) {
					t.Fatalf("legacy upgrade changed Review: %#v / %v", upgraded, err)
				}
				if !bytes.Contains(prepared, []byte("repository_path_instructions: []")) {
					t.Fatal("legacy capture did not record explicit empty selection")
				}
			}
			if string(data) != tc.data {
				t.Fatal("legacy artifact changed")
			}
		})
	}
}

func TestEvalProvenanceRejectsInvalidSelectedReviewGuidance(t *testing.T) {
	for _, tc := range []struct{ name, payload string }{
		{"null snapshot", "null"},
		{"scalar snapshot", "broken"},
		{"sequence snapshot", "[]"},
		{"missing version", "{repository_path_instructions: []}"},
		{"unknown version", "{version: 2, repository_path_instructions: []}"},
		{"null version", "{version: null, repository_path_instructions: []}"},
		{"string version", "{version: '1', repository_path_instructions: []}"},
		{"float version", "{version: 1.0, repository_path_instructions: []}"},
		{"overflow version", "{version: 9999999999999999999999999, repository_path_instructions: []}"},
		{"unknown field", "{version: 1, repository_path_instructions: [], remote: today}"},
		{"duplicate field", "{version: 1, version: 1, repository_path_instructions: []}"},
		{"missing list", "{version: 1}"},
		{"null list", "{version: 1, repository_path_instructions: null}"},
		{"scalar list", "{version: 1, repository_path_instructions: nope}"},
		{"null rule", "{version: 1, repository_path_instructions: [null]}"},
		{"unknown rule field", "{version: 1, repository_path_instructions: [{path: '*.go', instructions: hi, source: trusted}]}"},
		{"missing path", "{version: 1, repository_path_instructions: [{instructions: hi}]}"},
		{"empty text", "{version: 1, repository_path_instructions: [{path: '*.go', instructions: ' '}]}"},
		{"null text", "{version: 1, repository_path_instructions: [{path: '*.go', instructions: null}]}"},
		{"numeric text", "{version: 1, repository_path_instructions: [{path: '*.go', instructions: 123}]}"},
		{"invalid glob", "{version: 1, repository_path_instructions: [{path: '[', instructions: hi}]}"},
		{"conflict markers only", "{version: 1, repository_path_instructions: [{path: '*.go', instructions: '======='}]}"},
		{"malformed", "{"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertEvalGuidanceRejected(t, []byte(selectedReviewGuidanceKey+": "+tc.payload+"\n"))
		})
	}
	for _, data := range []string{
		"", "[]\n", "{}\n---\n{}\n", "unknown_global_key: true\n",
		privateSelectedGuidanceKey + ": {version: 1, review: [], document: []}\n",
		selectedReviewGuidanceKey + ": {version: 1, repository_path_instructions: []}\n" + privateSelectedGuidanceKey + ": {}\n",
	} {
		assertEvalGuidanceRejected(t, []byte(data))
	}
}

func TestEvalProvenanceKeepsTheExistingCombinedBudget(t *testing.T) {
	globalYAML := "review:\n  path_instructions:\n" + guidanceRulesYAML("    ", 20)
	global, repo := guidanceConfigs(t, globalYAML, "{}\n")
	cfg := Merge(global, repo)
	if err := cfg.EnableEvalProvenance(global, repo); err != nil {
		t.Fatalf("ordinary 20-rule global config rejected: %v", err)
	}
	got, err := LoadEvalConfig(cfg.ReplayGlobalYAML, cfg.ReplayRepoYAML)
	if err != nil || len(got.Review.GlobalPathInstructions) != 20 {
		t.Fatalf("20-rule replay: %#v / %v", got, err)
	}

	for _, tc := range []struct{ name, global, selected, repo string }{
		{"selected count", "", guidanceRulesYAML("    ", 33), "{}\n"},
		{"combined count", globalYAML, guidanceRulesYAML("    ", 12), guidanceRepo},
		{"selected bytes", "", "    - {path: '*.go', instructions: '" + strings.Repeat("x", MaxReviewPathInstructionsBytes) + "'}\n", "{}\n"},
		{"combined bytes", "review:\n  path_instructions:\n    - {path: '*.go', instructions: '" + strings.Repeat("g", 7500) + "'}\n", "    - {path: '*.go', instructions: '" + strings.Repeat("s", 7500) + "'}\n", guidanceRepo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := tc.global + selectedReviewGuidanceKey + ":\n  version: 1\n  repository_path_instructions:\n" + tc.selected
			if _, err := LoadEvalConfig([]byte(data), []byte(tc.repo)); err == nil {
				t.Fatal("accepted budget overflow")
			}
		})
	}
	cfg.Review.RepositoryPathInstructions = []PathInstruction{{Path: "[", Instructions: "bad"}}
	if err := cfg.EnableEvalProvenance(global, repo); err == nil {
		t.Fatal("writer recorded malformed resolved guidance")
	}
}

func guidanceConfigs(t *testing.T, globalYAML, repoYAML string) (*GlobalConfig, *RepoConfig) {
	t.Helper()
	global, err := LoadGlobalFromBytes([]byte(globalYAML))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := LoadRepoFromBytes([]byte(repoYAML))
	if err != nil {
		t.Fatal(err)
	}
	return global, repo
}

func guidanceRulesYAML(indent string, count int) string {
	var b strings.Builder
	for i := 0; i < count; i++ {
		fmt.Fprintf(&b, "%s- {path: '*.go', instructions: rule %d}\n", indent, i)
	}
	return b.String()
}

func assertEvalGuidanceRejected(t *testing.T, data []byte) {
	t.Helper()
	original := append([]byte(nil), data...)
	if _, err := LoadEvalConfig(data, []byte("{}\n")); err == nil {
		t.Fatalf("replay accepted invalid snapshot: %s", data)
	}
	if _, err := PrepareEvalGlobal(data); err == nil {
		t.Fatalf("capture accepted invalid snapshot: %s", data)
	}
	if !bytes.Equal(data, original) {
		t.Fatal("rejected artifact was modified")
	}
}
