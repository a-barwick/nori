package settings

import (
	"os"
	"strings"
	"testing"
)

func clearModelEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"NORI_CLOUD", "NORI_ENV_PROFILE", "NORI_ENV_FILE", "NORI_TASK_ENV_FILE",
		"NORI_STORE_BACKEND", "NORI_POSTGRES_URL", "NORI_CREDENTIAL_STORE",
		"NORI_MODEL_PROVIDER", "NORI_OUTER_PROVIDER", "NORI_INNER_PROVIDER",
		"NORI_MODEL_GATEWAY_URL", "NORI_MODEL_GATEWAY_API_KEY", "NORI_BYOK_REQUIRED", "NORI_MODEL_TEST_URL",
		"NORI_AGENT_MODEL", "MODEL_NAME", "NORI_LLM_MAX_USD",
		"OPENAI_API_KEY", "OPENAI_PROD_API_KEY", "OPENAI_DEV_API_KEY", "OPENAI_TEST_API_KEY",
		"ANTHROPIC_API_KEY", "ANTHROPIC_PROD_API_KEY", "ANTHROPIC_DEV_API_KEY", "ANTHROPIC_TEST_API_KEY",
		"OPENROUTER_API_KEY", "OPENROUTER_PROD_API_KEY", "OPENROUTER_DEV_API_KEY", "OPENROUTER_TEST_API_KEY",
		"NORI_USER_MAX_CALLS_PER_HOUR", "NORI_USER_MAX_USD_PER_DAY",
	}
	for _, k := range keys {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	t.Setenv("NORI_ENV_FILE", os.DevNull)
	t.Setenv("NORI_TASK_ENV_FILE", os.DevNull)
	t.Setenv("NORI_POSTGRES_URL", "postgres://nori@127.0.0.1:15432/nori?sslmode=disable")
}

func TestProfileKeyIsolation_TestNeverFallsBackToDevOrBase(t *testing.T) {
	clearModelEnv(t)
	t.Setenv("NORI_CLOUD", "local")
	t.Setenv("NORI_ENV_PROFILE", "test")
	t.Setenv("OPENAI_API_KEY", "sk-dev-base")
	t.Setenv("OPENAI_DEV_API_KEY", "sk-dev-explicit")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-dev")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-dev")

	s, err := LoadDefaultCloud("local")
	if err != nil {
		t.Fatalf("LoadDefaultCloud: %v", err)
	}
	if s.Models.OpenAIKey != "" || s.Models.AnthropicKey != "" || s.Models.OpenRouterKey != "" {
		t.Fatalf("expected test profile to ignore dev/base keys, got %+v", s.Models)
	}
	name, key := s.Models.OuterCredential()
	if name != "OPENAI_TEST_API_KEY" || key != "" {
		t.Fatalf("expected OPENAI_TEST_API_KEY with empty value, got %s=%q", name, key)
	}

	t.Setenv("OPENAI_TEST_API_KEY", "sk-test-openai")
	t.Setenv("ANTHROPIC_TEST_API_KEY", "sk-test-ant")
	t.Setenv("OPENROUTER_TEST_API_KEY", "sk-test-or")
	s, err = LoadDefaultCloud("local")
	if err != nil {
		t.Fatalf("LoadDefaultCloud with test keys: %v", err)
	}
	if s.Models.OpenAIKey != "sk-test-openai" || s.Models.AnthropicKey != "sk-test-ant" || s.Models.OpenRouterKey != "sk-test-or" {
		t.Fatalf("unexpected resolved test keys: %+v", s.Models)
	}
}

func TestProfileKeyIsolation_RejectsSharedKeysAcrossProfiles(t *testing.T) {
	clearModelEnv(t)
	t.Setenv("NORI_CLOUD", "local")
	t.Setenv("NORI_ENV_PROFILE", "test")
	t.Setenv("OPENAI_DEV_API_KEY", "sk-shared-key")
	t.Setenv("OPENAI_TEST_API_KEY", "sk-shared-key")

	_, err := LoadDefaultCloud("local")
	if err == nil || !strings.Contains(err.Error(), "must be distinct keys") {
		t.Fatalf("expected distinct-key error, got %v", err)
	}
}

func TestAnthropicAPIKeyNotStrippedAndRedacted(t *testing.T) {
	clearModelEnv(t)
	t.Setenv("NORI_CLOUD", "local")
	t.Setenv("NORI_OUTER_PROVIDER", "anthropic")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-secret-value")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "should-be-stripped")

	s, err := LoadDefaultCloud("local")
	if err != nil {
		t.Fatalf("LoadDefaultCloud: %v", err)
	}
	if s.Models.AnthropicKey != "sk-ant-secret-value" {
		t.Fatalf("expected ANTHROPIC_API_KEY preserved, got %q", s.Models.AnthropicKey)
	}
	if os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" {
		t.Fatalf("expected ANTHROPIC_AUTH_TOKEN stripped as management credential")
	}
	if s.Models.Outer != DefaultAnthropicOuterModel {
		t.Fatalf("expected default anthropic outer model %q, got %q", DefaultAnthropicOuterModel, s.Models.Outer)
	}
	found := false
	for _, c := range s.Observability.Credentials {
		if c == "sk-ant-secret-value" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected ANTHROPIC_API_KEY in Observability.Credentials for redaction")
	}
}

func TestGatewayLoopbackAllowedInLocalRefusedInAWS(t *testing.T) {
	clearModelEnv(t)
	t.Setenv("NORI_MODEL_PROVIDER", "gateway")
	t.Setenv("NORI_MODEL_GATEWAY_URL", "http://127.0.0.1:18123/v1")
	t.Setenv("NORI_MODEL_GATEWAY_API_KEY", "local-ephemeral-token")
	t.Setenv("NORI_AGENT_MODEL", "gpt-6.1-sol")
	t.Setenv("MODEL_NAME", "openai/gpt-6.1-terra")

	t.Setenv("NORI_CLOUD", "local")
	if _, err := LoadDefaultCloud("local"); err != nil {
		t.Fatalf("expected loopback http gateway allowed in local mode, got %v", err)
	}

	t.Setenv("NORI_CLOUD", "aws")
	if _, err := LoadDefaultCloud("aws"); err == nil {
		t.Fatalf("expected loopback http gateway rejected in aws mode")
	}
}

func TestProviderSwitchingAndMaxUSD(t *testing.T) {
	clearModelEnv(t)
	t.Setenv("NORI_CLOUD", "local")
	t.Setenv("NORI_OUTER_PROVIDER", "anthropic")
	t.Setenv("NORI_INNER_PROVIDER", "openai")
	t.Setenv("NORI_LLM_MAX_USD", "12.50")

	s, err := LoadDefaultCloud("local")
	if err != nil {
		t.Fatalf("LoadDefaultCloud: %v", err)
	}
	if s.Models.EffectiveOuterProvider() != "anthropic" || s.Models.Outer != DefaultAnthropicOuterModel {
		t.Fatalf("unexpected outer config: %+v", s.Models)
	}
	if s.Models.EffectiveInnerProvider() != "openai" || s.Models.Inner != DefaultInnerModel {
		t.Fatalf("unexpected inner config: %+v", s.Models)
	}
	if s.Models.MaxUSD != 12.50 {
		t.Fatalf("expected MaxUSD=12.50, got %v", s.Models.MaxUSD)
	}
}

func TestBYOKRequiredAllowsEmptyDeploymentGatewayKey(t *testing.T) {
	clearModelEnv(t)
	t.Setenv("NORI_CLOUD", "local")
	t.Setenv("NORI_MODEL_PROVIDER", "gateway")
	t.Setenv("NORI_MODEL_GATEWAY_URL", "https://ai-gateway.vercel.sh/v1")
	t.Setenv("NORI_BYOK_REQUIRED", "true")
	t.Setenv("NORI_CREDENTIAL_STORE", "database")
	t.Setenv("NORI_AGENT_MODEL", "anthropic/claude-haiku-4.5")
	t.Setenv("MODEL_NAME", "anthropic/claude-haiku-4.5")

	s, err := LoadDefaultCloud("local")
	if err != nil {
		t.Fatalf("LoadDefaultCloud with BYOK required: %v", err)
	}
	if !s.Models.BYOKRequired {
		t.Fatal("expected BYOKRequired=true")
	}
	if s.Models.GatewayKey != "" {
		t.Fatalf("expected empty deployment GatewayKey, got %q", s.Models.GatewayKey)
	}
}

func TestBYOKRequiredRejectsMissingCredentialStore(t *testing.T) {
	clearModelEnv(t)
	t.Setenv("NORI_CLOUD", "local")
	t.Setenv("NORI_MODEL_PROVIDER", "gateway")
	t.Setenv("NORI_MODEL_GATEWAY_URL", "https://ai-gateway.vercel.sh/v1")
	t.Setenv("NORI_BYOK_REQUIRED", "true")
	t.Setenv("NORI_AGENT_MODEL", "anthropic/claude-haiku-4.5")
	t.Setenv("MODEL_NAME", "anthropic/claude-haiku-4.5")

	_, err := LoadDefaultCloud("local")
	if err == nil || !strings.Contains(err.Error(), "NORI_CREDENTIAL_STORE=database") {
		t.Fatalf("expected NORI_CREDENTIAL_STORE=database error, got %v", err)
	}
}
