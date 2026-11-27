package app

import (
	"context"
	"slices"
	"testing"

	"nori/internal/modelkeys"
	"nori/internal/modelprofiles"
	"nori/internal/settings"
	"nori/internal/state"
)

func TestDescribeConnectionCapabilities_Google(t *testing.T) {
	caps := describeConnectionCapabilities("google", []string{"calendar.read", "calendar.write", "files.read", "files.write", "mail.send"})
	want := []string{
		"Google Calendar (read and manage events)",
		"Google Drive (read and edit selected Docs, Sheets, and PDFs)",
		"Gmail (send emails with your approval; cannot read or search inbox)",
	}
	if !slices.Equal(caps, want) {
		t.Fatalf("got %v, want %v", caps, want)
	}
}

func TestBuildModelSummary_BYOKAndHome(t *testing.T) {
	fn := buildModelSummary(settings.Settings{Models: settings.Models{BYOKRequired: true}}, appModels{account: &modelprofiles.Account{}})

	cloud := fn(context.Background(), &state.ModelSelection{
		ChatProfileID: "vercel_ai_gateway-opus",
		ChatPinned: &state.PinnedProfile{
			Backend: modelkeys.ProviderVercelAIGateway,
			Model:   "anthropic/claude-opus-5.5",
		},
	})
	if cloud == nil || cloud.Name != "Claude Opus 5.5" || cloud.Provider != "Vercel AI Gateway" || cloud.Location != "cloud" {
		t.Fatalf("unexpected cloud model summary: %+v", cloud)
	}

	home := fn(context.Background(), &state.ModelSelection{
		ChatProfileID: "home-dev-1-llama",
		ChatPinned: &state.PinnedProfile{
			Backend: modelprofiles.BackendHome,
			Model:   "llama3.2:latest",
		},
	})
	if home == nil || home.Name != "llama3.2:latest" || home.Provider != "Home AI Server" || home.Location != "home_ai" {
		t.Fatalf("unexpected home model summary: %+v", home)
	}
}

func TestBuildCapabilitySummary_PersonalComputer(t *testing.T) {
	disabled := buildCapabilitySummary(nil, settings.Settings{}, appModels{})(context.Background())
	if disabled.PersonalComputer != "" {
		t.Fatalf("expected empty PersonalComputer when disabled, got %q", disabled.PersonalComputer)
	}
	if !slices.ContainsFunc(disabled.UploadedFiles, func(s string) bool {
		return s == "text, Markdown, CSV, and ICS files (uploaded PDFs are not supported yet)"
	}) {
		t.Fatalf("expected disabled UploadedFiles to report PDFs unsupported, got %v", disabled.UploadedFiles)
	}

	enabled := buildCapabilitySummary(nil, settings.Settings{Computer: settings.Computer{Provider: "sprites"}}, appModels{})(context.Background())
	if enabled.PersonalComputer == "" {
		t.Fatalf("expected PersonalComputer summary when enabled")
	}
	if !slices.ContainsFunc(enabled.UploadedFiles, func(s string) bool {
		return s != "" && slices.Contains([]string{s}, "text, Markdown, CSV, ICS, and PDF files (PDFs and binary files can be staged and processed on your personal computer)")
	}) {
		t.Fatalf("expected enabled UploadedFiles to include PDF support, got %v", enabled.UploadedFiles)
	}
}
