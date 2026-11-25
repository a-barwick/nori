package platform

import (
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
	var model *conversation.OpenAIModel
	if models.UsesGateway() {
		model = conversation.NewGatewayModel(models.GatewayURL, models.GatewayKey, models.Outer)
	} else {
		model = conversation.NewOpenAIModel(models.OpenAIKey, models.Outer)
		model.TestURL = models.TestURL
		model.ReasoningEffort = models.OuterReasoning
	}
	model.Guard = llmguard.Default()
	model.UserID = userID
	return model
}

// TaskModel serves both the worker and conversation compaction.
func TaskModel(models settings.Models, meter usage.Meter) *responses.Client {
	return TaskModelForUser(models, meter, "")
}

func TaskModelForUser(models settings.Models, meter usage.Meter, userID string) *responses.Client {
	_, key := models.TaskCredential()
	var model *responses.Client
	if models.UsesGateway() {
		model = responses.NewGateway(models.GatewayURL, key, models.Inner, meter)
	} else {
		model = responses.New(key, models.Inner, meter)
		model.TestURL = models.TestURL
		model.DefaultReasoning = models.InnerReasoning
	}
	model.Guard = llmguard.Default()
	model.UserID = userID
	return model
}

func ClassifierForUser(models settings.Models, userID string) *conversation.JevClassifier {
	classifier := conversation.NewJevClassifier(models.JevKey)
	classifier.Guard = llmguard.Default()
	classifier.UserID = userID
	return classifier
}
