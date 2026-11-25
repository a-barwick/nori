package platform

import (
	"nori/internal/agent/responses"
	"nori/internal/conversation"
	"nori/internal/settings"
	"nori/internal/usage"
)

func ReplyModel(models settings.Models) conversation.ReplyModel {
	if models.UsesGateway() {
		return conversation.NewGatewayModel(models.GatewayURL, models.GatewayKey, models.Outer)
	}
	model := conversation.NewOpenAIModel(models.OpenAIKey, models.Outer)
	model.TestURL = models.TestURL
	model.ReasoningEffort = models.OuterReasoning
	return model
}

// TaskModel serves both the worker and conversation compaction.
func TaskModel(models settings.Models, meter usage.Meter) *responses.Client {
	_, key := models.TaskCredential()
	if models.UsesGateway() {
		return responses.NewGateway(models.GatewayURL, key, models.Inner, meter)
	}
	model := responses.New(key, models.Inner, meter)
	model.TestURL = models.TestURL
	model.DefaultReasoning = models.InnerReasoning
	return model
}
