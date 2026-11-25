package settings

import (
	"cmp"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Canonical default model IDs for non-gateway deployments.
// Direct OpenAI uses the bare model ID; OpenRouter uses the provider-prefixed ID.
const (
	DefaultOuterModel           = "gpt-6.1-sol"
	DefaultInnerModel           = "gpt-6.1-terra"
	DefaultOpenRouterOuterModel = "openai/gpt-6.1-sol"
	DefaultOpenRouterInnerModel = "openai/gpt-6.1-terra"
	DefaultAnthropicOuterModel  = "claude-sonnet-4-5"
	DefaultAnthropicInnerModel  = "claude-sonnet-4-5"
)

// ValidModelProvider reports whether p is an accepted model provider selector.
func ValidModelProvider(p string) bool {
	switch p {
	case "", "openai", "anthropic", "openrouter", "gateway":
		return true
	default:
		return false
	}
}

// ValidEnvProfile reports whether p is an accepted environment profile.
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

func profileKeyVarName(prefix, profile string) string {
	switch profile {
	case "test":
		return prefix + "_TEST_API_KEY"
	case "prod":
		if strings.TrimSpace(os.Getenv(prefix+"_PROD_API_KEY")) != "" {
			return prefix + "_PROD_API_KEY"
		}
		return prefix + "_API_KEY"
	case "dev":
		if strings.TrimSpace(os.Getenv(prefix+"_DEV_API_KEY")) != "" {
			return prefix + "_DEV_API_KEY"
		}
		return prefix + "_API_KEY"
	default:
		return prefix + "_API_KEY"
	}
}

// resolveProfileKey isolates provider keys across prod, dev, and test profiles.
// In "test" profile it reads ONLY <PREFIX>_TEST_API_KEY and never falls back to
// dev/prod or unprefixed <PREFIX>_API_KEY.
func resolveProfileKey(prefix, profile string) string {
	switch profile {
	case "test":
		return strings.TrimSpace(os.Getenv(prefix + "_TEST_API_KEY"))
	case "prod":
		return cmp.Or(
			strings.TrimSpace(os.Getenv(prefix+"_PROD_API_KEY")),
			strings.TrimSpace(os.Getenv(prefix+"_API_KEY")),
		)
	case "dev":
		return cmp.Or(
			strings.TrimSpace(os.Getenv(prefix+"_DEV_API_KEY")),
			strings.TrimSpace(os.Getenv(prefix+"_API_KEY")),
		)
	default:
		return strings.TrimSpace(os.Getenv(prefix + "_API_KEY"))
	}
}

func validateProfileKeyIsolation(profile string) error {
	for _, prefix := range []string{"OPENAI", "ANTHROPIC", "OPENROUTER"} {
		prodKey := strings.TrimSpace(os.Getenv(prefix + "_PROD_API_KEY"))
		devKey := strings.TrimSpace(os.Getenv(prefix + "_DEV_API_KEY"))
		testKey := strings.TrimSpace(os.Getenv(prefix + "_TEST_API_KEY"))
		baseKey := strings.TrimSpace(os.Getenv(prefix + "_API_KEY"))

		if prodKey != "" && devKey != "" && prodKey == devKey {
			return fmt.Errorf("%s_PROD_API_KEY and %s_DEV_API_KEY must be distinct keys", prefix, prefix)
		}
		if prodKey != "" && testKey != "" && prodKey == testKey {
			return fmt.Errorf("%s_PROD_API_KEY and %s_TEST_API_KEY must be distinct keys", prefix, prefix)
		}
		if devKey != "" && testKey != "" && devKey == testKey {
			return fmt.Errorf("%s_DEV_API_KEY and %s_TEST_API_KEY must be distinct keys", prefix, prefix)
		}
		if profile == "test" && testKey != "" && baseKey != "" && testKey == baseKey {
			return fmt.Errorf("%s_TEST_API_KEY must not reuse %s_API_KEY", prefix, prefix)
		}
	}
	return nil
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

func modelsFromEnvironment(profile string) Models {
	models := Models{
		EnvProfile:     profile,
		Provider:       strings.ToLower(strings.TrimSpace(os.Getenv("NORI_MODEL_PROVIDER"))),
		OuterProvider:  strings.ToLower(strings.TrimSpace(os.Getenv("NORI_OUTER_PROVIDER"))),
		InnerProvider:  strings.ToLower(strings.TrimSpace(os.Getenv("NORI_INNER_PROVIDER"))),
		GatewayURL:     strings.TrimSpace(os.Getenv("NORI_MODEL_GATEWAY_URL")),
		GatewayKey:     strings.TrimSpace(os.Getenv("NORI_MODEL_GATEWAY_API_KEY")),
		LocalURL:       strings.TrimSpace(os.Getenv("NORI_LOCAL_MODEL_URL")),
		LocalModel:     strings.TrimSpace(os.Getenv("NORI_LOCAL_MODEL_TAG")),
		ProfilesFile:   strings.TrimSpace(os.Getenv("NORI_MODEL_PROFILES_FILE")),
		ProfilesJSON:   strings.TrimSpace(os.Getenv("NORI_MODEL_PROFILES")),
		PickerUsers:    parseCSVList(os.Getenv("NORI_MODEL_PICKER_USERS")),
		OpenAIKey:      resolveProfileKey("OPENAI", profile),
		AnthropicKey:   resolveProfileKey("ANTHROPIC", profile),
		OpenRouterKey:  resolveProfileKey("OPENROUTER", profile),
		JevKey:         strings.TrimSpace(os.Getenv("JEV_API_KEY")),
		Outer:          strings.TrimSpace(os.Getenv("NORI_AGENT_MODEL")),
		Inner:          strings.TrimSpace(os.Getenv("MODEL_NAME")),
		TestURL:        strings.TrimSpace(os.Getenv("NORI_MODEL_TEST_URL")),
		OuterReasoning: strings.ToLower(strings.TrimSpace(os.Getenv("NORI_OUTER_REASONING_EFFORT"))),
		InnerReasoning: strings.ToLower(strings.TrimSpace(os.Getenv("NORI_INNER_REASONING_EFFORT"))),
		MaxUSD:         parseOptionalNonNegativeFloat("NORI_LLM_MAX_USD"),
	}
	if models.InnerReasoning == "" {
		models.InnerReasoning = "low"
	}
	if models.Outer == "" {
		switch models.EffectiveOuterProvider() {
		case "openai":
			models.Outer = DefaultOuterModel
		case "anthropic":
			models.Outer = DefaultAnthropicOuterModel
		case "openrouter":
			models.Outer = DefaultOpenRouterOuterModel
		}
	}
	if models.Inner == "" {
		switch models.EffectiveInnerProvider() {
		case "openai":
			models.Inner = DefaultInnerModel
		case "anthropic":
			models.Inner = DefaultAnthropicInnerModel
		case "openrouter":
			models.Inner = DefaultOpenRouterInnerModel
		}
	}
	return models
}

// EffectiveOuterProvider returns the resolved provider for the outer conversation model.
func (m Models) EffectiveOuterProvider() string {
	return cmp.Or(m.OuterProvider, m.Provider, "openai")
}

// EffectiveInnerProvider returns the resolved provider for the inner task/compaction/dream model.
func (m Models) EffectiveInnerProvider() string {
	if m.InnerProvider != "" {
		return m.InnerProvider
	}
	if m.Provider != "" {
		return m.Provider
	}
	if m.Inner == DefaultInnerModel {
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
		return profileKeyVarName("ANTHROPIC", m.EnvProfile), m.AnthropicKey
	case "openrouter":
		return profileKeyVarName("OPENROUTER", m.EnvProfile), m.OpenRouterKey
	default:
		return profileKeyVarName("OPENAI", m.EnvProfile), m.OpenAIKey
	}
}

func (m Models) TaskCredential() (name, key string) {
	switch m.EffectiveInnerProvider() {
	case "gateway":
		return "NORI_MODEL_GATEWAY_API_KEY", m.GatewayKey
	case "anthropic":
		return profileKeyVarName("ANTHROPIC", m.EnvProfile), m.AnthropicKey
	case "openai":
		return profileKeyVarName("OPENAI", m.EnvProfile), m.OpenAIKey
	default:
		return profileKeyVarName("OPENROUTER", m.EnvProfile), m.OpenRouterKey
	}
}

var bearerToken = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)

func isLoopbackHostname(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ValidReasoningEffort reports whether e is an accepted reasoning/thinking effort level.
func ValidReasoningEffort(e string) bool {
	switch e {
	case "", "none", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func parseCSVList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
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
		return fmt.Errorf("NORI_OUTER_REASONING_EFFORT must be none, low, medium, high, or max")
	}
	if !ValidReasoningEffort(m.InnerReasoning) {
		return fmt.Errorf("NORI_INNER_REASONING_EFFORT must be none, low, medium, high, or max")
	}
	if math.IsNaN(m.MaxUSD) || math.IsInf(m.MaxUSD, 0) || m.MaxUSD < 0 {
		return fmt.Errorf("NORI_LLM_MAX_USD must be a non-negative dollar amount")
	}
	if m.LocalURL != "" || m.LocalModel != "" {
		if cloud != "local" {
			return fmt.Errorf("NORI_LOCAL_MODEL_URL and NORI_LOCAL_MODEL_TAG are only allowed when NORI_CLOUD=local")
		}
		if m.LocalURL == "" || m.LocalModel == "" {
			return fmt.Errorf("NORI_LOCAL_MODEL_URL and NORI_LOCAL_MODEL_TAG must both be set when configuring a local model")
		}
		u, err := url.Parse(m.LocalURL)
		if err != nil || u == nil || u.Scheme != "http" || !isLoopbackHostname(u.Hostname()) || u.User != nil ||
			strings.ContainsAny(m.LocalURL, "?#") || strings.IndexFunc(m.LocalURL, unicode.IsSpace) >= 0 {
			return fmt.Errorf("NORI_LOCAL_MODEL_URL must be an http loopback URL without credentials, query or fragment")
		}
		if strings.IndexFunc(m.LocalModel, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return fmt.Errorf("NORI_LOCAL_MODEL_TAG must not contain whitespace or control characters")
		}
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
	validScheme := u != nil && (u.Scheme == "https" || (cloud == "local" && u.Scheme == "http" && isLoopbackHostname(u.Hostname())))
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
	if !bearerToken.MatchString(m.GatewayKey) {
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
