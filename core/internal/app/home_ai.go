package app

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"nori/internal/agent"
	"nori/internal/agent/responses"
	"nori/internal/conversation"
	"nori/internal/homeai"
	"nori/internal/llmguard"
	"nori/internal/modelprofiles"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/usage"
)

const homeModelURL = "https://home-model.invalid/v1"

func openHomeAI(ctx context.Context, cfg settings.Settings) (*homeai.Service, error) {
	key, err := cfg.HomeAI.EncryptionKey()
	if err != nil {
		return nil, err
	}
	listenURL := cfg.HomeAI.PostgresListenURL
	if listenURL == "" && cfg.HomeAI.PostgresURL == "" {
		listenURL = cfg.Store.PostgresListenURL
	}
	return homeai.Open(ctx, homeai.Options{
		PostgresListenURL: listenURL,
		PostgresURL:       cmp.Or(cfg.HomeAI.PostgresURL, cfg.Store.PostgresURL),
		VerificationURI:   cfg.HomeAI.VerificationURL, Key: key, MaxConns: 6,
	})
}

func homeProfiles(devices []homeai.Device, work bool) []modelprofiles.Profile {
	var profiles []modelprofiles.Profile
	for _, device := range devices {
		if device.Status == "revoked" {
			continue
		}
		for _, model := range device.Models {
			if model.Name == "" || model.Digest == "" || !slices.Contains(model.Capabilities, "completion") {
				continue
			}
			hash := sha256.Sum256([]byte(device.ID + "\x00" + model.Name + "\x00" + model.Digest))
			profile := modelprofiles.Profile{
				ID: "home-" + hex.EncodeToString(hash[:16]), Version: "v1",
				Label: model.Name + " · " + device.Name, Backend: modelprofiles.BackendHome,
				HomeDeviceID: device.ID, ModelDigest: model.Digest, Model: model.Name, Availability: device.Status,
				ChatAdapter: modelprofiles.AdapterChatCompletions, Lanes: []string{modelprofiles.LaneChat},
			}
			if work && slices.Contains(model.Capabilities, "tools") {
				profile.Lanes = append(profile.Lanes, modelprofiles.LaneTask)
				profile.TaskAdapter = modelprofiles.AdapterResponses
			}
			profiles = append(profiles, profile)
		}
	}
	return profiles
}

type homeModelTransport struct {
	service   *homeai.Service
	userID    string
	profile   modelprofiles.Profile
	operation string
}

func (t homeModelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	path := "/v1/chat/completions"
	if t.operation == "responses" {
		path = "/v1/responses"
	}
	if req.Method != http.MethodPost || req.URL.Scheme != "https" || req.URL.Host != "home-model.invalid" || req.URL.Path != path || req.Body == nil || t.service == nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, fmt.Errorf("%w: %w", llmguard.ErrDispatchBlocked, homeai.ErrOffline)
	}
	defer req.Body.Close()
	body, err := io.ReadAll(io.LimitReader(req.Body, (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 2<<20 {
		return nil, state.ErrInvalidResource
	}
	response, err := t.service.Infer(req.Context(), t.userID, t.profile.HomeDeviceID, t.profile.Model, t.profile.ModelDigest, t.operation, body)
	if homeai.IsPreflightError(err) {
		return nil, fmt.Errorf("%w: %w", llmguard.ErrDispatchBlocked, err)
	}
	return response, err
}

func homeReplyModel(service *homeai.Service, models settings.Models, userID string, profile modelprofiles.Profile) conversation.ReplyModel {
	m := conversation.NewGatewayModel(homeModelURL, "", profile.Model)
	m.ProviderName, m.LocalOnly, m.MaxRetries = llmguard.ProviderHome, true, -1
	m.Guard, m.UserID = cmp.Or(models.Guard, llmguard.ForUser(userID)), userID
	m.Client = &http.Client{Transport: homeModelTransport{service: service, userID: userID, profile: profile, operation: "chat"}}
	return m
}

func homeTaskModel(service *homeai.Service, models settings.Models, meter usage.Meter, userID string, profile modelprofiles.Profile) agent.Model {
	m := responses.NewGateway(homeModelURL, "", profile.Model, meter)
	m.ProviderName, m.LocalOnly, m.MaxRetries = llmguard.ProviderHome, true, -1
	m.Guard, m.UserID = cmp.Or(models.Guard, llmguard.ForUser(userID)), userID
	m.HTTP = &http.Client{Transport: homeModelTransport{service: service, userID: userID, profile: profile, operation: "responses"}}
	return m
}

func testHomeAI(ctx context.Context, service *homeai.Service, account *modelprofiles.Account, models settings.Models, userID, deviceID string) error {
	devices, err := service.List(ctx, userID)
	if err != nil {
		return err
	}
	idx := slices.IndexFunc(devices, func(device homeai.Device) bool { return device.ID == deviceID })
	if idx < 0 {
		return homeai.ErrDeviceNotFound
	}
	device := devices[idx]
	switch device.Status {
	case "ollama_unavailable":
		return homeai.ErrOllamaUnavailable
	case "online":
	default:
		return homeai.ErrOffline
	}
	profiles := homeProfiles([]homeai.Device{device}, false)
	if len(profiles) == 0 {
		return homeai.ErrModelUnavailable
	}
	chosen := profiles[0]
	selection, err := account.CurrentSelection(ctx)
	if err != nil {
		return err
	}
	if selection != nil {
		if i := slices.IndexFunc(profiles, func(p modelprofiles.Profile) bool { return p.ID == selection.ChatProfileID }); i >= 0 {
			chosen = profiles[i]
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err = homeReplyModel(service, models, userID, chosen).Respond(ctx, conversation.ReplyRequest{
		Messages: []json.RawMessage{json.RawMessage(`{"role":"user","content":"Reply with OK."}`)}, NoDispatch: true,
	})
	return err
}

type accountClassifier struct {
	account *modelprofiles.Account
	next    conversation.IntentClassifier
}

func (c accountClassifier) Classify(ctx context.Context, request conversation.ClassifyRequest) (conversation.Classification, error) {
	homeOnly, err := c.account.HomeOnly(ctx)
	if err != nil || homeOnly {
		return conversation.Classification{}, err
	}
	return c.next.Classify(ctx, request)
}

func (m *accountTaskModel) availability(ctx context.Context, selection *state.ModelSelection) (bool, error) {
	if selection == nil || selection.TaskPinned == nil || selection.TaskPinned.Backend != modelprofiles.BackendHome {
		return true, nil
	}
	if m.home == nil {
		return false, nil
	}
	devices, err := m.home.List(ctx, m.userID)
	if err != nil {
		return false, err
	}
	for _, device := range devices {
		if device.ID != selection.TaskPinned.HomeDeviceID {
			continue
		}
		return device.Status == "online", nil
	}
	return false, nil
}
