package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const privacyFilterProvider = "privacyfilter"
const pluginName = "privacyfilter"

type privacyFilterConfig struct {
	// The host passes these fields through but owns their behavior.
	Enabled          *bool      `yaml:"enabled"`
	Priority         int        `yaml:"priority"`
	MaxHanPercent    percentage `yaml:"max_han_percent"`
	AllowQuotedInput bool       `yaml:"allow_quoted_input"`
	SkipModels       []string   `yaml:"skip_models"`
	SkipFormats      []string   `yaml:"skip_formats"`
	// Retained only to report migration from the original privacy filter.
	GitleaksTOML *string `yaml:"gitleaks_toml"`
}

type percentage int

func (p *percentage) UnmarshalYAML(node *yaml.Node) error {
	if node.Tag != "!!int" {
		return fmt.Errorf("max_han_percent must be an integer from 0 to 100")
	}
	var value int
	if err := node.Decode(&value); err != nil {
		return err
	}
	*p = percentage(value)
	return nil
}

func defaultConfig() privacyFilterConfig {
	return privacyFilterConfig{MaxHanPercent: 20, AllowQuotedInput: true}
}

func parseConfig(raw []byte) (privacyFilterConfig, error) {
	cfg := defaultConfig()
	if len(bytes.TrimSpace(raw)) > 0 {
		// yaml.v3 skips custom unmarshaling for null. Inspect explicit policy
		// values first so null cannot silently retain a permissive default.
		var document yaml.Node
		if err := yaml.Unmarshal(raw, &document); err != nil {
			return cfg, fmt.Errorf("invalid privacyfilter config: %w", err)
		}
		if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
			return cfg, fmt.Errorf("invalid privacyfilter config: expected a mapping")
		}
		mapping := document.Content[0]
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			key, value := mapping.Content[i].Value, mapping.Content[i+1]
			if key == "max_han_percent" && value.Tag != "!!int" {
				return cfg, fmt.Errorf("max_han_percent must be an integer from 0 to 100")
			}
			if key == "allow_quoted_input" && value.Tag != "!!bool" {
				return cfg, fmt.Errorf("allow_quoted_input must be a boolean")
			}
			if key == "<<" {
				return cfg, fmt.Errorf("invalid privacyfilter config: YAML merge keys are not supported")
			}
		}
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		if err := decoder.Decode(&cfg); err != nil {
			return cfg, fmt.Errorf("invalid privacyfilter config: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return cfg, fmt.Errorf("invalid privacyfilter config: expected one YAML document")
		}
	}
	if cfg.MaxHanPercent < 0 || cfg.MaxHanPercent > 100 {
		return cfg, fmt.Errorf("max_han_percent must be an integer from 0 to 100")
	}
	if cfg.GitleaksTOML != nil {
		log.Warn("privacyfilter: gitleaks_toml is obsolete; this branch checks language and does not redact private information")
	}
	return cfg, nil
}

func (cfg *privacyFilterConfig) shouldSkip(model, requestedModel, format string) bool {
	for _, m := range cfg.SkipModels {
		trimmed := strings.TrimSpace(m)
		if trimmed != "" && (strings.EqualFold(trimmed, model) || strings.EqualFold(trimmed, requestedModel)) {
			return true
		}
	}
	for _, f := range cfg.SkipFormats {
		trimmed := strings.TrimSpace(f)
		if trimmed != "" && strings.EqualFold(trimmed, format) {
			return true
		}
	}
	return false
}
