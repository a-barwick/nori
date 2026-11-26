package app

import (
	"context"

	"nori/internal/agent"
	"nori/internal/conversation"
	"nori/internal/usage"
)

type unavailableReplyModel struct {
	name string
	err  error
}

func (m *unavailableReplyModel) Name() string { return m.name }
func (m *unavailableReplyModel) Respond(ctx context.Context, _ conversation.ReplyRequest) (conversation.Reply, error) {
	attempt := usage.Attempt(ctx, "outer", m.name)
	attempt.RequestStatus = "not_sent"
	return conversation.Reply{Usage: attempt}, m.err
}

type unavailableTaskModel struct {
	name string
	err  error
}

func (m *unavailableTaskModel) Provider() string { return "gateway" }
func (m *unavailableTaskModel) Name() string     { return m.name }
func (m *unavailableTaskModel) Respond(_ context.Context, _ agent.Request) (agent.Response, error) {
	return agent.Response{NotSent: true}, m.err
}
func (m *unavailableTaskModel) Inspect(_ context.Context, _ agent.InspectRequest) (agent.Inspection, error) {
	return agent.Inspection{}, m.err
}
