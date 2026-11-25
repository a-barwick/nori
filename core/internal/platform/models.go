package platform

import (
	"nori/internal/agent"
	"nori/internal/agent/anthropic"
	"nori/internal/agent/responses"
	"nori/internal/conversation"
	"nori/internal/llmguard"
	"nori/internal/settings"
	"nori/internal/usage"
)

func ReplyModel(models settings.Models) conversation.ReplyModel {
	return ReplyModelForUser(models, "")
}

func ReplyModelForUser(models settings.Models, userID string) conversation.ReplyModel {
	switch models.EffectiveOuterProvider() {
	case "gateway":
		model := conversation.NewGatewayModel(models.GatewayURL, models.GatewayKey, models.Outer)
		model.Guard = llmguard.Default()
		model.UserID = userID
		return model
	case "anthropic":
		model := conversation.NewAnthropicModel(models.AnthropicKey, models.Outer)
		model.TestURL = models.TestURL
		model.Guard = llmguard.Default()
		model.UserID = userID
		return model
	case "openrouter":
		model := conversation.NewOpenRouterModel(models.OpenRouterKey, models.Outer)
		model.TestURL = models.TestURL
		model.Guard = llmguard.Default()
		model.UserID = userID
		return model
	default:
		model := conversation.NewOpenAIModel(models.OpenAIKey, models.Outer)
		model.TestURL = models.TestURL
		model.ReasoningEffort = models.OuterReasoning
		model.Guard = llmguard.Default()
		model.UserID = userID
		return model
	}
}

// TaskModel serves both the worker and conversation compaction.
func TaskModel(models settings.Models, meter usage.Meter) agent.Model {
	return TaskModelForUser(models, meter, "")
}

func TaskModelForUser(models settings.Models, meter usage.Meter, userID string) agent.Model {
	_, key := models.TaskCredential()
	switch models.EffectiveInnerProvider() {
	case "gateway":
		model := responses.NewGateway(models.GatewayURL, key, models.Inner, meter)
		model.Guard = llmguard.Default()
		model.UserID = userID
		return model
	case "anthropic":
		model := anthropic.New(key, models.Inner, meter)
		model.TestURL = models.TestURL
		model.Guard = llmguard.Default()
		model.UserID = userID
		return model
	default:
		model := responses.New(key, models.Inner, meter)
		model.ProviderName = models.EffectiveInnerProvider()
		model.TestURL = models.TestURL
		model.DefaultReasoning = models.InnerReasoning
		model.Guard = llmguard.Default()
		model.UserID = userID
		return model
	}
}

func ClassifierForUser(models settings.Models, userID string) *conversation.JevClassifier {
	classifier := conversation.NewJevClassifier(models.JevKey)
	classifier.Guard = llmguard.Default()
	classifier.UserID = userID
	return classifier
}
