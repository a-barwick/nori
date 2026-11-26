package app_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"nori/internal/agent"
	"nori/internal/app"
	"nori/internal/homeai"
	"nori/internal/homeaiwire"
	"nori/internal/modelprofiles"
	"nori/internal/settings"
	"nori/internal/state/records"
	"nori/internal/transcript"
	"nori/internal/usage"
)

func TestHomeConnectorRoutesChatAcrossReplicas(t *testing.T) {
	dsn := os.Getenv("NORI_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("NORI_TEST_POSTGRES_URL is unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	key := bytes.Repeat([]byte{42}, 32)
	verification := "https://example.test/connector/activate"
	cfg := settings.Settings{
		Cloud: "local", Role: "all", TaskBudgetUSD: 1, Features: settings.Features{DisableJev: true},
		Models:      settings.Models{BYOKRequired: true},
		Connections: settings.Connections{CredentialStore: "database", CredentialKey: strings.Repeat("42", 32)},
		HomeAI:      settings.HomeAI{Key: base64.StdEncoding.EncodeToString(key), VerificationURL: verification, WorkEnabled: true},
		Metering:    usage.Config{MaxCallsPerHour: 3, MaxUSDPerDay: .02},
		Store:       settings.Store{Backend: "postgres", PostgresURL: dsn, LocalArtifactDir: t.TempDir()},
	}
	svc, err := app.OpenService(ctx, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	alice := createUser(t, svc, uuid.NewString(), "connector-"+uuid.NewString()+"@example.test")
	bob := createUser(t, svc, uuid.NewString(), "connector-"+uuid.NewString()+"@example.test")
	relay, err := homeai.Open(ctx, homeai.Options{PostgresURL: dsn, Key: key, VerificationURI: verification})
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	host := httptest.NewServer(relay.Handler())
	defer host.Close()

	core := svc.Handler(true)
	request := func(user, method, path string, body any, expected int) []byte {
		t.Helper()
		var data []byte
		if body != nil {
			data, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequestWithContext(ctx, method, "/users/"+user+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		core.ServeHTTP(rec, req)
		if rec.Code != expected {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, rec.Code, expected, rec.Body)
		}
		return rec.Body.Bytes()
	}
	const settingsPath = "/v1/settings/home-connectors"
	oauth := oauth2.Config{
		ClientID: "nori-connector",
		Endpoint: oauth2.Endpoint{
			DeviceAuthURL: host.URL + "/oauth/device_authorization",
			TokenURL:      host.URL + "/oauth/token", AuthStyle: oauth2.AuthStyleInParams,
		},
	}
	device, err := oauth.DeviceAuth(ctx, oauth2.SetAuthURLParam("device_name", "Home test"))
	if err != nil {
		t.Fatal(err)
	}
	code := map[string]string{"user_code": device.UserCode}
	request(alice.ID, "POST", settingsPath+"/pairing/lookup", code, 200)
	request(alice.ID, "POST", settingsPath+"/pairing/approve", code, 204)
	token, err := oauth.DeviceAccessToken(ctx, device)
	if err != nil {
		t.Fatal(err)
	}
	socket, _, err := websocket.Dial(ctx, host.URL+"/v1/connector/socket", &websocket.DialOptions{
		Subprotocols: []string{homeaiwire.Subprotocol},
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer " + token.AccessToken}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	if err := wsjson.Write(ctx, socket, homeaiwire.Message{
		Type: "hello", Version: "test", OllamaAvailable: true,
		Models: []homeaiwire.Model{{Name: "fixture:latest", Digest: strings.Repeat("a", 64), Capabilities: []string{"completion", "tools"}}},
	}); err != nil {
		t.Fatal(err)
	}

	var view modelprofiles.AccountView
	for {
		data := request(alice.ID, "GET", "/v1/settings/models", nil, 200)
		if err := json.Unmarshal(data, &view); err != nil {
			t.Fatal(err)
		}
		if len(view.ModelProfiles.Profiles) > 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("connector never published its model")
		case <-time.After(25 * time.Millisecond):
		}
	}
	profile := view.ModelProfiles.Profiles[0]
	if profile.Backend != modelprofiles.BackendHome || profile.HomeDeviceID == "" {
		t.Fatalf("unexpected profile: %+v", profile)
	}
	request(bob.ID, "PUT", "/v1/settings/models", map[string]any{
		"selection": map[string]any{"chat_profile_id": profile.ID}, "expected_revision": 0,
	}, 400)
	request(alice.ID, "PUT", "/v1/settings/models", map[string]any{
		"selection": map[string]any{"chat_profile_id": profile.ID, "task_profile_id": profile.ID, "home_only": true}, "expected_revision": view.Revision,
	}, 200)

	owner, release, err := svc.Acquire(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	const answer = "Hello from the home server."
	connectorResult := make(chan error, 1)
	go func() { connectorResult <- answerHomeChat(ctx, socket, answer) }()
	id := uuid.NewString()
	request(alice.ID, "POST", "/api/turn", map[string]string{"id": id, "message": "Hello home model."}, 202)
	if err := owner.Runner.Process(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-connectorResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("the home model did not receive the chat")
	}
	data := request(alice.ID, "GET", "/v1/conversations/assistant", nil, 200)
	if !bytes.Contains(data, []byte(answer)) {
		t.Fatalf("home reply missing: %s", data)
	}
	if err := socket.CloseNow(); err != nil {
		t.Fatal(err)
	}
	for {
		devices, err := relay.List(ctx, alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(devices) == 1 && devices[0].Status == "offline" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("relay did not mark the disconnected device offline")
		case <-time.After(25 * time.Millisecond):
		}
	}
	stats := records.NewShared(svc.Backend(), alice, 1)
	now := time.Now()
	before, _, _, err := stats.UsageWindowSummary(ctx, now.Add(-time.Hour), now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	work := owner.TaskActivities.AccountModel(ctx, nil)
	for range 6 {
		reply, err := work.Respond(ctx, agent.Request{Messages: []transcript.Message{transcript.UserText("Wait for the home server.")}})
		if !errors.Is(err, homeai.ErrOffline) || !reply.NotSent {
			t.Fatalf("unexpected offline Work outcome: %+v %v", reply, err)
		}
	}
	request(alice.ID, "POST", "/api/turn", map[string]string{"id": uuid.NewString(), "message": "The home server is offline."}, 202)
	if err := owner.Runner.Process(ctx); err != nil {
		t.Fatal(err)
	}
	data = request(alice.ID, "GET", "/v1/conversations/assistant", nil, 200)
	if !bytes.Contains(data, []byte("home_offline")) {
		t.Fatalf("offline Chat did not report the connection failure: %s", data)
	}
	after, spent, unknown, err := stats.UsageWindowSummary(ctx, now.Add(-time.Hour), now.Add(-24*time.Hour))
	if err != nil || after != before || spent != 0 || unknown != 0 {
		t.Fatalf("home preflights changed usage: before=%d after=%d spent=%f unknown=%d err=%v", before, after, spent, unknown, err)
	}

	request(alice.ID, "DELETE", settingsPath+"/"+profile.HomeDeviceID, nil, 204)
	updated := request(alice.ID, "GET", "/v1/settings/models", nil, 200)
	view = modelprofiles.AccountView{}
	if err := json.Unmarshal(updated, &view); err != nil {
		t.Fatal(err)
	}
	if !view.Selection.HomeOnly || view.Selection.ChatProfileID != profile.ID || len(view.ModelProfiles.Profiles) != 0 {
		t.Fatalf("revocation changed the selection: %+v", view)
	}

	selected, err := owner.Assistant.Models.CurrentSelection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _, _, err := owner.Assistant.Models.Resolve(ctx, modelprofiles.LaneChat, selected)
	if err != nil || resolved.HomeDeviceID != profile.HomeDeviceID {
		t.Fatalf("revoked selection routed elsewhere: %+v %v", resolved, err)
	}
	if err := verifyHomeResetWithoutFeature(ctx, t, cfg, oauth, host.URL, bob.ID, request); err != nil {
		t.Fatal(err)
	}
}

func verifyHomeResetWithoutFeature(
	ctx context.Context, t *testing.T, cfg settings.Settings, oauth oauth2.Config, relayURL string, userID string,
	request func(string, string, string, any, int) []byte,
) error {
	t.Helper()
	device, err := oauth.DeviceAuth(ctx)
	if err != nil {
		return err
	}
	request(userID, "POST", "/v1/settings/home-connectors/pairing/approve", map[string]string{"user_code": device.UserCode}, 204)
	token, err := oauth.DeviceAccessToken(ctx, device)
	if err != nil {
		return err
	}
	options := &websocket.DialOptions{
		Subprotocols: []string{homeaiwire.Subprotocol},
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer " + token.AccessToken}},
	}
	socket, _, err := websocket.Dial(ctx, relayURL+"/v1/connector/socket", options)
	if err != nil {
		return err
	}
	defer socket.CloseNow()
	cfg.HomeAI = settings.HomeAI{}
	disabled, err := app.OpenService(ctx, nil, cfg)
	if err != nil {
		return err
	}
	defer disabled.Close()
	reset := httptest.NewRequestWithContext(ctx, "POST", "/users/"+userID+"/v1/settings/reset", strings.NewReader(`{"confirmation":"RESET"}`))
	reset.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	disabled.Handler(true).ServeHTTP(rec, reset)
	if rec.Code != http.StatusNoContent {
		return fmt.Errorf("reset with connector feature disabled: HTTP %d: %s", rec.Code, rec.Body)
	}
	newSocket, response, err := websocket.Dial(ctx, relayURL+"/v1/connector/socket", options)
	if newSocket != nil {
		newSocket.CloseNow()
	}
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("old connector credential survived reset: %v", err)
	}
	return nil
}

func answerHomeChat(ctx context.Context, conn *websocket.Conn, answer string) error {
	var req homeaiwire.Message
	if err := wsjson.Read(ctx, conn, &req); err != nil {
		return err
	}
	if req.Type != "infer" || req.Operation != "chat" {
		return fmt.Errorf("unexpected relay message %q / %q", req.Type, req.Operation)
	}
	message := homeaiwire.Message{Type: "accepted", RequestID: req.RequestID, AttemptID: req.AttemptID}
	if err := wsjson.Write(ctx, conn, message); err != nil {
		return err
	}
	if err := wsjson.Write(ctx, conn, homeaiwire.Message{
		Type: "headers", RequestID: req.RequestID, AttemptID: req.AttemptID, Status: 200, ContentType: "text/event-stream",
	}); err != nil {
		return err
	}
	reply, err := json.Marshal(map[string]any{"text": answer, "actions": []any{}})
	if err != nil {
		return err
	}
	content, err := json.Marshal(map[string]any{"choices": []any{map[string]any{
		"index": 0, "delta": map[string]any{"role": "assistant", "content": string(reply)}, "finish_reason": "stop",
	}}})
	if err != nil {
		return err
	}
	message = homeaiwire.Message{Type: "chunk", RequestID: req.RequestID, AttemptID: req.AttemptID, Sequence: 1, Data: []byte("data: " + string(content) + "\n\ndata: [DONE]\n\n")}
	if err := wsjson.Write(ctx, conn, message); err != nil {
		return err
	}
	if err := wsjson.Write(ctx, conn, homeaiwire.Message{Type: "complete", RequestID: req.RequestID, AttemptID: req.AttemptID, Sequence: 2}); err != nil {
		return err
	}
	return wsjson.Write(ctx, conn, homeaiwire.Message{Type: "ready", RequestID: req.RequestID, AttemptID: req.AttemptID})
}
