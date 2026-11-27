package app

import (
	"context"
	"slices"
	"strings"

	"nori/internal/agent"
	"nori/internal/connections"
	"nori/internal/conversation"
	"nori/internal/modelkeys"
	"nori/internal/modelprofiles"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/state"
)

func buildCapabilitySummary(services *platform.AssistantServices, cfg settings.Settings, models appModels) func(context.Context) *conversation.CapabilitySummary {
	return func(ctx context.Context) *conversation.CapabilitySummary {
		webSearch := "unavailable"
		if strings.TrimSpace(cfg.ExaSearchKey) != "" || supportsTaskNativeSearch(ctx, models) {
			webSearch = "available"
		}
		summary := &conversation.CapabilitySummary{
			WebSearch: webSearch,
			UploadedFiles: []string{
				"images (PNG, JPEG, WebP)",
				"text, Markdown, CSV, and ICS files (uploaded PDFs are not supported yet)",
			},
			BuiltIn: []string{
				"one-time reminders",
				"recurring automations and monitors",
				"saved artifacts",
				"memory",
			},
			ConnectedApps: []conversation.ConnectedAppSummary{},
			MCPServers:    []conversation.MCPServerSummary{},
		}
		if services != nil && services.Connections != nil {
			if list, err := services.Connections.List(ctx); err == nil {
				for _, conn := range list {
					if conn.Status == connections.StatusDisconnected || conn.Status == connections.StatusNotConnected {
						continue
					}
					appTitle := strings.TrimSpace(conn.Title)
					if appTitle == "" {
						appTitle = conn.IntegrationID
					}
					account := ""
					if conn.Account != nil {
						account = conn.Account.Label
					}
					summary.ConnectedApps = append(summary.ConnectedApps, conversation.ConnectedAppSummary{
						App:          appTitle,
						Account:      account,
						Status:       conn.Status,
						Capabilities: describeConnectionCapabilities(conn.IntegrationID, conn.Capabilities),
					})
				}
			}
		}
		if services != nil && services.MCP != nil {
			if servers, err := services.MCP.List(ctx); err == nil {
				for _, srv := range servers {
					if !srv.Enabled {
						continue
					}
					status := strings.TrimSpace(srv.Observed.Status)
					if status == "" {
						status = "not_connected"
					}
					summary.MCPServers = append(summary.MCPServers, conversation.MCPServerSummary{
						Name:      srv.Name,
						Status:    status,
						ToolCount: srv.Observed.ToolCount,
					})
				}
			}
		}
		return summary
	}
}

func supportsTaskNativeSearch(ctx context.Context, models appModels) bool {
	if models.account != nil {
		sel, err := models.account.CurrentSelection(ctx)
		if err == nil && sel != nil && sel.TaskPinned != nil && sel.TaskPinned.Backend == modelkeys.ProviderOpenAI {
			return true
		}
		return false
	}
	native, ok := models.defaultTaskModel.(agent.NativeWebSearchModel)
	return ok && native.SupportsNativeWebSearch()
}

func describeConnectionCapabilities(integrationID string, caps []string) []string {
	if integrationID == "google" {
		out := []string{}
		hasCalRead := slices.Contains(caps, "calendar.read")
		hasCalWrite := slices.Contains(caps, "calendar.write")
		switch {
		case hasCalWrite:
			out = append(out, "Google Calendar (read and manage events)")
		case hasCalRead:
			out = append(out, "Google Calendar (read-only)")
		}
		hasDriveRead := slices.Contains(caps, "files.read")
		hasDriveWrite := slices.Contains(caps, "files.write")
		switch {
		case hasDriveWrite:
			out = append(out, "Google Drive (read and edit selected Docs, Sheets, and PDFs)")
		case hasDriveRead:
			out = append(out, "Google Drive (read selected Docs, Sheets, and PDFs)")
		}
		if slices.Contains(caps, "mail.send") {
			out = append(out, "Gmail (send emails with your approval; cannot read or search inbox)")
		}
		return out
	}
	if len(caps) == 0 {
		return nil
	}
	return append([]string(nil), caps...)
}

func buildModelSummary(cfg settings.Settings, models appModels) func(context.Context, *state.ModelSelection) *conversation.ModelSummary {
	return func(ctx context.Context, sel *state.ModelSelection) *conversation.ModelSummary {
		if cfg.Models.BYOKRequired && models.account != nil {
			if sel.IsEmpty() || sel.ChatPinned == nil {
				if current, err := models.account.CurrentSelection(ctx); err == nil && current != nil {
					sel = current
				}
			}
			if sel == nil || sel.ChatProfileID == "" {
				return nil
			}
			backend, model := "", ""
			if sel.ChatPinned != nil {
				backend, model = sel.ChatPinned.Backend, sel.ChatPinned.Model
			}
			name, provider, location := modelprofiles.Describe(sel.ChatProfileID, backend, model)
			if name == "" {
				return nil
			}
			return &conversation.ModelSummary{Name: name, Provider: provider, Location: location}
		}
		name, provider, location := modelprofiles.Describe("", cfg.Models.EffectiveOuterProvider(), cfg.Models.Outer)
		if name == "" {
			return nil
		}
		return &conversation.ModelSummary{Name: name, Provider: provider, Location: location}
	}
}
