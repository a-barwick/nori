package app

import (
	"context"

	"nori/internal/agent"
	"nori/internal/conversation"
	"nori/internal/state"
)

type unavailableReplyModel struct {
	name string
	err  error
}

func (m *unavailableReplyModel) Name() string { return m.name }
func (m *unavailableReplyModel) Respond(_ context.Context, _ conversation.ReplyRequest) (conversation.Reply, error) {
	return conversation.Reply{
		Usage: state.Usage{Provider: "outer", Model: m.name, CostStatus: "unknown", RequestStatus: "not_sent"},
	}, m.err
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
