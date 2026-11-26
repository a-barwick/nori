package sim_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"nori/internal/api"
	"nori/internal/connections"
	"nori/internal/extensions"
	"nori/internal/permissions"
	"nori/internal/serviceauth"
	"nori/internal/sim/env"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/users"
)

const (
	googleFixtureDrive = "https://www.googleapis.com/auth/drive.file"
	googleFixtureMail  = "https://www.googleapis.com/auth/gmail.send"
)

type googleFixtureIntegration struct{}

func (googleFixtureIntegration) Info() connections.Info {
	return connections.Info{ID: "google", Title: "Google", Auth: "oauth", BaseScopes: []string{"openid", "email"}, Capabilities: []connections.Capability{
		{ID: "files.read", Effect: "read", Supported: true, Scopes: []string{googleFixtureDrive}},
		{ID: "mail.send", Effect: "sensitive_send", Supported: true, Scopes: []string{googleFixtureMail}},
	}}
}

func (googleFixtureIntegration) OAuth(callback string) *oauth2.Config {
	return &oauth2.Config{ClientID: "fixture-client", ClientSecret: "fixture-secret", RedirectURL: callback,
		Endpoint: oauth2.Endpoint{AuthURL: "https://google.example/authorize", TokenURL: "https://google.example/token"}}
}

func (googleFixtureIntegration) Identify(context.Context, *http.Client) (connections.Account, error) {
	return connections.Account{ProviderID: "fixture-google-user", Label: "google-user@example.test"}, nil
}

func (googleFixtureIntegration) Revoke(context.Context, *oauth2.Token) error { return nil }
func (googleFixtureIntegration) Definitions() []extensions.Definition        { return nil }

func (googleFixtureIntegration) Resources(context.Context, *http.Client) ([]connections.Resource, error) {
	return nil, errors.New("calendar resource lookup was called without calendar access")
}

func (googleFixtureIntegration) DriveResources(_ context.Context, _ *http.Client) ([]connections.Resource, error) {
	return []connections.Resource{{Title: "Trip plan", Kind: "drive_file", Selected: true, Ref: connections.ResourceRef{ProviderID: "trip_plan"}}}, nil
}

func (googleFixtureIntegration) DriveAccount(_ context.Context, client *http.Client) (string, error) {
	res, err := client.Get("https://google.example/drive/about")
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	return string(body), err
}

type googleFixtureTransport struct{ mail bool }

func (f googleFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body := ""
	contentType := "application/json"
	switch req.URL.Path {
	case "/token":
		if err := req.ParseForm(); err != nil || req.Form.Get("code_verifier") == "" {
			return nil, errors.New("OAuth exchange did not use PKCE")
		}
		drive := googleFixtureDrive
		switch req.Form.Get("code") {
		case "connect":
			scopes := "openid https://www.googleapis.com/auth/userinfo.email " + drive
			if f.mail {
				scopes += " " + googleFixtureMail
			}
			body = fmt.Sprintf(`{"access_token":"service-token","refresh_token":"service-refresh","token_type":"bearer","scope":%q}`, scopes)
		case "too-broad":
			body = `{"access_token":"broad-token","refresh_token":"broad-refresh","token_type":"bearer","scope":"openid email https://www.googleapis.com/auth/drive.readonly"}`
		case "picked", "wrong-picker-account":
			body = fmt.Sprintf(`{"access_token":%q,"token_type":"bearer","scope":%q}`, req.Form.Get("code"), drive)
		default:
			return nil, errors.New("unknown OAuth fixture code")
		}
	case "/drive/about":
		contentType = "text/plain"
		body = "google-user@example.test"
		if req.Header.Get("Authorization") == "Bearer wrong-picker-account" {
			body = "someone-else@example.test"
		}
	default:
		return nil, errors.New("unexpected Google fixture endpoint")
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestGoogleWorkspaceConnectionAndPicker(t *testing.T) {
	ctx := context.Background()
	clock := env.NewClock(env.DefaultBaseTime)
	backend := memstore.New()
	ids := env.NewIDGen(rand.New(rand.NewPCG(7, 93)))
	user, db, creds := newSimMCPUserStore(t, backend, clock, ids, "tester@example.test")
	other, otherDB, otherCreds := newSimMCPUserStore(t, backend, clock, ids, "other@example.test")
	const callback = "https://nori.example.test/api/v1/connections/oauth/callback"
	const key = "fixture-state-signing-key"
	svc := connections.New(db, creds, "", googleFixtureIntegration{})
	svc.GoogleWorkspace = &connections.GoogleWorkspace{UserID: user, RedirectURL: callback, StateKey: key}
	svc.Now, svc.Transport = clock.Now, googleFixtureTransport{mail: true}
	otherSvc := connections.New(otherDB, otherCreds, "", googleFixtureIntegration{})
	otherSvc.Now, otherSvc.GoogleWorkspace = clock.Now, &connections.GoogleWorkspace{UserID: other, RedirectURL: callback, StateKey: key}
	directory := users.NewMemoryDirectory()
	if err := directory.Create(ctx, users.User{ID: user, Email: "tester@example.test", DisplayName: "Beta tester", TimeZone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	assistant := api.Handler(api.Deps{Assistant: &api.Assistant{Connections: svc}})
	userRoutes := api.UserHandlerWithOptions(directory, func(_ context.Context, row users.User) (api.UserRoutes, func(), error) {
		if row.ID != user {
			return api.UserRoutes{}, nil, errors.New("unexpected connection owner")
		}
		return api.UserRoutes{Handler: assistant}, func() {}, nil
	}, false, "test", api.UserHandlerOptions{DisableRateLimit: true, GoogleOAuthUser: func(raw string) string {
		return connections.GoogleWorkspaceCallbackUser(raw, key, clock.Now())
	}})
	auth := serviceauth.Config{Key: "fixture-service-token-key-at-least-32-bytes", Audience: "nori"}
	handler := auth.Protect(userRoutes)
	serviceToken, err := auth.Token()
	if err != nil {
		t.Fatal(err)
	}
	create := func(id string) connections.Projection {
		t.Helper()
		receipt, err := svc.Create(ctx, id, connections.Editable{IntegrationID: "google"})
		var projection connections.Projection
		if err != nil || json.Unmarshal(receipt.Resource, &projection) != nil {
			t.Fatalf("create Google connection: %v", err)
		}
		return projection
	}
	connection := create("create_1")
	if second := create("create_2"); second.ID != connection.ID {
		t.Fatal("creating Google again made a second connection")
	}
	parseAttempt := func(raw string) *url.URL {
		t.Helper()
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		state := u.Query().Get("state")
		if connections.GoogleWorkspaceCallbackUser(state, key, clock.Now()) != user ||
			connections.GoogleWorkspaceCallbackUser(state+"x", key, clock.Now()) != "" ||
			connections.GoogleWorkspaceCallbackUser(state, key, clock.Now().Add(16*time.Minute)) != "" {
			t.Fatal("Google OAuth state was not bound to the user, signature, and expiry")
		}
		return u
	}
	start := func(request string) string {
		t.Helper()
		projected, err := svc.Get(ctx, connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := svc.Act(ctx, connection.ID, request, projected.Revision, connections.Action{
			Action: "connect", Capabilities: []string{"files.read", "mail.send"}, ReturnTo: connections.IOSOAuthCallbackURL,
		})
		var attempt connections.AuthAttempt
		if err != nil || json.Unmarshal(receipt.Auth, &attempt) != nil {
			t.Fatalf("start Google connection: %v", err)
		}
		return parseAttempt(attempt.AuthorizeURL).Query().Get("state")
	}
	oauthState := start("connect_1")
	if _, err := otherSvc.Callback(ctx, oauthState, "connect", ""); !errors.Is(err, connections.ErrAttemptInvalid) {
		t.Fatalf("a different user redeemed the callback: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/oauth/google-workspace/callback?"+url.Values{"state": {oauthState}, "code": {"connect"}}.Encode(), nil)
	request.Header.Set("Authorization", "Bearer "+serviceToken)
	request.Header.Set("X-Nori-Redirect", "json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var reply struct{ Location string }
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &reply) != nil ||
		!strings.HasPrefix(reply.Location, "nori://oauth/callback?") || !strings.Contains(reply.Location, "result=connected") {
		t.Fatalf("finish Google connection through the browser bridge: %d %s", response.Code, response.Body.String())
	}
	resources, err := svc.Resources(ctx, connection.ID)
	if err != nil || len(resources) != 1 || resources[0].Title != "Trip plan" || !resources[0].Selected {
		t.Fatalf("selected Drive resources: %+v, %v", resources, err)
	}
	location, err := svc.Callback(ctx, start("connect_2"), "too-broad", "")
	if err != nil || !strings.Contains(location, "result=failed") {
		t.Fatalf("accepted broader Drive access: %q %v", location, err)
	}
	if _, err := svc.StartGoogleDrivePicker(ctx, connection.ID, "https://other.example.test"); !errors.Is(err, state.ErrInvalidResource) {
		t.Fatalf("picker accepted external return address: %v", err)
	}
	for _, result := range []struct{ code, status string }{{"wrong-picker-account", "failed"}, {"picked", "drive_files_selected"}} {
		attempt, err := svc.StartGoogleDrivePicker(ctx, connection.ID, "/apps")
		if err != nil {
			t.Fatal(err)
		}
		query := parseAttempt(attempt.AuthorizeURL).Query()
		if query.Get("scope") != googleFixtureDrive || query.Get("trigger_onepick") != "true" || query.Get("code_challenge") == "" {
			t.Fatalf("picker OAuth returned unexpected parameters: %v", query)
		}
		location, err = svc.CallbackWithSelection(ctx, query.Get("state"), result.code, "", "trip_plan")
		if err != nil || !strings.Contains(location, "result="+result.status) {
			t.Fatalf("picker callback: %q %v", location, err)
		}
	}
	grant, err := svc.Grant(ctx, connection.ID)
	if err != nil || grant.Token.AccessToken != "service-token" || grant.Token.RefreshToken != "service-refresh" {
		t.Fatalf("the picker or rejected grant replaced the service credential: %v", err)
	}
	if _, err := svc.Act(ctx, connection.ID, "disconnect_google", grant.Revision, connections.Action{Action: "disconnect"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartGoogleDrivePicker(ctx, connection.ID, "/apps"); !errors.Is(err, connections.ErrNotConnected) {
		t.Fatalf("the disconnected account could open a picker: %v", err)
	}
	connection = create("create_after_disconnect")
	if connection.Account != nil || len(connection.GrantedScopes) != 0 || connection.Status != "disconnected" {
		t.Fatalf("reopening the account row preserved a previous grant: %+v", connection)
	}
}

func TestGoogleSendApprovalIsPerOperation(t *testing.T) {
	ctx := context.Background()
	h := newPermSimHarness(t, 41, "allow", "allow")
	request := permissions.Request{RunID: "gmail_task", Revision: 1, Origin: "user", ToolID: "google/mail/mail-send", Effect: "sensitive_send",
		ConnectionID: "conn_google_workspace", Digest: "act_email", OperationID: "gmail_op1", RequireExplicitApproval: true}
	if decision, err := h.svc.Check(ctx, request); err != nil || decision.Result != "needs_interaction" {
		t.Fatalf("sending was allowed without interactive approval: %+v %v", decision, err)
	}
	card, err := h.svc.Open(ctx, permissions.Interaction{ID: "gmail_approval", TaskID: request.RunID, InstructionRevision: &request.Revision,
		Kind: "approval", Prompt: "Send email", Payload: json.RawMessage(`{"summary":"Exact email","approval_once":true}`), ActionDigest: request.Digest}, "")
	if err != nil {
		t.Fatal(err)
	}
	response := permissions.Response{Kind: "approve", ActionDigest: request.Digest, RememberRule: true}
	if _, _, err := h.svc.Respond(ctx, card.ID, h.nextReqID(), card.Revision, response); !errors.Is(err, state.ErrInvalidResource) {
		t.Fatalf("the one-time approval accepted a standing rule: %v", err)
	}
	response.RememberRule = false
	if _, _, err := h.svc.Respond(ctx, card.ID, h.nextReqID(), card.Revision, response); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if decision, err := h.svc.Check(ctx, request); err != nil || decision.Result != "allow" || !decision.ApprovedByHuman {
			t.Fatalf("the approved operation could not proceed or retry: %+v %v", decision, err)
		}
	}
	request.OperationID = "gmail_op2"
	if decision, err := h.svc.Check(ctx, request); err != nil || decision.Result != "needs_interaction" {
		t.Fatalf("a second operation reused the send approval: %+v %v", decision, err)
	}
	card.ID, card.Status, card.Revision = "gmail_approval_again", "", 0
	card, err = h.svc.Open(ctx, card, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.Respond(ctx, card.ID, h.nextReqID(), card.Revision, response); err != nil {
		t.Fatal(err)
	}
	if decision, err := h.svc.Check(ctx, request); err != nil || decision.Result != "allow" || !decision.ApprovedByHuman {
		t.Fatalf("the separately approved send could not proceed: %+v %v", decision, err)
	}
}
