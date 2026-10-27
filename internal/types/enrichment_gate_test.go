package types

import (
	"context"
	"testing"
)

func kbWithEnrichment() *KnowledgeBase {
	return &KnowledgeBase{
		IndexingStrategy: IndexingStrategy{
			VectorEnabled:  true,
			KeywordEnabled: true,
			WikiEnabled:    true,
			GraphEnabled:   true,
		},
		ExtractConfig:            &ExtractConfig{Enabled: true},
		AutoTagConfig:            &AutoTagConfig{Enabled: true},
		QuestionGenerationConfig: &QuestionGenerationConfig{Enabled: true},
		VLMConfig:                VLMConfig{Enabled: true, ModelID: "vlm"},
	}
}

func TestEnrichmentDisabledByDefault(t *testing.T) {
	t.Setenv("WEKNORA_ENRICHMENT_ENABLED", "")
	if EnrichmentEnabled() {
		t.Fatal("enrichment must be off when the env var is unset")
	}

	kb := kbWithEnrichment()
	if kb.IsWikiEnabled() {
		t.Error("wiki must be off while the master switch is off")
	}
	if kb.IsGraphEnabled() {
		t.Error("graph must be off while the master switch is off")
	}
	if kb.IsMultimodalEnabled() {
		t.Error("multimodal must be off while the master switch is off")
	}
}

func TestEnrichmentHonoursPerKbFlagsWhenOn(t *testing.T) {
	t.Setenv("WEKNORA_ENRICHMENT_ENABLED", "true")
	if !EnrichmentEnabled() {
		t.Fatal("enrichment must be on when the env var is true")
	}

	kb := kbWithEnrichment()
	if !kb.IsWikiEnabled() || !kb.IsGraphEnabled() || !kb.IsMultimodalEnabled() {
		t.Error("per-KB flags must be honoured while the master switch is on")
	}
}

func TestEnrichmentWritesNeedSystemAdmin(t *testing.T) {
	t.Setenv("WEKNORA_ENRICHMENT_ENABLED", "true")
	ctx := context.Background()
	if EnrichmentWritesAllowed(ctx) {
		t.Error("non-admin writes must not enable enrichment")
	}

	adminCtx := context.WithValue(ctx, SystemAdminContextKey, true)
	if !EnrichmentWritesAllowed(adminCtx) {
		t.Error("system admin writes may enable enrichment while the switch is on")
	}

	t.Setenv("WEKNORA_ENRICHMENT_ENABLED", "false")
	if EnrichmentWritesAllowed(adminCtx) {
		t.Error("the master switch must override the admin flag")
	}
}

func TestStripEnrichmentTurnsEverythingOff(t *testing.T) {
	kb := kbWithEnrichment()
	kb.StripEnrichment()

	if kb.IndexingStrategy.WikiEnabled || kb.IndexingStrategy.GraphEnabled {
		t.Error("wiki and graph must be stripped")
	}
	if kb.ExtractConfig.Enabled || kb.AutoTagConfig.Enabled || kb.QuestionGenerationConfig.Enabled {
		t.Error("extract, auto-tag and question generation must be stripped")
	}
	if kb.VLMConfig.Enabled {
		t.Error("multimodal must be stripped")
	}
}
