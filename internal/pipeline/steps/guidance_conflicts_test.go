package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func assertGuidanceConflictProtocol(t *testing.T, prompt string) {
	t.Helper()
	for _, want := range []string{
		"Operator guidance supplements trusted repository requirements; it must not override them or weaken the built-in defaults",
		"Apply all compatible requirements, regardless of source order; order is not last-writer-wins",
		`severity "warning" and action "ask-user"`,
		"naming the conflicting requirements, their source labels, and the decision needed",
		"Do not silently choose one or drop either requirement",
		"do not make edits that depend on resolving the conflict before that decision",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("emitted prompt lost %q", want)
		}
	}
	if got := strings.Count(prompt, "Supplemental guidance:"); got != 1 {
		t.Errorf("protocol count = %d, want exactly one", got)
	}
}

// Test the resolved-source interface, including EvalReplay: historical input
// restoration belongs to eval, but replay must use today's prompt behavior.
func TestReviewStep_ConflictProtocolRequiresRenderedOperatorGuidance(t *testing.T) {
	t.Parallel()
	unconfigured := reviewPromptFor(t, nil)
	base := strings.TrimSuffix(unconfigured, agent.MemoryFilesRule)
	rule := config.PathInstruction{Path: "*.txt", Instructions: "Preserve the fixture format."}
	unmatched := config.PathInstruction{Path: "docs/**", Instructions: "Unmatched operator rule."}
	trusted := config.PathInstruction{Path: "*.txt", Instructions: "Keep fixture contents accurate."}
	trustedSection := wantSection(wantBlock(trusted.Path, "feature.txt", trusted.Instructions))
	globalSection := "\n\n" + config.ReviewGlobalPathInstructionsHeading + "\n" + wantBlock(rule.Path, "feature.txt", rule.Instructions)
	remoteSection := "\n\n" + config.ReviewRepositoryPathInstructionsHeading + "\n" + wantBlock(rule.Path, "feature.txt", rule.Instructions)

	cases := []struct {
		name     string
		review   config.Review
		sections string
		protocol bool
	}{
		{name: "unconfigured"},
		{name: "explicit empty selection", review: config.Review{RepositoryPathInstructions: []config.PathInstruction{}}},
		{name: "trusted only", review: config.Review{PathInstructions: []config.PathInstruction{trusted}}, sections: trustedSection},
		{name: "global unmatched", review: config.Review{GlobalPathInstructions: []config.PathInstruction{unmatched}}},
		{name: "selected remote unmatched", review: config.Review{RepositoryPathInstructions: []config.PathInstruction{unmatched}, PathInstructions: []config.PathInstruction{trusted}}, sections: trustedSection},
		{name: "matching global empty remote", review: config.Review{GlobalPathInstructions: []config.PathInstruction{rule}, RepositoryPathInstructions: []config.PathInstruction{}}, sections: globalSection, protocol: true},
		{name: "matching selected remote", review: config.Review{RepositoryPathInstructions: []config.PathInstruction{rule}, PathInstructions: []config.PathInstruction{trusted}}, sections: remoteSection + trustedSection, protocol: true},
		{name: "all sources", review: config.Review{GlobalPathInstructions: []config.PathInstruction{rule}, RepositoryPathInstructions: []config.PathInstruction{rule}, PathInstructions: []config.PathInstruction{trusted}}, sections: globalSection + remoteSection + trustedSection, protocol: true},
		{name: "unusable operator text", review: config.Review{GlobalPathInstructions: []config.PathInstruction{{Path: "*.txt", Instructions: "======="}}, RepositoryPathInstructions: []config.PathInstruction{{Path: "*.txt", Instructions: "  "}}, PathInstructions: []config.PathInstruction{trusted}}, sections: trustedSection},
	}
	for _, tc := range cases {
		for _, replay := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/replay=%v", tc.name, replay), func(t *testing.T) {
				t.Parallel()
				dir, baseSHA, headSHA := setupGitRepo(t)
				ag := newStaticReviewAgent(cleanReviewJSON)
				sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
				sctx.Config.Review = tc.review
				sctx.EvalReplay = replay
				out, err := (&ReviewStep{}).Execute(sctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(ag.calls) != 1 || out.NeedsApproval {
					t.Fatalf("clean review: calls=%d, outcome=%+v", len(ag.calls), out)
				}
				prompt := strings.ReplaceAll(ag.calls[0].Prompt, dir, "<WORKDIR>")
				want := base + tc.sections
				if tc.protocol {
					assertGuidanceConflictProtocol(t, prompt)
					want += supplementalGuidanceRule
				}
				want += agent.MemoryFilesRule
				if prompt != want {
					t.Fatalf("prompt changed beyond the applicable source sections and protocol:\n%s", prompt)
				}
			})
		}
	}
}

func TestReviewStep_ConflictProtocolLeavesConversationLast(t *testing.T) {
	t.Parallel()
	run := func(conversation bool) string {
		dir, baseSHA, headSHA := setupGitRepo(t)
		ag := newStaticReviewAgent(cleanReviewJSON)
		sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
		sctx.Config.Review.GlobalPathInstructions = []config.PathInstruction{{Path: "*.txt", Instructions: "Preserve fixture format."}}
		if conversation {
			enableReviewConversation(sctx.Config)
		}
		if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
			t.Fatal(err)
		}
		prompt := strings.ReplaceAll(lastReviewPrompt(t, ag), dir, "<WORKDIR>")
		return strings.ReplaceAll(prompt, sctx.EvidenceDir, "<EVIDENCE>")
	}
	off, on := run(false), run(true)
	assertGuidanceConflictProtocol(t, off)
	assertGuidanceConflictProtocol(t, on)
	if !strings.HasSuffix(off, supplementalGuidanceRule+agent.MemoryFilesRule) || !strings.HasPrefix(on, off) || on == off {
		t.Fatal("operator protocol must precede memory rules and the append-last conversation")
	}
}

func TestDocumentStep_ConflictProtocolRequiresUsableOperatorPolicy(t *testing.T) {
	t.Parallel()
	for _, combined := range []bool{false, true} {
		t.Run(fmt.Sprintf("combined=%v", combined), func(t *testing.T) {
			t.Parallel()
			dir, baseSHA, headSHA := setupGitRepo(t)
			sctx := newTestContextWithDBRecords(t, &mockAgent{name: "test"}, dir, baseSHA, headSHA, config.Commands{})
			step := &DocumentStep{}
			baseline := step.buildPrompt(sctx, baseSHA, "none", combined)
			for _, trusted := range []string{"", "docs/config.md owns configuration facts."} {
				sctx.Config.Document.Instructions = trusted
				for _, operator := range []string{"", " \n\t ", "=======", "  Preserve the owner's examples.  "} {
					sctx.Config.Document.RepositoryInstructions = operator
					prompt := step.buildPrompt(sctx, baseSHA, "none", combined)
					sections := repositoryDocumentPolicySection(sctx) + trustedDocumentPolicySection(sctx)
					if sanitizePromptMultilineText(operator) != "" {
						assertGuidanceConflictProtocol(t, prompt)
						sections += supplementalGuidanceRule
						if combined {
							sections += `
- For a documentation-policy conflict, set "category" to "documentation", not "lint".`
						}
					} else if strings.Contains(prompt, "Supplemental guidance:") {
						t.Fatalf("unusable operator policy %q enabled the protocol", operator)
					}
					want := strings.Replace(baseline, documentScopeDiscipline, documentScopeDiscipline+sections, 1)
					if prompt != want {
						t.Fatalf("policy changed prompt beyond its sections: trusted=%q operator=%q", trusted, operator)
					}
					if !combined && strings.Contains(prompt, "category") {
						t.Fatal("standalone Document must remain category-free")
					}
				}
			}
		})
	}
}

// Scripted findings prove prompt delivery and consumer routing, not that a
// model will detect every conflict or refrain from every dependent edit.
func TestOperatorGuidance_ConflictsUseExistingDecisionFindings(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"review", "document", "housekeeping"} {
		for _, conflict := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/conflict=%v", mode, conflict), func(t *testing.T) {
				t.Parallel()
				dir, baseSHA, headSHA := setupGitRepo(t)
				operator := "docs/operator.md must own configuration facts."
				trusted := "Preserve examples in the configuration owner."
				if conflict {
					trusted = "docs/config.md must own configuration facts, never docs/operator.md."
				}
				operatorLabel := "Machine-local documentation ownership policy for this repository"
				trustedLabel := "Repository documentation ownership policy (trusted, from the default branch"
				if mode == "review" {
					operatorLabel = config.ReviewRepositoryPathInstructionsHeading
					trustedLabel = config.ReviewPathInstructionsHeading
				}
				description := operatorLabel + ": " + operator + " " + trustedLabel + ": " + trusted + " Decide the authoritative owner before editing."
				ag := &mockAgent{name: "test", runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
					assertGuidanceConflictProtocol(t, opts.Prompt)
					for _, want := range []string{operator, trusted, operatorLabel, trustedLabel} {
						if !strings.Contains(opts.Prompt, want) {
							t.Errorf("emitted prompt lost requirement/source %q", want)
						}
					}
					if mode == "document" {
						if strings.Contains(opts.Prompt, "category") || string(opts.JSONSchema) != string(findingsSchema) {
							t.Fatal("standalone Document gained category guidance or changed schema")
						}
						var schema struct {
							Properties map[string]struct {
								Items struct {
									Properties map[string]json.RawMessage
								}
							}
						}
						if err := json.Unmarshal(opts.JSONSchema, &schema); err != nil {
							t.Fatal(err)
						}
						props := schema.Properties["findings"].Items.Properties
						if len(props) == 0 || props["category"] != nil {
							t.Fatal("standalone findings schema must remain category-free")
						}
					}
					if mode == "housekeeping" {
						if !strings.Contains(opts.Prompt, `For a documentation-policy conflict, set "category" to "documentation", not "lint"`) || !strings.Contains(opts.Prompt, housekeepingLintSection) || string(opts.JSONSchema) != string(housekeepingFindingsSchema) {
							t.Fatal("combined pass lost documentation routing, lint duty, or existing schema")
						}
					}
					findings := cleanReviewFindings()
					if conflict {
						item := Finding{Severity: "warning", Action: "ask-user", File: "feature.txt", Line: 1, Description: description, ReviewScope: types.FindingReviewScopeSource}
						if mode == "housekeeping" {
							item.Category = types.FindingCategoryDocumentation
						}
						findings.Items = []Finding{item}
						findings.Summary = "guidance needs a decision"
					}
					output, err := json.Marshal(findings)
					return &agent.Result{Output: output}, err
				}}
				cmds := config.Commands{}
				if mode == "document" {
					cmds.Lint = "true"
				}
				sctx := newHousekeepingContext(t, ag, dir, baseSHA, headSHA, cmds)
				sctx.Config.Review = config.Review{
					RepositoryPathInstructions: []config.PathInstruction{{Path: "*.txt", Instructions: operator}},
					PathInstructions:           []config.PathInstruction{{Path: "*.txt", Instructions: trusted}},
				}
				sctx.Config.Document.RepositoryInstructions = operator
				sctx.Config.Document.Instructions = trusted
				if reviewConversationDir(sctx) != "" {
					t.Fatal("fixture must not rely on review conversation")
				}
				var step pipeline.Step = &DocumentStep{}
				if mode == "review" {
					step = &ReviewStep{}
				}
				out, err := step.Execute(sctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(ag.calls) != 1 || out.NeedsApproval != conflict {
					t.Fatalf("calls=%d, outcome=%+v", len(ag.calls), out)
				}
				findings, err := types.ParseFindingsJSON(out.Findings)
				if err != nil {
					t.Fatal(err)
				}
				if types.HasAskUserFindings(findings) != conflict || len(types.AutoFixableFindings(findings).Items) != 0 {
					t.Fatalf("reported conflict must require a decision, never automatic repair: %+v", findings)
				}
				if conflict {
					if len(findings.Items) != 1 || findings.Items[0].Description != description || findings.Items[0].Severity != "warning" || findings.Items[0].Action != "ask-user" {
						t.Fatalf("conflict did not survive the existing consumer: %+v", findings)
					}
					wantCategory := ""
					if mode == "housekeeping" {
						wantCategory = types.FindingCategoryDocumentation
					}
					if findings.Items[0].Category != wantCategory {
						t.Fatalf("category = %q, want %q", findings.Items[0].Category, wantCategory)
					}
				}
				if mode == "housekeeping" {
					// Exercise the real lint consumer, not just the stash splitter.
					lint, err := (&LintStep{}).Execute(sctx)
					if err != nil {
						t.Fatal(err)
					}
					lf, err := types.ParseFindingsJSON(lint.Findings)
					if err != nil || len(lf.Items) != 0 || lint.NeedsApproval || len(ag.calls) != 1 {
						t.Fatalf("documentation conflict leaked into lint: %+v, %v", lint, err)
					}
				} else if _, ok := sctx.Shared.TakeHousekeepingLint(); ok {
					t.Fatal("non-combined pass stashed a lint result")
				}
			})
		}
	}
}
