package main

import (
	"strings"
	"testing"
)

func TestLoadGatewayKeysIsolatesProdKeys(t *testing.T) {
	t.Run("does not fall back to prod keys", func(t *testing.T) {
		env := map[string]string{
			"OPENAI_API_KEY":     "sk-prod-openai",
			"ANTHROPIC_API_KEY":  "sk-prod-anthropic",
			"OPENROUTER_API_KEY": "sk-prod-openrouter",
		}
		keys, err := loadGatewayKeys(func(k string) string { return env[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if keys.openAI != "" || keys.anthropic != "" || keys.openRouter != "" {
			t.Fatalf("expected empty keys when only prod keys are set, got %+v", keys)
		}
	})

	t.Run("accepts dedicated test keys", func(t *testing.T) {
		env := map[string]string{
			"OPENAI_TEST_API_KEY":     "sk-test-openai",
			"OPENAI_API_KEY":          "sk-prod-openai",
			"ANTHROPIC_TEST_API_KEY":  "sk-test-anthropic",
			"OPENROUTER_TEST_API_KEY": "sk-test-openrouter",
		}
		keys, err := loadGatewayKeys(func(k string) string { return env[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if keys.openAI != "sk-test-openai" || keys.anthropic != "sk-test-anthropic" || keys.openRouter != "sk-test-openrouter" {
			t.Fatalf("unexpected keys: %+v", keys)
		}
	})

	t.Run("rejects test key equal to prod key", func(t *testing.T) {
		env := map[string]string{
			"OPENAI_TEST_API_KEY": "sk-shared-key",
			"OPENAI_API_KEY":      "sk-shared-key",
		}
		_, err := loadGatewayKeys(func(k string) string { return env[k] })
		if err == nil || !strings.Contains(err.Error(), "must not reuse") {
			t.Fatalf("expected isolation error, got %v", err)
		}
	})
}
