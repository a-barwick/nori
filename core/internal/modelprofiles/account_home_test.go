package modelprofiles_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"nori/internal/agent"
	"nori/internal/conversation"
	"nori/internal/llmguard"
	"nori/internal/modelkeys"
	"nori/internal/modelprofiles"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/transcript"
	"nori/internal/usage"
	"nori/internal/users"
)

func TestAccountHomeSelectionPinsDeviceAndBlocksCloudAfterRevocation(t *testing.T) {
	ctx := context.Background()
	backend := memstore.New()
	profile := modelprofiles.Profile{
		ID: "home-mac", Version: "v1", Backend: modelprofiles.BackendHome, Model: "local-model",
		HomeDeviceID: "registered-device", ModelDigest: "initial-digest", Lanes: []string{modelprofiles.LaneChat},
	}
	inventory := []modelprofiles.Profile{profile}
	account := &modelprofiles.Account{
		Keys:         &modelkeys.Service{},
		Store:        records.NewShared(backend, users.User{ID: "owner", Email: "owner@example.test", TimeZone: "UTC"}, 1),
		HomeProfiles: func(context.Context) ([]modelprofiles.Profile, error) { return inventory, nil },
	}
	foreign := &modelprofiles.Account{
		Keys:         &modelkeys.Service{},
		Store:        records.NewShared(backend, users.User{ID: "22222222-2222-4222-8222-222222222222", Email: "other@example.test", TimeZone: "UTC"}, 1),
		HomeProfiles: func(context.Context) ([]modelprofiles.Profile, error) { return nil, nil },
	}

	if _, err := foreign.Save(ctx, state.ModelSelection{ChatProfileID: profile.ID, ChatPinned: &state.PinnedProfile{Backend: modelprofiles.BackendHome, HomeDeviceID: profile.HomeDeviceID}}, 0, nil); !errors.Is(err, state.ErrInvalidResource) {
		t.Fatalf("foreign connector or supplied pin was accepted: %v", err)
	}
	enable := true
	view, err := account.Save(ctx, state.ModelSelection{ChatProfileID: profile.ID}, 0, &enable)
	if err != nil {
		t.Fatal(err)
	}
	if view.Selection.ChatPinned == nil || view.Selection.ChatPinned.HomeDeviceID != profile.HomeDeviceID || view.Selection.ChatPinned.ModelDigest != profile.ModelDigest {
		t.Fatalf("device and model were not pinned: %+v", view.Selection)
	}
	cloud := modelprofiles.PinAccountSelection(&state.ModelSelection{TaskProfileID: "openai-sol"})
	if _, _, _, err := account.Resolve(ctx, modelprofiles.LaneTask, cloud); !errors.Is(err, modelprofiles.ErrHomeOnly) {
		t.Fatalf("previous cloud run was not fenced: %v", err)
	}
	if _, err := account.Save(ctx, state.ModelSelection{ChatProfileID: profile.ID, TaskProfileID: profile.ID}, view.Revision, nil); !errors.Is(err, state.ErrInvalidResource) {
		t.Fatalf("chat profile was accepted for Work: %v", err)
	}

	legacy, err := account.Save(ctx, state.ModelSelection{ChatProfileID: profile.ID}, view.Revision, nil)
	if err != nil || !legacy.Selection.HomeOnly || legacy.Revision != view.Revision {
		t.Fatalf("older client's update changed the policy: %+v, %v", legacy, err)
	}

	inventory = nil
	offline, err := account.View(ctx)
	if err != nil || len(offline.ModelProfiles.Profiles) != 0 || offline.Selection.ChatProfileID != profile.ID {
		t.Fatalf("disconnected selection changed: %+v, %v", offline, err)
	}
	resolved, _, _, err := account.Resolve(ctx, modelprofiles.LaneChat, nil)
	if err != nil || resolved.HomeDeviceID != profile.HomeDeviceID || resolved.ModelDigest != profile.ModelDigest {
		t.Fatalf("disconnected model was rerouted: %+v, %v", resolved, err)
	}
}

func TestHomeOnlyBlocksPreviouslyResolvedCloudClients(t *testing.T) {
	ctx := context.Background()
	profile := modelprofiles.Profile{ID: "home-mac", Backend: modelprofiles.BackendHome, Model: "local", HomeDeviceID: "device", ModelDigest: "digest", Lanes: []string{modelprofiles.LaneChat}}
	account := &modelprofiles.Account{
		Keys:         &modelkeys.Service{},
		Store:        records.NewShared(memstore.New(), users.User{ID: users.LegacyID}, 1),
		HomeProfiles: func(context.Context) ([]modelprofiles.Profile, error) { return []modelprofiles.Profile{profile}, nil },
	}
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/responses" {
			_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"text\":\"OK\",\"actions\":[]}"},"finish_reason":"stop"}]}`))
	}))
	defer provider.Close()
	cloud := modelprofiles.Profile{Backend: "openai", Model: "gpt-6.2-sol"}
	models := settings.Models{TestURL: provider.URL, CloudDispatchClient: llmguard.NewCheckedClient(account.CheckCloudDispatch)}
	chat := platform.ReplyModelForBYOK(models, "user", cloud, "low", "fixture-key")
	work := platform.TaskModelForBYOK(models, usage.Func(func(context.Context, state.Usage) error { return nil }), "user", cloud, "low", "fixture-key")
	chatRequest := conversation.ReplyRequest{Messages: []json.RawMessage{json.RawMessage(`{"role":"user","content":"Hi"}`)}}
	workRequest := agent.Request{Messages: []transcript.Message{transcript.UserText("Hi")}}
	enabled := true
	view, err := account.Save(ctx, state.ModelSelection{ChatProfileID: profile.ID}, 0, &enabled)
	if err != nil {
		t.Fatal(err)
	}
	chatReply, err := chat.Respond(ctx, chatRequest)
	if !errors.Is(err, modelprofiles.ErrHomeOnly) || chatReply.Usage.RequestStatus != "not_sent" {
		t.Fatalf("previously resolved Chat was not blocked: %+v, %v", chatReply, err)
	}
	workReply, err := work.Respond(ctx, workRequest)
	if !errors.Is(err, modelprofiles.ErrHomeOnly) || !workReply.NotSent || requests.Load() != 0 {
		t.Fatalf("previously resolved Work was not blocked: %+v, %v, sent %d", workReply, err, requests.Load())
	}
	enabled = false
	if _, err := account.Save(ctx, view.Selection, view.Revision, &enabled); err != nil {
		t.Fatal(err)
	}
	if reply, err := chat.Respond(ctx, chatRequest); err != nil || reply.Text != "OK" || requests.Load() != 1 {
		t.Fatalf("cloud Chat did not resume: %+v, %v, sent %d", reply, err, requests.Load())
	}
}
