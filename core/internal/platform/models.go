package platform

import (
	"cmp"
	"context"
	"errors"
	"strings"

	"nori/internal/agent"
	"nori/internal/agent/anthropic"
	"nori/internal/agent/responses"
	"nori/internal/conversation"
	"nori/internal/llmguard"
	"nori/internal/modelprofiles"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/usage"
)

const vercelGatewayBaseURL = "https://ai-gateway.vercel.sh/v1"

// IsHostedModelAllowed reports whether model is on Nori's strict known-tariff
// platform-hosted model allowlist.
func IsHostedModelAllowed(model string) bool {
	return settings.IsHostedModelAllowed(model)
}

type unavailableHostedModel struct{ name string }

func (m unavailableHostedModel) Name() string { return m.name }
func (m unavailableHostedModel) Respond(context.Context, conversation.ReplyRequest) (conversation.Reply, error) {
	return conversation.Reply{}, errors.New("selected hosted model is unavailable")
}

type unavailableHostedTask struct{ name string }

func (m unavailableHostedTask) Name() string     { return m.name }
func (m unavailableHostedTask) Provider() string { return "" }
func (m unavailableHostedTask) Respond(context.Context, agent.Request) (agent.Response, error) {
	return agent.Response{NotSent: true}, errors.New("selected hosted model is unavailable")
}
func (m unavailableHostedTask) Inspect(context.Context, agent.InspectRequest) (agent.Inspection, error) {
	return agent.Inspection{}, errors.New("selected hosted model is unavailable")
}

func hostedGatewayBase(models settings.Models, profile modelprofiles.Profile) string {
	if models.TestURL != "" {
		// Settings accepts this override only for an HTTP loopback server in local development.
		return strings.TrimRight(models.TestURL, "/") + "/v1"
	}
	return cmp.Or(strings.TrimSpace(profile.Endpoint), vercelGatewayBaseURL)
}

// ReplyModelForHosted builds a guarded Chat model routed through Nori's platform
// Vercel AI Gateway key. Only strictly allowlisted known-tariff models are admitted,
// and all usage records are explicitly tagged Billing="hosted".
func ReplyModelForHosted(models settings.Models, userID string, p modelprofiles.Profile, reasoning, platformGatewayKey string) conversation.ReplyModel {
	if strings.TrimSpace(platformGatewayKey) == "" || !IsHostedModelAllowed(p.Model) {
		return unavailableHostedModel{name: p.Model}
	}
	base := hostedGatewayBase(models, p)
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(p.Model)), "anthropic/") {
		model := conversation.NewGatewayAnthropicModel(base, platformGatewayKey, p.Model)
		model.Client = cmp.Or(models.CloudDispatchClient, model.Client)
		model.Guard, model.UserID, model.ReasoningEffort = modelGuard(models, userID), userID, reasoning
		model.BillingMode = state.UsageBillingHosted
		return model
	}
	model := conversation.NewGatewayModel(base, platformGatewayKey, p.Model)
	model.Client = cmp.Or(models.CloudDispatchClient, model.Client)
	model.Guard, model.UserID, model.ReasoningEffort = modelGuard(models, userID), userID, reasoning
	model.AllowGatewayReasoning = true
	model.BillingMode = state.UsageBillingHosted
	return model
}

// TaskModelForHosted builds a guarded Work/Task model routed through Nori's platform
// Vercel AI Gateway key. Only strictly allowlisted known-tariff models are admitted,
// and all usage records are explicitly tagged Billing="hosted".
func TaskModelForHosted(models settings.Models, meter usage.Meter, userID string, p modelprofiles.Profile, reasoning, platformGatewayKey string) agent.Model {
	if strings.TrimSpace(platformGatewayKey) == "" || !IsHostedModelAllowed(p.Model) {
		return unavailableHostedTask{name: p.Model}
	}
	base := hostedGatewayBase(models, p)
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(p.Model)), "anthropic/") {
		model := anthropic.NewGateway(base, platformGatewayKey, p.Model, meter)
		model.HTTP = cmp.Or(models.CloudDispatchClient, model.HTTP)
		model.Guard, model.UserID, model.DefaultReasoning = modelGuard(models, userID), userID, reasoning
		model.BillingMode = state.UsageBillingHosted
		return model
	}
	model := responses.NewGateway(base, platformGatewayKey, p.Model, meter)
	model.HTTP = cmp.Or(models.CloudDispatchClient, model.HTTP)
	model.Guard, model.UserID, model.DefaultReasoning = modelGuard(models, userID), userID, reasoning
	model.AllowGatewayReasoning = true
	model.BillingMode = state.UsageBillingHosted
	return model
}

type unavailableBYOKModel struct{ name string }

func (m unavailableBYOKModel) Name() string { return m.name }
func (m unavailableBYOKModel) Respond(context.Context, conversation.ReplyRequest) (conversation.Reply, error) {
	return conversation.Reply{}, errors.New("selected model provider is unavailable")
}

type unavailableBYOKTask struct{ name string }

func (m unavailableBYOKTask) Name() string     { return m.name }
func (m unavailableBYOKTask) Provider() string { return "" }
func (m unavailableBYOKTask) Respond(context.Context, agent.Request) (agent.Response, error) {
	return agent.Response{NotSent: true}, errors.New("selected model provider is unavailable")
}
func (m unavailableBYOKTask) Inspect(context.Context, agent.InspectRequest) (agent.Inspection, error) {
	return agent.Inspection{}, errors.New("selected model provider is unavailable")
}

func ReplyModelForBYOK(models settings.Models, userID string, p modelprofiles.Profile, reasoning, key string) conversation.ReplyModel {
	if strings.TrimSpace(key) == "" {
		return unavailableBYOKModel{name: p.Model}
	}
	if p.Backend == "anthropic" || (p.Backend == "vercel_ai_gateway" && strings.HasPrefix(p.Model, "anthropic/")) {
		model := conversation.NewAnthropicModel(key, p.Model)
		label := "Anthropic"
		if p.Backend == "vercel_ai_gateway" {
			base := p.Endpoint
			if base == "" {
				base = vercelGatewayBaseURL
			}
			model = conversation.NewGatewayAnthropicModel(base, key, p.Model)
			label = "Vercel AI Gateway"
		} else if p.Endpoint != "" {
			model = conversation.NewGatewayAnthropicModel(p.Endpoint, key, p.Model)
		}
		model.TestURL = models.TestURL
		model.Client = cmp.Or(models.CloudDispatchClient, model.Client)
		model.Guard, model.UserID, model.ReasoningEffort, model.BYOKProviderLabel = modelGuard(models, userID), userID, reasoning, label
		model.BillingMode = state.UsageBillingBYOK
		return model
	}
	var model *conversation.OpenAIModel
	var label string
	switch p.Backend {
	case "openai":
		model, label = conversation.NewOpenAIModel(key, p.Model), "OpenAI"
	case "google":
		base := p.Endpoint
		if base == "" {
			base = "https://generativelanguage.googleapis.com/v1beta/openai"
		}
		model, label = conversation.NewGatewayModel(base, key, p.Model), "Google"
		model.GoogleCompatibility = true
	case "vercel_ai_gateway":
		base := p.Endpoint
		if base == "" {
			base = vercelGatewayBaseURL
		}
		model, label = conversation.NewGatewayModel(base, key, p.Model), "Vercel AI Gateway"
	case "openrouter":
		model, label = conversation.NewOpenRouterModel(key, p.Model), "OpenRouter"
		if p.Endpoint != "" {
			model = conversation.NewGatewayModel(p.Endpoint, key, p.Model)
		}
	default:
		return unavailableBYOKModel{name: p.Model}
	}
	model.TestURL = models.TestURL
	model.Client = cmp.Or(models.CloudDispatchClient, model.Client)
	model.Guard, model.UserID, model.ReasoningEffort = modelGuard(models, userID), userID, reasoning
	model.BYOKProviderLabel, model.AllowGatewayReasoning = label, true
	model.BillingMode = state.UsageBillingBYOK
	return model
}

func TaskModelForBYOK(models settings.Models, meter usage.Meter, userID string, p modelprofiles.Profile, reasoning, key string) agent.Model {
	if strings.TrimSpace(key) == "" {
		return unavailableBYOKTask{name: p.Model}
	}
	if p.Backend == "anthropic" || (p.Backend == "vercel_ai_gateway" && strings.HasPrefix(p.Model, "anthropic/")) {
		model := anthropic.New(key, p.Model, meter)
		label := "Anthropic"
		if p.Backend == "vercel_ai_gateway" {
			base := p.Endpoint
			if base == "" {
				base = vercelGatewayBaseURL
			}
			model = anthropic.NewGateway(base, key, p.Model, meter)
			label = "Vercel AI Gateway"
		} else if p.Endpoint != "" {
			model = anthropic.NewGateway(p.Endpoint, key, p.Model, meter)
		}
		model.TestURL = models.TestURL
		model.HTTP = cmp.Or(models.CloudDispatchClient, model.HTTP)
		model.Guard, model.UserID, model.DefaultReasoning, model.BYOKProviderLabel = modelGuard(models, userID), userID, reasoning, label
		model.BillingMode = state.UsageBillingBYOK
		return model
	}
	if p.Backend == "google" {
		model := responses.NewGoogle(p.Endpoint, key, p.Model, meter)
		model.HTTP = cmp.Or(models.CloudDispatchClient, model.HTTP)
		model.Guard, model.UserID, model.DefaultReasoning = modelGuard(models, userID), userID, reasoning
		model.BillingMode = state.UsageBillingBYOK
		return model
	}
	var model *responses.Client
	var label string
	switch p.Backend {
	case "openai":
		model, label = responses.New(key, p.Model, meter), "OpenAI"
		model.ProviderName = "openai"
	case "vercel_ai_gateway":
		base := p.Endpoint
		if base == "" {
			base = vercelGatewayBaseURL
		}
		model, label = responses.NewGateway(base, key, p.Model, meter), "Vercel AI Gateway"
	case "openrouter":
		model, label = responses.New(key, p.Model, meter), "OpenRouter"
		model.ProviderName = "openrouter"
		if p.Endpoint != "" {
			model = responses.NewGateway(p.Endpoint, key, p.Model, meter)
		}
	default:
		return unavailableBYOKTask{name: p.Model}
	}
	model.TestURL = models.TestURL
	model.HTTP = cmp.Or(models.CloudDispatchClient, model.HTTP)
	model.Guard, model.UserID, model.DefaultReasoning = modelGuard(models, userID), userID, reasoning
	model.BYOKProviderLabel, model.AllowGatewayReasoning, model.Byok = label, true, true
	model.BillingMode = state.UsageBillingBYOK
	return model
}

func modelGuard(models settings.Models, userID string) *llmguard.Guard {
	if models.Guard != nil {
		return models.Guard
	}
	return llmguard.ForUser(userID)
}

func userBillingMode(models settings.Models, key string) string {
	if models.TestURL != "" && strings.TrimSpace(key) == "" {
		return state.UsageBillingLocal
	}
	return state.UsageBillingHosted
}

func ReplyModelForUser(models settings.Models, userID string) conversation.ReplyModel {
	guard := modelGuard(models, userID)
	_, outerKey := models.OuterCredential()
	billingMode := userBillingMode(models, outerKey)
	switch models.EffectiveOuterProvider() {
	case "gateway":
		model := conversation.NewGatewayModel(models.GatewayURL, models.GatewayKey, models.Outer)
		model.Guard = guard
		model.UserID = userID
		model.BillingMode = billingMode
		return model
	case "anthropic":
		model := conversation.NewAnthropicModel(models.AnthropicKey, models.Outer)
		model.TestURL = models.TestURL
		model.ReasoningEffort = models.OuterReasoning
		model.Guard = guard
		model.UserID = userID
		model.BillingMode = billingMode
		return model
	case "openrouter":
		model := conversation.NewOpenRouterModel(models.OpenRouterKey, models.Outer)
		model.TestURL = models.TestURL
		model.Guard = guard
		model.UserID = userID
		model.BillingMode = billingMode
		return model
	default:
		model := conversation.NewOpenAIModel(models.OpenAIKey, models.Outer)
		model.TestURL = models.TestURL
		model.LocalOnly = models.LocalModel
		model.ReasoningEffort = models.OuterReasoning
		model.Guard = guard
		model.UserID = userID
		model.BillingMode = billingMode
		return model
	}
}

func TaskModelForUser(models settings.Models, meter usage.Meter, userID string) agent.Model {
	guard := modelGuard(models, userID)
	_, key := models.TaskCredential()
	billingMode := userBillingMode(models, key)
	switch models.EffectiveInnerProvider() {
	case "gateway":
		model := responses.NewGateway(models.GatewayURL, key, models.Inner, meter)
		model.Guard = guard
		model.UserID = userID
		model.BillingMode = billingMode
		return model
	case "anthropic":
		model := anthropic.New(key, models.Inner, meter)
		model.TestURL = models.TestURL
		model.DefaultReasoning = models.InnerReasoning
		model.Guard = guard
		model.UserID = userID
		model.BillingMode = billingMode
		return model
	default:
		model := responses.New(key, models.Inner, meter)
		model.ProviderName = models.EffectiveInnerProvider()
		model.TestURL = models.TestURL
		model.LocalOnly = models.LocalModel
		model.DefaultReasoning = models.InnerReasoning
		model.Guard = guard
		model.UserID = userID
		model.BillingMode = billingMode
		return model
	}
}

func ClassifierForUser(models settings.Models, userID string) *conversation.JevClassifier {
	classifier := conversation.NewJevClassifier(models.JevKey)
	classifier.Client = cmp.Or(models.CloudDispatchClient, classifier.Client)
	classifier.Guard = modelGuard(models, userID)
	classifier.UserID = userID
	return classifier
}
