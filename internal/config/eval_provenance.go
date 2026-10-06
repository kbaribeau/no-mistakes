package config

import (
	"bytes"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// Internal round/case metadata, NOT an accepted global-config setting. Record
// the resolved list before changed-file matching, including an explicit empty
// selection. Replay supplies these historical inputs to the CURRENT Review.
const selectedReviewGuidanceKey = "no_mistakes_selected_review_guidance"

// The earlier unpublished Review+Document snapshot used a different shape and
// selection model. Never reinterpret it or modify those artifacts in place.
const privateSelectedGuidanceKey = "no_mistakes_selected_guidance"

type selectedReviewGuidance struct {
	Version                    int               `yaml:"version"`
	RepositoryPathInstructions []PathInstruction `yaml:"repository_path_instructions"`
}

// EnableEvalProvenance records the configuration for this executor. Recovery
// still rereads config; already-recorded rounds retain their own old blobs.
func (c *Config) EnableEvalProvenance(global *GlobalConfig, repo *RepoConfig) error {
	if c == nil || global == nil || repo == nil {
		return fmt.Errorf("eval provenance requires merged, global, and repository configuration")
	}
	data := global.SourceYAML
	if len(data) == 0 {
		data = []byte("{}\n")
	}
	raw, err := evalGlobalMapping(data)
	if err != nil {
		return err
	}
	if _, err := LoadGlobalFromBytes(data); err != nil {
		return fmt.Errorf("read eval global configuration: %w", err)
	}
	if err := putSelectedReviewGuidance(raw, c.Review.RepositoryPathInstructions); err != nil {
		return err
	}
	globalYAML, err := marshalEvalGlobal(raw)
	if err != nil {
		return fmt.Errorf("serialize eval global configuration: %w", err)
	}
	repoYAML, err := yaml.Marshal(repo)
	if err != nil {
		return fmt.Errorf("serialize eval repository configuration: %w", err)
	}
	// Validate the whole wire record, including the unchanged combined budget,
	// before enabling provenance. No independent per-source quota is added.
	if _, err := LoadEvalConfig(globalYAML, repoYAML); err != nil {
		return fmt.Errorf("validate eval provenance: %w", err)
	}
	c.ReplayGlobalYAML = globalYAML
	c.ReplayRepoYAML = repoYAML
	c.CaptureEvalProvenance = true
	return nil
}

// PrepareEvalGlobal validates and canonicalizes capture's historical global
// blob. The public exporter has retained repository_overrides since its first
// version; only legacy blobs with NO nonempty remote Review list are therefore
// unambiguous. Do not infer a remote from today's registration or a fingerprint.
func PrepareEvalGlobal(data []byte) ([]byte, error) {
	raw, _, selected, err := loadEvalGlobal(data)
	if err != nil {
		return nil, err
	}
	if err := putSelectedReviewGuidance(raw, selected); err != nil {
		return nil, err
	}
	// The selected list is the sole authority in exported cases. Preserve all
	// unrelated override fields (commands, formatting, Document), not a broad
	// privacy rewrite. Raw round provenance remains untouched.
	if node, ok := raw["repository_overrides"]; ok {
		var overrides map[string]map[string]yaml.Node
		if err := node.Decode(&overrides); err != nil {
			return nil, fmt.Errorf("decode captured repository overrides: %w", err)
		}
		for _, override := range overrides {
			delete(override, "review")
		}
		if err := node.Encode(overrides); err != nil {
			return nil, err
		}
		raw["repository_overrides"] = node
	}
	return marshalEvalGlobal(raw)
}

// LoadEvalConfig restores historical configured inputs, not a historical prompt
// or binary. Missing/ambiguous provenance must fail before an agent launches.
func LoadEvalConfig(globalYAML, repoYAML []byte) (*Config, error) {
	_, global, selected, err := loadEvalGlobal(globalYAML)
	if err != nil {
		return nil, err
	}
	repo, err := LoadRepoFromBytes(repoYAML)
	if err != nil {
		return nil, fmt.Errorf("load captured repository config: %w", err)
	}
	cfg := Merge(global, repo)
	cfg.Review.RepositoryPathInstructions = resolvePathInstructions(selected)
	if err := cfg.Review.ValidatePathInstructionsBudget(); err != nil {
		return nil, fmt.Errorf("captured Review guidance: %w", err)
	}
	return cfg, nil
}

func loadEvalGlobal(data []byte) (map[string]yaml.Node, *GlobalConfig, []PathInstruction, error) {
	raw, err := evalGlobalMapping(data)
	if err != nil {
		return nil, nil, nil, err
	}
	var selected []PathInstruction
	node, recorded := raw[selectedReviewGuidanceKey]
	if recorded {
		selected, err = decodeSelectedReviewGuidance(node)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("decode selected Review guidance: %w", err)
		}
		delete(raw, selectedReviewGuidanceKey)
	}
	publicYAML, err := marshalEvalGlobal(raw)
	if err != nil {
		return nil, nil, nil, err
	}
	global, err := LoadGlobalFromBytes(publicYAML)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load captured global config: %w", err)
	}
	if !recorded {
		for _, override := range global.RepositoryOverrides {
			if len(override.Review.PathInstructions) != 0 {
				return nil, nil, nil, fmt.Errorf("selected per-repository Review guidance was not recorded in this legacy configuration; capture a new review with selected-guidance provenance (today's remote cannot reconstruct it)")
			}
		}
	}
	return raw, global, selected, nil
}

func evalGlobalMapping(data []byte) (map[string]yaml.Node, error) {
	var raw map[string]yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse eval global configuration: %w", err)
	}
	if raw == nil {
		// YAML null is a valid public global config with all defaults. This
		// is distinct from a null selected-guidance payload, which is invalid.
		raw = make(map[string]yaml.Node)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("eval global configuration must contain exactly one YAML document")
	}
	if _, ok := raw[privateSelectedGuidanceKey]; ok {
		return nil, fmt.Errorf("unsupported private selected-guidance snapshot; recapture with a supported build or explicitly convert a copy; the original artifact is unchanged")
	}
	return raw, nil
}

// Resolve public-config aliases before sorting/rewriting the mapping: keeping
// raw alias nodes could move a use before its anchor or retain an anchor in a
// removed override. Internal metadata is checked strictly before this step.
func marshalEvalGlobal(raw map[string]yaml.Node) ([]byte, error) {
	values := make(map[string]any, len(raw))
	for key, node := range raw {
		var value any
		if err := node.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode eval global field %q: %w", key, err)
		}
		values[key] = value
	}
	return yaml.Marshal(values)
}

func putSelectedReviewGuidance(raw map[string]yaml.Node, selected []PathInstruction) error {
	// append to a non-nil empty slice so YAML emits [], never null.
	snapshot := selectedReviewGuidance{Version: 1, RepositoryPathInstructions: append([]PathInstruction{}, selected...)}
	var node yaml.Node
	if err := node.Encode(snapshot); err != nil {
		return err
	}
	raw[selectedReviewGuidanceKey] = node
	return nil
}

func decodeSelectedReviewGuidance(node yaml.Node) ([]PathInstruction, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("snapshot must be a mapping")
	}
	data, err := yaml.Marshal(&node)
	if err != nil {
		return nil, err
	}
	var snapshot selectedReviewGuidance
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&snapshot); err != nil {
		return nil, err
	}
	if snapshot.Version != 1 {
		return nil, fmt.Errorf("unsupported snapshot version %d", snapshot.Version)
	}
	if snapshot.RepositoryPathInstructions == nil {
		return nil, fmt.Errorf("snapshot requires an explicit repository_path_instructions list, even when empty")
	}
	// yaml's ordinary config decoding permits scalar coercion and aliases.
	// This small internal protocol does not: require concrete typed fields.
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		switch key.Value {
		case "version":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!int" {
				return nil, fmt.Errorf("snapshot version must be an integer")
			}
		case "repository_path_instructions":
			if value.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("repository_path_instructions must be a list")
			}
			for _, rule := range value.Content {
				if rule.Kind != yaml.MappingNode {
					return nil, fmt.Errorf("selected rule must be a mapping")
				}
				for j := 0; j < len(rule.Content); j += 2 {
					field, text := rule.Content[j], rule.Content[j+1]
					if (field.Value != "path" && field.Value != "instructions") || text.Kind != yaml.ScalarNode || text.Tag != "!!str" {
						return nil, fmt.Errorf("selected rule requires string path and instructions fields")
					}
				}
			}
		default:
			return nil, fmt.Errorf("unknown snapshot field %q", key.Value)
		}
	}
	if err := validatePathInstructionEntries(snapshot.RepositoryPathInstructions); err != nil {
		return nil, err
	}
	return snapshot.RepositoryPathInstructions, nil
}
