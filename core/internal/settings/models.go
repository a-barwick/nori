package settings

import (
	"cmp"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"

	"nori/internal/netguard"
	"nori/internal/secret"
)

const (
	DefaultOuterModel           = "gpt-6.2-sol"
	DefaultInnerModel           = "gpt-6.1-luna"
	DefaultOpenRouterOuterModel = "google/gemini-4-neon"
	DefaultOpenRouterInnerModel = "google/gemini-4-hydrogen"
	DefaultGatewayOuterModel    = "google/gemini-4-neon"
	DefaultGatewayInnerModel    = "google/gemini-4-hydrogen"
	DefaultHostedOuterModel     = DefaultGatewayOuterModel
	DefaultHostedInnerModel     = DefaultGatewayInnerModel
	DefaultAnthropicOuterModel  = "claude-opus-5.5"
	DefaultAnthropicInnerModel  = "claude-haiku-5.5"
)

// hostedModelAllowlist lists the exact known-tariff routed models permitted on
// Nori's platform-hosted Vercel AI Gateway. Any model outside this list fails
// closed so unpriced hosted models can never be silently charged $0.
var hostedModelAllowlist = []string{
	"openai/gpt-6.2-sol",
	"openai/gpt-6.1-terra",
	"openai/gpt-6-luna",
	"anthropic/claude-opus-5.5",
	"anthropic/claude-sonnet-5.5",
	"anthropic/claude-haiku-5.5",
	"google/gemini-4-carbon",
	"google/gemini-4-neon",
	"google/gemini-4-hydrogen",
}

var hostedModelAllowSet = func() map[string]bool {
	m := make(map[string]bool, len(hostedModelAllowlist))
	for _, model := range hostedModelAllowlist {
		m[model] = true
	}
	return m
}()

// HostedModelAllowlist returns a copy of the exact model IDs allowed for
// platform-hosted inference.
func HostedModelAllowlist() []string {
	out := make([]string, len(hostedModelAllowlist))
	copy(out, hostedModelAllowlist)
	return out
}

// IsHostedModelAllowed reports whether model is on the strict known-tariff
// platform-hosted allowlist.
func IsHostedModelAllowed(model string) bool {
	return hostedModelAllowSet[strings.ToLower(strings.TrimSpace(model))]
}

func ValidModelProvider(p string) bool {
	switch p {
	case "", "openai", "anthropic", "openrouter", "gateway":
		return true
	default:
		return false
	}
}

func ValidEnvProfile(p string) bool {
	switch p {
	case "", "prod", "dev", "test":
		return true
	default:
		return false
	}
}

func resolveEnvProfile(cloud string) string {
	if raw := strings.ToLower(strings.TrimSpace(os.Getenv("NORI_ENV_PROFILE"))); raw != "" {
		return raw
	}
	if cloud == "local" {
		return "dev"
	}
	return "prod"
}

func parseOptionalNonNegativeFloat(name string) float64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return math.NaN()
	}
	return v
}

func parseOptionalNonNegativeFloatDefault(name string, fallback float64) float64 {
	if strings.TrimSpace(os.Getenv(name)) == "" {
		return fallback
	}
	return parseOptionalNonNegativeFloat(name)
}

func modelsFromEnvironment(profile, cloud string) Models {
	testURL := strings.TrimSpace(os.Getenv("NORI_MODEL_TEST_URL"))
	localURL := strings.TrimSpace(os.Getenv("NORI_LOCAL_MODEL_URL"))
	localTag := ""
	localModel := testURL == "" && localURL != ""
	if localModel {
		testURL = localURL
		localTag = cmp.Or(strings.TrimSpace(os.Getenv("NORI_LOCAL_MODEL_TAG")), "inkling-2-mini")
	}
	if !(cloud == "local" && testURL != "") {
		return Models{
			EnvProfile:   profile,
			BYOKRequired: true,
			TestURL:      testURL,
			LocalModel:   localModel,
			JevKey:       strings.TrimSpace(os.Getenv("JEV_API_KEY")),
			MaxUSD:       parseOptionalNonNegativeFloat("NORI_LLM_MAX_USD"),
		}
	}
	models := Models{
		EnvProfile:     profile,
		Provider:       "openai",
		JevKey:         strings.TrimSpace(os.Getenv("JEV_API_KEY")),
		Outer:          cmp.Or(strings.TrimSpace(os.Getenv("NORI_AGENT_MODEL")), localTag, DefaultOuterModel),
		Inner:          cmp.Or(strings.TrimSpace(os.Getenv("MODEL_NAME")), localTag, DefaultInnerModel),
		TestURL:        testURL,
		LocalModel:     localModel,
		OuterReasoning: strings.ToLower(strings.TrimSpace(os.Getenv("NORI_OUTER_REASONING_EFFORT"))),
		InnerReasoning: strings.ToLower(strings.TrimSpace(os.Getenv("NORI_INNER_REASONING_EFFORT"))),
		MaxUSD:         parseOptionalNonNegativeFloat("NORI_LLM_MAX_USD"),
	}
	if models.InnerReasoning == "" {
		models.InnerReasoning = "low"
	}
	return models
}

func (m Models) EffectiveOuterProvider() string {
	return cmp.Or(m.OuterProvider, m.Provider, "openai")
}

func (m Models) EffectiveInnerProvider() string {
	if m.InnerProvider != "" {
		return m.InnerProvider
	}
	if m.Provider != "" {
		return m.Provider
	}
	if m.Inner == DefaultInnerModel || m.Inner == "gpt-6.1-terra" {
		return "openai"
	}
	return "openrouter"
}

func (m Models) UsesGatewayOuter() bool { return m.EffectiveOuterProvider() == "gateway" }
func (m Models) UsesGatewayInner() bool { return m.EffectiveInnerProvider() == "gateway" }
func (m Models) UsesGateway() bool      { return m.UsesGatewayOuter() || m.UsesGatewayInner() }

func (m Models) UsesOpenAIForTasks() bool { return m.EffectiveInnerProvider() == "openai" }

func (m Models) OuterCredential() (name, key string) {
	switch m.EffectiveOuterProvider() {
	case "gateway":
		return "NORI_MODEL_GATEWAY_API_KEY", m.GatewayKey
	case "anthropic":
		return "ANTHROPIC_API_KEY", m.AnthropicKey
	case "openrouter":
		return "OPENROUTER_API_KEY", m.OpenRouterKey
	default:
		return "OPENAI_API_KEY", m.OpenAIKey
	}
}

func (m Models) TaskCredential() (name, key string) {
	switch m.EffectiveInnerProvider() {
	case "gateway":
		return "NORI_MODEL_GATEWAY_API_KEY", m.GatewayKey
	case "anthropic":
		return "ANTHROPIC_API_KEY", m.AnthropicKey
	case "openai":
		return "OPENAI_API_KEY", m.OpenAIKey
	default:
		return "OPENROUTER_API_KEY", m.OpenRouterKey
	}
}

func ValidReasoningEffort(e string) bool {
	switch e {
	case "", "none", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func (m Models) validateModels(cloud string) error {
	if !ValidModelProvider(m.Provider) {
		return fmt.Errorf("NORI_MODEL_PROVIDER must be openai, anthropic, openrouter, or gateway")
	}
	if !ValidModelProvider(m.OuterProvider) {
		return fmt.Errorf("NORI_OUTER_PROVIDER must be openai, anthropic, openrouter, or gateway")
	}
	if !ValidModelProvider(m.InnerProvider) {
		return fmt.Errorf("NORI_INNER_PROVIDER must be openai, anthropic, openrouter, or gateway")
	}
	if !ValidReasoningEffort(m.OuterReasoning) {
		return fmt.Errorf("NORI_OUTER_REASONING_EFFORT must be none, low, medium, high, xhigh, or max")
	}
	if !ValidReasoningEffort(m.InnerReasoning) {
		return fmt.Errorf("NORI_INNER_REASONING_EFFORT must be none, low, medium, high, xhigh, or max")
	}
	if math.IsNaN(m.MaxUSD) || math.IsInf(m.MaxUSD, 0) || m.MaxUSD < 0 {
		return fmt.Errorf("NORI_LLM_MAX_USD must be a non-negative dollar amount")
	}
	return m.validateGateway(cloud)
}

func (m Models) validateGateway(cloud string) error {
	if !m.UsesGateway() {
		if m.GatewayURL != "" || m.GatewayKey != "" {
			return fmt.Errorf("NORI_MODEL_PROVIDER must be gateway when using model gateway settings")
		}
		return nil
	}
	if m.TestURL != "" {
		return fmt.Errorf("NORI_MODEL_TEST_URL must be unset when NORI_MODEL_PROVIDER=gateway")
	}
	u, err := url.Parse(m.GatewayURL)
	validScheme := u != nil && (u.Scheme == "https" || (cloud == "local" && u.Scheme == "http" && netguard.IsLoopbackHost(u.Hostname())))
	if err != nil || !validScheme || u.Hostname() == "" || u.User != nil ||
		strings.ContainsAny(m.GatewayURL, "?#") || strings.IndexFunc(m.GatewayURL, unicode.IsSpace) >= 0 {
		return fmt.Errorf("NORI_MODEL_GATEWAY_URL must be an absolute HTTPS API base URL (or http loopback with NORI_CLOUD=local) without credentials, query or fragment")
	}
	if u.Port() != "" {
		port, err := strconv.ParseUint(u.Port(), 10, 16)
		if err != nil || port == 0 {
			return fmt.Errorf("NORI_MODEL_GATEWAY_URL must use a valid port")
		}
	}
	if (!m.BYOKRequired || m.GatewayKey != "") && !secret.ValidBearerToken(m.GatewayKey) {
		return fmt.Errorf("NORI_MODEL_GATEWAY_API_KEY must contain a bearer token")
	}
	var checks [][2]string
	if m.UsesGatewayOuter() {
		checks = append(checks, [2]string{"NORI_AGENT_MODEL", m.Outer})
	}
	if m.UsesGatewayInner() {
		checks = append(checks, [2]string{"MODEL_NAME", m.Inner})
	}
	for _, setting := range checks {
		if setting[1] == "" || strings.IndexFunc(setting[1], func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r)
		}) >= 0 {
			return fmt.Errorf("%s must contain an explicit gateway model alias without whitespace", setting[0])
		}
	}
	return nil
}
