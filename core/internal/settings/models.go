package settings

import (
	"cmp"
	"fmt"
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
	DefaultOpenRouterInnerModel = "openai/gpt-6.1-terra"
)

func modelsFromEnvironment() Models {
	models := Models{
		Provider:   os.Getenv("NORI_MODEL_PROVIDER"),
		GatewayURL: os.Getenv("NORI_MODEL_GATEWAY_URL"), GatewayKey: os.Getenv("NORI_MODEL_GATEWAY_API_KEY"),
		OpenAIKey: os.Getenv("OPENAI_API_KEY"), JevKey: os.Getenv("JEV_API_KEY"), OpenRouterKey: os.Getenv("OPENROUTER_API_KEY"),
		Outer: os.Getenv("NORI_AGENT_MODEL"), Inner: os.Getenv("MODEL_NAME"), TestURL: os.Getenv("NORI_MODEL_TEST_URL"),
	}
	if !models.UsesGateway() {
		models.Outer = cmp.Or(models.Outer, DefaultOuterModel)
		models.Inner = cmp.Or(models.Inner, DefaultOpenRouterInnerModel)
	}
	return models
}

var bearerToken = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)

func (m Models) validateGateway() error {
	if !m.UsesGateway() {
		if m.Provider != "" || m.GatewayURL != "" || m.GatewayKey != "" {
			return fmt.Errorf("NORI_MODEL_PROVIDER must be gateway when using model gateway settings")
		}
		return nil
	}
	if m.TestURL != "" {
		return fmt.Errorf("NORI_MODEL_TEST_URL must be unset when NORI_MODEL_PROVIDER=gateway")
	}
	u, err := url.Parse(m.GatewayURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		strings.ContainsAny(m.GatewayURL, "?#") || strings.IndexFunc(m.GatewayURL, unicode.IsSpace) >= 0 {
		return fmt.Errorf("NORI_MODEL_GATEWAY_URL must be an absolute HTTPS API base URL without credentials, query or fragment")
	}
	if u.Port() != "" {
		port, err := strconv.ParseUint(u.Port(), 10, 16)
		if err != nil || port == 0 {
			return fmt.Errorf("NORI_MODEL_GATEWAY_URL must use a valid HTTPS port")
		}
	}
	if !bearerToken.MatchString(m.GatewayKey) {
		return fmt.Errorf("NORI_MODEL_GATEWAY_API_KEY must contain a bearer token")
	}
	for _, setting := range [][2]string{{"NORI_AGENT_MODEL", m.Outer}, {"MODEL_NAME", m.Inner}} {
		if setting[1] == "" || strings.IndexFunc(setting[1], func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r)
		}) >= 0 {
			return fmt.Errorf("%s must contain an explicit gateway model alias without whitespace", setting[0])
		}
	}
	return nil
}
