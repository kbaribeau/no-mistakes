package config

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Internal round/case metadata, deliberately NOT a global-config setting.
// Store it in the existing YAML provenance blob: no new run pin or DB schema.
const evalGuidanceKey = "no_mistakes_selected_guidance"

type evalGuidance struct {
	Version  int                       `yaml:"version"`
	Review   []evalReviewInstruction   `yaml:"review"`
	Document []evalDocumentInstruction `yaml:"document"`
}

// PathInstruction.Source is never YAML-decodable in user configuration. This
// separate wire type permits provenance only in the internal replay snapshot.
type evalReviewInstruction struct {
	Path         string            `yaml:"path"`
	Instructions string            `yaml:"instructions"`
	Source       InstructionSource `yaml:"source"`
}

type evalDocumentInstruction struct {
	Text   string            `yaml:"text"`
	Source InstructionSource `yaml:"source"`
}

// EnableEvalProvenance records the resolved guidance for the executor that
// actually performs this round. Recovery still rereads config; already-recorded
// rounds keep their old blob, while new rounds capture the recovered values.
func (c *Config) EnableEvalProvenance(global *GlobalConfig, repo *RepoConfig) error {
	if c == nil || global == nil || repo == nil {
		return fmt.Errorf("eval provenance requires merged, global, and repository configuration")
	}
	if err := c.ValidateReviewInstructions(); err != nil {
		return err
	}
	guidance := evalGuidance{Version: 1, Review: []evalReviewInstruction{}, Document: []evalDocumentInstruction{}}
	for _, entry := range c.Review.PathInstructions {
		guidance.Review = append(guidance.Review, evalReviewInstruction{Path: entry.Path, Instructions: entry.Instructions, Source: entry.Source})
	}
	for _, entry := range c.Document.Instructions {
		guidance.Document = append(guidance.Document, evalDocumentInstruction{Text: entry.Text, Source: entry.Source})
	}
	var raw map[string]any
	if err := yaml.Unmarshal(global.SourceYAML, &raw); err != nil {
		return fmt.Errorf("parse eval global configuration: %w", err)
	}
	if raw == nil {
		raw = make(map[string]any)
	}
	// Never retain unrelated checkout guidance or its machine-specific keys.
	// Global review/document blocks are already in the ordered, source-labelled lists.
	delete(raw, "repo_instructions")
	delete(raw, "review")
	delete(raw, "document")
	raw[evalGuidanceKey] = guidance
	globalYAML, err := yaml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("serialize eval global configuration: %w", err)
	}
	repoYAML, err := yaml.Marshal(repo)
	if err != nil {
		return fmt.Errorf("serialize eval repository configuration: %w", err)
	}
	// Validate the complete wire record before marking provenance enabled.
	if _, err := LoadEvalConfig(globalYAML, repoYAML); err != nil {
		return fmt.Errorf("validate eval provenance: %w", err)
	}
	c.ReplayGlobalYAML = globalYAML
	c.ReplayRepoYAML = repoYAML
	c.CaptureEvalProvenance = true
	return nil
}

// PrepareEvalGlobal validates a round's provenance for capture. Legacy round
// blobs WITHOUT checkout guidance are unambiguous and can be upgraded using
// their historical inputs. A map with no recorded selection cannot be guessed
// from today's registered path or today's config, even if it has just one key.
func PrepareEvalGlobal(globalYAML, repoYAML []byte) ([]byte, error) {
	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(globalYAML, &raw); err != nil {
		return nil, fmt.Errorf("parse eval global configuration: %w", err)
	}
	if _, ok := raw[evalGuidanceKey]; ok {
		if _, err := LoadEvalConfig(globalYAML, repoYAML); err != nil {
			return nil, err
		}
		return append([]byte(nil), globalYAML...), nil
	}
	global, err := LoadGlobalFromBytes(globalYAML)
	if err != nil {
		return nil, err
	}
	if len(global.RepoInstructions) != 0 {
		return nil, fmt.Errorf("eval capture cannot reconstruct selected checkout guidance from this legacy round; capture a new review with selected-guidance provenance")
	}
	repo, err := LoadRepoFromBytes(repoYAML)
	if err != nil {
		return nil, err
	}
	cfg, err := ResolveForRepository(global, repo, "", "")
	if err != nil {
		return nil, err
	}
	if err := cfg.EnableEvalProvenance(global, repo); err != nil {
		return nil, err
	}
	return cfg.ReplayGlobalYAML, nil
}

// LoadEvalConfig restores only historical inputs. Cases without an explicit
// snapshot are refused: older captures may already have stripped a checkout
// map, and absence in such a case is not evidence that none was selected.
func LoadEvalConfig(globalYAML, repoYAML []byte) (*Config, error) {
	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(globalYAML, &raw); err != nil {
		return nil, fmt.Errorf("parse captured global configuration: %w", err)
	}
	node, ok := raw[evalGuidanceKey]
	if !ok {
		return nil, fmt.Errorf("captured case has no selected-guidance snapshot; its checkout guidance may have been lost, so replay requires a fresh capture")
	}
	data, err := yaml.Marshal(&node)
	if err != nil {
		return nil, err
	}
	var snapshot evalGuidance
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("decode selected-guidance snapshot: %w", err)
	}
	if snapshot.Version != 1 {
		return nil, fmt.Errorf("unsupported selected-guidance snapshot version %d", snapshot.Version)
	}
	if snapshot.Review == nil || snapshot.Document == nil {
		return nil, fmt.Errorf("selected-guidance snapshot requires explicit review and document lists, even when empty")
	}
	delete(raw, evalGuidanceKey)
	if _, ok := raw["repo_instructions"]; ok {
		return nil, fmt.Errorf("selected-guidance snapshot must not carry a checkout-keyed repo_instructions map")
	}
	data, err = yaml.Marshal(raw)
	if err != nil {
		return nil, err
	}
	global, err := LoadGlobalFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("load captured global config: %w", err)
	}
	repo, err := LoadRepoFromBytes(repoYAML)
	if err != nil {
		return nil, fmt.Errorf("load captured repository config: %w", err)
	}
	cfg := Merge(global, repo)
	bySource := make(map[InstructionSource][]PathInstruction)
	cfg.Review.PathInstructions = nil
	for _, entry := range snapshot.Review {
		switch entry.Source {
		case InstructionSourceOperatorGlobal, InstructionSourceOperatorRepo, InstructionSourceRepository:
		default:
			return nil, fmt.Errorf("invalid selected review guidance source %q", entry.Source)
		}
		rule := PathInstruction{Path: entry.Path, Instructions: entry.Instructions, Source: entry.Source}
		cfg.Review.PathInstructions = append(cfg.Review.PathInstructions, rule)
		bySource[entry.Source] = append(bySource[entry.Source], rule)
	}
	cfg.Document.Instructions = nil
	for _, entry := range snapshot.Document {
		if entry.Source != InstructionSourceOperatorGlobal && entry.Source != InstructionSourceOperatorRepo && entry.Source != InstructionSourceRepository {
			return nil, fmt.Errorf("invalid selected document guidance source %q", entry.Source)
		}
		if strings.TrimSpace(entry.Text) == "" {
			return nil, fmt.Errorf("empty selected document guidance")
		}
		if err := validateDocumentRaw("selected document guidance", DocumentRaw{Instructions: entry.Text}); err != nil {
			return nil, err
		}
		cfg.Document.Instructions = append(cfg.Document.Instructions, DocumentInstruction{Source: entry.Source, Text: entry.Text})
	}
	if err := cfg.ValidateReviewInstructions(); err != nil {
		return nil, err
	}
	for _, source := range []InstructionSource{InstructionSourceOperatorGlobal, InstructionSourceOperatorRepo, InstructionSourceRepository} {
		rules := bySource[source]
		maxEntries, maxBytes := MaxOperatorReviewPathInstructions, MaxOperatorReviewPathInstructionsBytes
		if source == InstructionSourceRepository {
			maxEntries, maxBytes = MaxReviewPathInstructions, ReviewPathInstructionsBudgetBytes(rules)
		}
		if err := validateReviewPathInstructions("selected review guidance from "+string(source), rules, maxEntries, maxBytes); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}
