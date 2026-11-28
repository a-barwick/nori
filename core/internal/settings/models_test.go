package settings

import (
	"os"
	"testing"
)

func clearModelEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"NORI_CLOUD", "NORI_ENV_PROFILE", "NORI_ENV_FILE", "NORI_TASK_ENV_FILE",
		"NORI_STORE_BACKEND", "NORI_POSTGRES_URL", "NORI_CREDENTIAL_STORE",
		"NORI_MODEL_PROVIDER", "NORI_OUTER_PROVIDER", "NORI_INNER_PROVIDER",
		"NORI_MODEL_GATEWAY_URL", "NORI_MODEL_GATEWAY_API_KEY", "NORI_BYOK_REQUIRED",
		"NORI_MODEL_TEST_URL", "NORI_LOCAL_MODEL_URL", "NORI_LOCAL_MODEL_TAG",
		"NORI_AGENT_MODEL", "MODEL_NAME", "NORI_LLM_MAX_USD",
		"OPENAI_API_KEY", "OPENAI_PROD_API_KEY", "OPENAI_DEV_API_KEY", "OPENAI_TEST_API_KEY",
		"ANTHROPIC_API_KEY", "ANTHROPIC_PROD_API_KEY", "ANTHROPIC_DEV_API_KEY", "ANTHROPIC_TEST_API_KEY",
		"OPENROUTER_API_KEY", "OPENROUTER_PROD_API_KEY", "OPENROUTER_DEV_API_KEY", "OPENROUTER_TEST_API_KEY",
		"GOOGLE_API_KEY", "GEMINI_API_KEY", "AI_GATEWAY_API_KEY",
		"JEV_API_KEY", "EXA_API_KEY",
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

func TestLoadSimulationStripsAndIgnoresOperatorLLMKeys(t *testing.T) {
	clearModelEnv(t)
	t.Setenv("NORI_CLOUD", "local")
	t.Setenv("NORI_ENV_PROFILE", "test")
	t.Setenv("NORI_MODEL_PROVIDER", "gateway")
	t.Setenv("NORI_MODEL_GATEWAY_URL", "http://127.0.0.1:18123/v1")
	t.Setenv("NORI_MODEL_GATEWAY_API_KEY", "local-ephemeral-token")
	t.Setenv("OPENAI_API_KEY", "sk-dev-base")
	t.Setenv("OPENAI_DEV_API_KEY", "sk-dev-explicit")
	t.Setenv("OPENAI_TEST_API_KEY", "sk-test-openai")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-dev")
	t.Setenv("ANTHROPIC_TEST_API_KEY", "sk-test-ant")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "should-be-stripped")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-dev")
	t.Setenv("OPENROUTER_TEST_API_KEY", "sk-test-or")
	t.Setenv("GEMINI_API_KEY", "unused-gemini-key")
	t.Setenv("JEV_API_KEY", "jev-test-fixture")
	t.Setenv("EXA_API_KEY", "exa-test-fixture")

	s, err := LoadSimulation()
	if err != nil {
		t.Fatalf("LoadSimulation: %v", err)
	}
	m := s.Models
	if !m.BYOKRequired || m.OpenAIKey != "" || m.AnthropicKey != "" || m.OpenRouterKey != "" || m.GatewayKey != "" || m.GatewayURL != "" {
		t.Fatalf("expected simulation without loopback model URL to enforce strict BYOK with zero operator keys, got %+v", m)
	}
	for _, stripped := range []string{
		"OPENAI_API_KEY", "OPENAI_DEV_API_KEY", "OPENAI_TEST_API_KEY",
		"ANTHROPIC_API_KEY", "ANTHROPIC_TEST_API_KEY", "ANTHROPIC_AUTH_TOKEN",
		"OPENROUTER_API_KEY", "OPENROUTER_TEST_API_KEY",
		"NORI_MODEL_GATEWAY_API_KEY", "GEMINI_API_KEY",
	} {
		if os.Getenv(stripped) != "" {
			t.Fatalf("expected %s to be stripped from simulation environment", stripped)
		}
	}
	if m.JevKey != "jev-test-fixture" || s.ExaSearchKey != "exa-test-fixture" {
		t.Fatalf("expected JEV_API_KEY and EXA_API_KEY to remain intact, got jev=%q exa=%q", m.JevKey, s.ExaSearchKey)
	}
}

func TestLoadSimulationAllowsLoopbackTestAndLocalInklingWithoutCloudKeys(t *testing.T) {
	t.Run("NORI_MODEL_TEST_URL loopback stub", func(t *testing.T) {
		clearModelEnv(t)
		t.Setenv("NORI_CLOUD", "local")
		t.Setenv("NORI_ENV_PROFILE", "test")
		t.Setenv("NORI_MODEL_TEST_URL", "http://127.0.0.1:18123")
		t.Setenv("NORI_LLM_MAX_USD", "12.50")
		t.Setenv("OPENAI_TEST_API_KEY", "sk-should-be-stripped")

		s, err := LoadSimulation()
		if err != nil {
			t.Fatalf("LoadSimulation with loopback test URL: %v", err)
		}
		if s.Models.BYOKRequired {
			t.Fatalf("expected BYOKRequired=false when loopback NORI_MODEL_TEST_URL is set")
		}
		if s.Models.TestURL != "http://127.0.0.1:18123" || s.Models.Provider != "openai" {
			t.Fatalf("unexpected loopback test config: %+v", s.Models)
		}
		if s.Models.OpenAIKey != "" || os.Getenv("OPENAI_TEST_API_KEY") != "" {
			t.Fatalf("expected cloud key stripped even in loopback test mode")
		}
		if s.Models.MaxUSD != 12.50 {
			t.Fatalf("expected MaxUSD=12.50, got %v", s.Models.MaxUSD)
		}
	})

	t.Run("NORI_LOCAL_MODEL_URL defaults to inkling-2-mini", func(t *testing.T) {
		clearModelEnv(t)
		t.Setenv("NORI_CLOUD", "local")
		t.Setenv("NORI_LOCAL_MODEL_URL", "http://127.0.0.1:11434")

		s, err := LoadSimulation()
		if err != nil {
			t.Fatalf("LoadSimulation with NORI_LOCAL_MODEL_URL: %v", err)
		}
		if s.Models.BYOKRequired {
			t.Fatalf("expected BYOKRequired=false for local Inkling simulation")
		}
		if s.Models.TestURL != "http://127.0.0.1:11434" || s.Models.Outer != "inkling-2-mini" || s.Models.Inner != "inkling-2-mini" {
			t.Fatalf("unexpected local Inkling config: %+v", s.Models)
		}
	})

	t.Run("NORI_LOCAL_MODEL_URL rejects non-loopback host", func(t *testing.T) {
		clearModelEnv(t)
		t.Setenv("NORI_CLOUD", "local")
		t.Setenv("NORI_LOCAL_MODEL_URL", "https://api.openai.com/v1")

		if _, err := LoadSimulation(); err == nil {
			t.Fatalf("expected non-loopback NORI_LOCAL_MODEL_URL to be rejected")
		}
	})

	t.Run("rejects non-local cloud in simulation", func(t *testing.T) {
		clearModelEnv(t)
		t.Setenv("NORI_CLOUD", "aws")
		t.Setenv("NORI_MODEL_TEST_URL", "http://127.0.0.1:18123")

		if _, err := LoadSimulation(); err == nil {
			t.Fatalf("expected NORI_CLOUD=aws to be rejected in LoadSimulation")
		}
	})
}

func TestRuntimeIgnoresDeploymentLLMKeys(t *testing.T) {
	for _, cloud := range []string{"local", "aws"} {
		t.Run(cloud, func(t *testing.T) {
			clearModelEnv(t)
			t.Setenv("NORI_CLOUD", cloud)
			if cloud == "aws" {
				t.Setenv("NORI_POSTGRES_URL", "postgres://nori@cloud-db/byok?sslmode=require")
			}
			t.Setenv("NORI_CREDENTIAL_STORE", "database")
			t.Setenv("NORI_MODEL_PROVIDER", "gateway")
			t.Setenv("NORI_MODEL_GATEWAY_URL", "https://example.test/v1")
			t.Setenv("NORI_BYOK_REQUIRED", "false")
			t.Setenv("NORI_MODEL_GATEWAY_API_KEY", "unused-gateway-key")
			t.Setenv("OPENAI_API_KEY", "unused-openai-key")
			t.Setenv("ANTHROPIC_API_KEY", "unused-anthropic-key")
			t.Setenv("OPENROUTER_API_KEY", "unused-openrouter-key")
			t.Setenv("GEMINI_API_KEY", "unused-gemini-key")
			t.Setenv("JEV_API_KEY", "jev-test-fixture")
			t.Setenv("EXA_API_KEY", "exa-test-fixture")

			s, err := LoadDefaultCloud(cloud)
			if err != nil {
				t.Fatal(err)
			}
			m := s.Models
			if !m.BYOKRequired || m.GatewayKey != "" || m.OpenAIKey != "" || m.AnthropicKey != "" || m.OpenRouterKey != "" || m.GatewayURL != "" {
				t.Fatal("runtime configuration retained a deployment model key or gateway")
			}
			if os.Getenv("GEMINI_API_KEY") != "" || os.Getenv("NORI_MODEL_GATEWAY_API_KEY") != "" {
				t.Fatal("the application inherited deployment model credentials")
			}
			if m.JevKey != "jev-test-fixture" || s.ExaSearchKey != "exa-test-fixture" {
				t.Fatal("Jev or Exa credential was removed")
			}
		})
	}
}

func TestHostedModelAllowlist(t *testing.T) {
	allowed := HostedModelAllowlist()
	if len(allowed) != 9 {
		t.Fatalf("HostedModelAllowlist len = %d, want 9: %v", len(allowed), allowed)
	}
	for _, m := range allowed {
		if !IsHostedModelAllowed(m) {
			t.Fatalf("expected %q to be allowed", m)
		}
	}
	if !IsHostedModelAllowed("  GOOGLE/GEMINI-4-NEON ") {
		t.Fatalf("expected case-insensitive trimmed allowlist match")
	}
	for _, rejected := range []string{"", "gpt-6.2-sol", "gemini-4-neon", "openai/gpt-5-unknown", "anthropic/claude-3-5-sonnet", "meta/llama-3"} {
		if IsHostedModelAllowed(rejected) {
			t.Fatalf("expected unpriced/non-allowlisted model %q to be rejected", rejected)
		}
	}
}
