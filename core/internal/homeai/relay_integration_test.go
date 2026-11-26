package homeai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"nori/internal/homeaiwire"
	"nori/internal/state/postgres"
)

type accountDeletionBackend interface {
	BeginAccountDelete(context.Context, string) error
	FinishAccountDelete(context.Context, string, string) error
}

type relayFixture struct {
	ctx      context.Context
	service  *Service
	deletion accountDeletionBackend
	server   *httptest.Server
	userID   string
	deviceID string
	tokens   tokenResponse
	conn     *websocket.Conn
}

func newRelayFixture(t *testing.T) *relayFixture {
	t.Helper()
	raw := os.Getenv("NORI_TEST_POSTGRES_URL")
	if raw == "" {
		t.Skip("NORI_TEST_POSTGRES_URL is not set")
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		t.Fatal("relay tests require a valid Postgres URL")
	}
	database := strings.TrimPrefix(u.Path, "/")
	if !isLoopback(u.Hostname()) || database == "" || database == "nori" {
		t.Fatal("relay tests require an isolated loopback test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := "home_ai_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		_ = admin.Close(cleanupCtx)
	})
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	scoped := u.String()
	backend, err := postgres.OpenBackend(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	s, err := Open(ctx, Options{PostgresURL: scoped, Key: bytes.Repeat([]byte{42}, 32), VerificationURI: "http://127.0.0.1/connector/activate", MaxConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	userID := uuid.NewString()
	_, err = s.pool.Exec(ctx, `INSERT INTO nori_records(shard,collection,id,data) VALUES('users','users',$1,jsonb_build_object('id',$1::text,'email',$2::text))`, userID, userID+"@example.test")
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(s.Handler())
	t.Cleanup(host.Close)
	deletion, ok := backend.(accountDeletionBackend)
	if !ok {
		t.Fatal("Postgres backend does not support account deletion")
	}
	f := &relayFixture{ctx: ctx, service: s, deletion: deletion, server: host, userID: userID}
	grant := f.start(t, "Home test")
	if _, err = s.Approve(ctx, userID, grant.UserCode); err != nil {
		t.Fatal(err)
	}
	f.tokens = f.token(t, url.Values{"client_id": {"nori-connector"}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {grant.DeviceCode}}, http.StatusOK)
	devices, err := s.List(ctx, userID)
	if err != nil || len(devices) != 1 {
		t.Fatalf("devices=%+v err=%v", devices, err)
	}
	f.deviceID = devices[0].ID
	return f
}

func (f *relayFixture) start(t *testing.T, name string) deviceAuthorization {
	t.Helper()
	resp, err := http.PostForm(f.server.URL+"/oauth/device_authorization", url.Values{"client_id": {"nori-connector"}, "device_name": {name}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var grant deviceAuthorization
	if err = json.NewDecoder(resp.Body).Decode(&grant); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("grant status=%d err=%v", resp.StatusCode, err)
	}
	return grant
}

func (f *relayFixture) token(t *testing.T, form url.Values, want int) tokenResponse {
	t.Helper()
	resp, err := http.PostForm(f.server.URL+"/oauth/token", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("token status=%d body=%s", resp.StatusCode, body)
	}
	var token tokenResponse
	if err = json.Unmarshal(body, &token); err != nil {
		t.Fatal(err)
	}
	return token
}

func (f *relayFixture) connect(t *testing.T) {
	t.Helper()
	conn, _, err := websocket.Dial(f.ctx, f.server.URL+"/v1/connector/socket", &websocket.DialOptions{Subprotocols: []string{homeaiwire.Subprotocol}, HTTPHeader: http.Header{"Authorization": {"Bearer " + f.tokens.AccessToken}}})
	if err != nil {
		t.Fatal(err)
	}
	f.conn = conn
	t.Cleanup(func() { _ = conn.CloseNow() })
	f.send(t, homeaiwire.Message{Type: "hello", Version: "test", OllamaAvailable: true, Models: []homeaiwire.Model{{Name: "model:latest", Digest: "abc123", Capabilities: []string{"completion", "tools"}}}})
	for {
		devices, err := f.service.List(f.ctx, f.userID)
		if err != nil {
			t.Fatal(err)
		}
		if len(devices) == 1 && devices[0].Status == "online" && len(devices[0].Models) == 1 {
			return
		}
		select {
		case <-f.ctx.Done():
			t.Fatal("connector never became ready")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (f *relayFixture) send(t *testing.T, msg homeaiwire.Message) {
	t.Helper()
	if err := wsjson.Write(f.ctx, f.conn, msg); err != nil {
		t.Fatal(err)
	}
}
func (f *relayFixture) receive(t *testing.T) homeaiwire.Message {
	t.Helper()
	var msg homeaiwire.Message
	if err := wsjson.Read(f.ctx, f.conn, &msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

func TestRelayStreamsAndFencesRequests(t *testing.T) {
	f := newRelayFixture(t)
	f.connect(t)
	_, err := f.service.Infer(f.ctx, uuid.NewString(), f.deviceID, "model:latest", "abc123", "chat", []byte(`{"model":"model:latest"}`))
	if !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("cross-account request=%v", err)
	}
	_, err = f.service.Infer(f.ctx, f.userID, f.deviceID, "model:latest", "wrong", "chat", []byte(`{"model":"model:latest"}`))
	if !errors.Is(err, ErrModelChanged) {
		t.Fatalf("changed model=%v", err)
	}
	type result struct {
		response *http.Response
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		r, err := f.service.Infer(f.ctx, f.userID, f.deviceID, "model:latest", "abc123", "chat", []byte(`{"model":"model:latest"}`))
		resultCh <- result{r, err}
	}()
	request := f.receive(t)
	if request.Type != "infer" || request.Operation != "chat" || string(request.Payload) != `{"model":"model:latest"}` {
		t.Fatalf("request=%+v", request)
	}
	_, err = f.service.Infer(f.ctx, f.userID, f.deviceID, "model:latest", "abc123", "chat", []byte(`{"model":"model:latest"}`))
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent request=%v", err)
	}
	f.send(t, homeaiwire.Message{Type: "accepted", RequestID: request.RequestID, AttemptID: "other"})
	f.send(t, homeaiwire.Message{Type: "accepted", RequestID: request.RequestID, AttemptID: request.AttemptID})
	f.send(t, homeaiwire.Message{Type: "headers", RequestID: request.RequestID, AttemptID: request.AttemptID, Status: 200, ContentType: "text/event-stream"})
	f.send(t, homeaiwire.Message{Type: "chunk", RequestID: request.RequestID, AttemptID: request.AttemptID, Sequence: 1, Data: []byte("first ")})
	f.send(t, homeaiwire.Message{Type: "chunk", RequestID: request.RequestID, AttemptID: request.AttemptID, Sequence: 1, Data: []byte("duplicate")})
	f.send(t, homeaiwire.Message{Type: "chunk", RequestID: request.RequestID, AttemptID: request.AttemptID, Sequence: 2, Data: []byte("second")})
	f.send(t, homeaiwire.Message{Type: "complete", RequestID: request.RequestID, AttemptID: request.AttemptID})
	f.send(t, homeaiwire.Message{Type: "ready", RequestID: request.RequestID, AttemptID: request.AttemptID})
	res := <-resultCh
	if res.err != nil {
		t.Fatal(res.err)
	}
	defer res.response.Body.Close()
	data, err := io.ReadAll(res.response.Body)
	if err != nil || string(data) != "first second" {
		t.Fatalf("stream=%q err=%v", data, err)
	}
	var count int
	if err = f.service.pool.QueryRow(f.ctx, `SELECT count(*) FROM nori_home_ai_requests WHERE id=$1`, request.RequestID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("completed request retained: %d, %v", count, err)
	}
	go func() {
		r, err := f.service.Infer(f.ctx, f.userID, f.deviceID, "model:latest", "abc123", "chat", []byte(`{"model":"model:latest"}`))
		resultCh <- result{r, err}
	}()
	request = f.receive(t)
	if request.Type != "infer" {
		t.Fatalf("next message after completion=%+v", request)
	}
	f.send(t, homeaiwire.Message{Type: "accepted", RequestID: request.RequestID, AttemptID: request.AttemptID})
	f.send(t, homeaiwire.Message{Type: "headers", RequestID: request.RequestID, AttemptID: request.AttemptID, Status: 200, ContentType: "application/json"})
	f.send(t, homeaiwire.Message{Type: "chunk", RequestID: request.RequestID, AttemptID: request.AttemptID, Sequence: 1, Data: []byte("second response")})
	f.send(t, homeaiwire.Message{Type: "complete", RequestID: request.RequestID, AttemptID: request.AttemptID})
	f.send(t, homeaiwire.Message{Type: "ready", RequestID: request.RequestID, AttemptID: request.AttemptID})
	res = <-resultCh
	if res.err != nil {
		t.Fatal(res.err)
	}
	data, err = io.ReadAll(res.response.Body)
	_ = res.response.Body.Close()
	if err != nil || string(data) != "second response" {
		t.Fatalf("second stream=%q err=%v", data, err)
	}
}

func TestRelayCancellationAndRevocation(t *testing.T) {
	f := newRelayFixture(t)
	f.connect(t)
	ctx, cancel := context.WithCancel(f.ctx)
	resultCh := make(chan error, 1)
	go func() {
		_, err := f.service.Infer(ctx, f.userID, f.deviceID, "model:latest", "abc123", "chat", []byte(`{"model":"model:latest"}`))
		resultCh <- err
	}()
	request := f.receive(t)
	cancel()
	if err := <-resultCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	message := f.receive(t)
	if message.Type != "cancel" || message.RequestID != request.RequestID {
		t.Fatalf("message=%+v", message)
	}
	if err := f.service.Revoke(f.ctx, f.userID, f.deviceID); err != nil {
		t.Fatal(err)
	}
	var revoked bool
	var requests int
	if err := f.service.pool.QueryRow(f.ctx, `SELECT (SELECT revoked_at IS NOT NULL FROM nori_home_ai_devices WHERE id=$1),(SELECT count(*) FROM nori_home_ai_requests WHERE user_id=$2)`, f.deviceID, f.userID).Scan(&revoked, &requests); err != nil || !revoked || requests != 0 {
		t.Fatalf("revoked=%v retained requests=%d err=%v", revoked, requests, err)
	}
	_, _, err := websocket.Dial(f.ctx, f.server.URL+"/v1/connector/socket", &websocket.DialOptions{Subprotocols: []string{homeaiwire.Subprotocol}, HTTPHeader: http.Header{"Authorization": {"Bearer " + f.tokens.AccessToken}}})
	if err == nil {
		t.Fatal("revoked token connected")
	}
}

func TestRelayRefreshReplayRevokesDevice(t *testing.T) {
	f := newRelayFixture(t)
	first := f.tokens.RefreshToken
	rotated := f.token(t, url.Values{"client_id": {"nori-connector"}, "grant_type": {"refresh_token"}, "refresh_token": {first}}, http.StatusOK)
	if rotated.RefreshToken == first || rotated.AccessToken == f.tokens.AccessToken {
		t.Fatal("token rotation did not change both tokens")
	}
	f.token(t, url.Values{"client_id": {"nori-connector"}, "grant_type": {"refresh_token"}, "refresh_token": {first}}, http.StatusBadRequest)
	f.token(t, url.Values{"client_id": {"nori-connector"}, "grant_type": {"refresh_token"}, "refresh_token": {rotated.RefreshToken}}, http.StatusBadRequest)
	devices, err := f.service.List(f.ctx, f.userID)
	if err != nil || len(devices) != 0 {
		t.Fatalf("replayed refresh retained device %+v err=%v", devices, err)
	}
}

func TestRelayConcurrentPairingReservesOneDevice(t *testing.T) {
	f := newRelayFixture(t)
	userID := uuid.NewString()
	_, err := f.service.pool.Exec(f.ctx, `INSERT INTO nori_records(shard,collection,id,data) VALUES('users','users',$1,jsonb_build_object('id',$1::text,'email',$2::text))`, userID, userID+"@example.test")
	if err != nil {
		t.Fatal(err)
	}
	first := f.start(t, "One")
	second := f.start(t, "Two")
	start := make(chan struct{})
	out := make(chan error, 2)
	for _, code := range []string{first.UserCode, second.UserCode} {
		go func(code string) { <-start; _, err := f.service.Approve(f.ctx, userID, code); out <- err }(code)
	}
	close(start)
	var approved, limited int
	for range 2 {
		err := <-out
		if err == nil {
			approved++
		} else if errors.Is(err, ErrDeviceLimit) {
			limited++
		} else {
			t.Fatalf("approval failed: %v", err)
		}
	}
	if approved != 1 || limited != 1 {
		t.Fatalf("approved=%d limited=%d", approved, limited)
	}
	var count int
	if err := f.service.pool.QueryRow(f.ctx, `SELECT count(*) FROM nori_home_ai_grants WHERE user_id=$1 AND status='approved'`, userID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("approved grants=%d err=%v", count, err)
	}
}

func TestRelayRevocationInterruptsStream(t *testing.T) {
	f := newRelayFixture(t)
	f.connect(t)
	type result struct {
		response *http.Response
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		response, err := f.service.Infer(f.ctx, f.userID, f.deviceID, "model:latest", "abc123", "chat", []byte(`{"model":"model:latest"}`))
		resultCh <- result{response, err}
	}()
	request := f.receive(t)
	f.send(t, homeaiwire.Message{Type: "accepted", RequestID: request.RequestID, AttemptID: request.AttemptID})
	f.send(t, homeaiwire.Message{Type: "headers", RequestID: request.RequestID, AttemptID: request.AttemptID, Status: 200, ContentType: "text/event-stream"})
	f.send(t, homeaiwire.Message{Type: "chunk", RequestID: request.RequestID, AttemptID: request.AttemptID, Sequence: 1, Data: []byte("hello")})
	res := <-resultCh
	if res.err != nil {
		t.Fatal(res.err)
	}
	defer res.response.Body.Close()
	first := make([]byte, 5)
	if _, err := io.ReadFull(res.response.Body, first); err != nil || string(first) != "hello" {
		t.Fatalf("first=%q err=%v", first, err)
	}
	if err := f.service.Revoke(f.ctx, f.userID, f.deviceID); err != nil {
		t.Fatal(err)
	}
	_, err := io.ReadAll(res.response.Body)
	if !errors.Is(err, ErrRevoked) {
		t.Fatalf("stream after revoke: %v", err)
	}
}

func TestRelayAccountDeleteFencesAndPurges(t *testing.T) {
	f := newRelayFixture(t)
	f.connect(t)
	grant := f.start(t, "Pending connector")

	type result struct {
		response *http.Response
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		response, err := f.service.Infer(f.ctx, f.userID, f.deviceID, "model:latest", "abc123", "chat", []byte(`{"model":"model:latest"}`))
		resultCh <- result{response, err}
	}()
	request := f.receive(t)
	f.send(t, homeaiwire.Message{Type: "accepted", RequestID: request.RequestID, AttemptID: request.AttemptID})
	f.send(t, homeaiwire.Message{Type: "headers", RequestID: request.RequestID, AttemptID: request.AttemptID, Status: 200, ContentType: "text/event-stream"})
	f.send(t, homeaiwire.Message{Type: "chunk", RequestID: request.RequestID, AttemptID: request.AttemptID, Sequence: 1, Data: []byte("hello")})
	res := <-resultCh
	if res.err != nil {
		t.Fatal(res.err)
	}
	defer res.response.Body.Close()
	first := make([]byte, 5)
	if _, err := io.ReadFull(res.response.Body, first); err != nil || string(first) != "hello" {
		t.Fatalf("first=%q err=%v", first, err)
	}

	// Keep one completed request and chunk so their cascading deletion is checked.
	historyID, attemptID := uuid.NewString(), uuid.NewString()
	if _, err := f.service.pool.Exec(f.ctx, `INSERT INTO nori_home_ai_requests
		(id,attempt_id,user_id,device_id,generation,operation,model,digest,payload,status,deadline)
		VALUES($1,$2,$3,$4,1,'chat','model:latest','abc123',decode('00','hex'),'complete',clock_timestamp()+interval '1 hour')`, historyID, attemptID, f.userID, f.deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.pool.Exec(f.ctx, `INSERT INTO nori_home_ai_chunks(request_id,attempt_id,sequence,payload) VALUES($1,$2,1,decode('00','hex'))`, historyID, attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.pool.Exec(f.ctx, `INSERT INTO nori_home_ai_refresh_history(token_hash,device_id,expires_at) VALUES($1,$2,clock_timestamp()+interval '1 hour')`, tokenHash(uuid.NewString()), f.deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.pool.Exec(f.ctx, `INSERT INTO nori_home_ai_pairing_attempts(user_id,bucket,attempts) VALUES($1,date_trunc('minute',clock_timestamp()),1) ON CONFLICT(user_id,bucket) DO UPDATE SET attempts=1`, f.userID); err != nil {
		t.Fatal(err)
	}

	otherID := uuid.NewString()
	if _, err := f.service.pool.Exec(f.ctx, `INSERT INTO nori_records(shard,collection,id,data) VALUES('users','users',$1,jsonb_build_object('id',$1::text))`, otherID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.pool.Exec(f.ctx, `INSERT INTO nori_home_ai_pairing_attempts(user_id,bucket,attempts) VALUES($1,date_trunc('minute',clock_timestamp()),1)`, otherID); err != nil {
		t.Fatal(err)
	}

	listener, err := pgx.Connect(f.ctx, f.service.listenURL)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close(f.ctx)
	if _, err := listener.Exec(f.ctx, `LISTEN nori_home_ai_events`); err != nil {
		t.Fatal(err)
	}
	if err := f.deletion.BeginAccountDelete(f.ctx, f.userID); err != nil {
		t.Fatal(err)
	}
	notifyCtx, stopNotify := context.WithTimeout(f.ctx, 5*time.Second)
	defer stopNotify()
	waiting := map[string]bool{"d:" + f.deviceID: true, "r:" + request.RequestID: true}
	for len(waiting) != 0 {
		event, err := listener.WaitForNotification(notifyCtx)
		if err != nil {
			t.Fatalf("deletion notifications missing %v: %v", waiting, err)
		}
		delete(waiting, event.Payload)
	}
	if _, err := io.ReadAll(res.response.Body); !errors.Is(err, ErrRevoked) {
		t.Fatalf("stream after deletion begins: %v", err)
	}
	readCtx, stopRead := context.WithTimeout(f.ctx, 5*time.Second)
	defer stopRead()
	for {
		_, _, err := f.conn.Read(readCtx)
		if err != nil {
			if readCtx.Err() != nil {
				t.Fatalf("existing connector did not disconnect: %v", readCtx.Err())
			}
			break
		}
	}
	if _, err := f.service.Infer(f.ctx, f.userID, f.deviceID, "model:latest", "abc123", "chat", []byte(`{"model":"model:latest"}`)); !errors.Is(err, ErrRevoked) {
		t.Fatalf("inference during pending deletion: %v", err)
	}
	if _, err := f.service.Approve(f.ctx, f.userID, grant.UserCode); !errors.Is(err, ErrRevoked) {
		t.Fatalf("pairing during pending deletion: %v", err)
	}
	conn, resp, err := websocket.Dial(f.ctx, f.server.URL+"/v1/connector/socket", &websocket.DialOptions{Subprotocols: []string{homeaiwire.Subprotocol}, HTTPHeader: http.Header{"Authorization": {"Bearer " + f.tokens.AccessToken}}})
	if conn != nil {
		_ = conn.CloseNow()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("new socket during pending deletion: response=%v err=%v", resp, err)
	}

	if err := f.deletion.FinishAccountDelete(f.ctx, f.userID, f.userID); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name, query string
		arg         string
	}{
		{"user", `SELECT count(*) FROM nori_records WHERE shard='users' AND collection='users' AND id=$1`, f.userID},
		{"grants", `SELECT count(*) FROM nori_home_ai_grants WHERE user_id=$1`, f.userID},
		{"devices", `SELECT count(*) FROM nori_home_ai_devices WHERE user_id=$1`, f.userID},
		{"requests", `SELECT count(*) FROM nori_home_ai_requests WHERE user_id=$1`, f.userID},
		{"chunks", `SELECT count(*) FROM nori_home_ai_chunks WHERE request_id=$1`, historyID},
		{"refresh history", `SELECT count(*) FROM nori_home_ai_refresh_history WHERE device_id=$1`, f.deviceID},
		{"pairing attempts", `SELECT count(*) FROM nori_home_ai_pairing_attempts WHERE user_id=$1`, f.userID},
	}
	for _, check := range checks {
		var count int
		if err := f.service.pool.QueryRow(f.ctx, check.query, check.arg).Scan(&count); err != nil || count != 0 {
			t.Errorf("remaining %s=%d err=%v", check.name, count, err)
		}
	}
	var otherCount int
	if err := f.service.pool.QueryRow(f.ctx, `SELECT count(*) FROM nori_home_ai_pairing_attempts WHERE user_id=$1`, otherID).Scan(&otherCount); err != nil || otherCount != 1 {
		t.Fatalf("other account pairing attempts=%d err=%v", otherCount, err)
	}
}

func TestRelayAccountDeleteSerializesPairing(t *testing.T) {
	f := newRelayFixture(t)
	userID := uuid.NewString()
	if _, err := f.service.pool.Exec(f.ctx, `INSERT INTO nori_records(shard,collection,id,data) VALUES('users','users',$1,jsonb_build_object('id',$1::text))`, userID); err != nil {
		t.Fatal(err)
	}
	grant := f.start(t, "Concurrent connector")
	tx, err := f.service.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if _, err := tx.Exec(f.ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "nori-home-ai:"+userID); err != nil {
		t.Fatal(err)
	}
	approval := make(chan error, 1)
	go func() {
		_, err := f.service.Approve(f.ctx, userID, grant.UserCode)
		approval <- err
	}()
	waitForBlocked := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var blocked int
			err := f.service.pool.QueryRow(f.ctx, `SELECT count(*) FROM pg_stat_activity
				WHERE datname=current_database() AND wait_event_type='Lock' AND wait_event='advisory'
				AND query LIKE 'SELECT pg_advisory_xact_lock%'`).Scan(&blocked)
			if err != nil {
				t.Fatal(err)
			}
			if blocked >= want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("expected %d pairing/deletion transactions to wait for the account lock", want)
	}
	waitForBlocked(1)
	deletion := make(chan error, 1)
	go func() { deletion <- f.deletion.BeginAccountDelete(f.ctx, userID) }()
	waitForBlocked(2)
	var pending bool
	if err := f.service.pool.QueryRow(f.ctx, `SELECT COALESCE((data->'delete_pending')='true'::jsonb,false) FROM nori_records WHERE shard='users' AND collection='users' AND id=$1`, userID).Scan(&pending); err != nil || pending {
		t.Fatalf("deletion marked while pairing lock held: pending=%v err=%v", pending, err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-approval; err != nil && !errors.Is(err, ErrRevoked) {
		t.Fatalf("concurrent approval: %v", err)
	}
	if err := <-deletion; err != nil {
		t.Fatalf("concurrent deletion: %v", err)
	}
	if err := f.deletion.FinishAccountDelete(f.ctx, userID, userID); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := f.service.pool.QueryRow(f.ctx, `SELECT count(*) FROM nori_home_ai_grants WHERE user_id=$1`, userID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("account grants after concurrent deletion=%d err=%v", remaining, err)
	}
}

func TestRelayClassifiesOnlySafePreflightFailures(t *testing.T) {
	f := newRelayFixture(t)
	f.connect(t)
	cases := []struct {
		code string
		want error
	}{{"ollama_unavailable", ErrOllamaUnavailable}, {"busy", ErrBusy}, {"model_unavailable", ErrModelUnavailable}, {"model_changed", ErrModelChanged}, {"transport_error", ErrInterrupted}}
	for _, tc := range cases {
		result := make(chan error, 1)
		go func() {
			_, err := f.service.Infer(f.ctx, f.userID, f.deviceID, "model:latest", "abc123", "chat", []byte(`{"model":"model:latest"}`))
			result <- err
		}()
		request := f.receive(t)
		if request.Type != "infer" {
			t.Fatalf("message=%+v", request)
		}
		f.send(t, homeaiwire.Message{Type: "accepted", RequestID: request.RequestID, AttemptID: request.AttemptID})
		f.send(t, homeaiwire.Message{Type: "failed", RequestID: request.RequestID, AttemptID: request.AttemptID, Error: tc.code})
		f.send(t, homeaiwire.Message{Type: "ready", RequestID: request.RequestID, AttemptID: request.AttemptID})
		if got := <-result; !errors.Is(got, tc.want) {
			t.Fatalf("failure %s: got %v want %v", tc.code, got, tc.want)
		}
	}
}

func TestRelayExpiresAbandonedPendingBeforeAdmission(t *testing.T) {
	f := newRelayFixture(t)
	f.connect(t)
	var generation int64
	if err := f.service.pool.QueryRow(f.ctx, `SELECT generation FROM nori_home_ai_devices WHERE id=$1`, f.deviceID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	oldID := uuid.NewString()
	sealed, err := f.service.seal(oldID, []byte(`{"model":"model:latest"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.service.pool.Exec(f.ctx, `INSERT INTO nori_home_ai_requests(id,attempt_id,user_id,device_id,generation,operation,model,digest,payload,status,deadline) VALUES($1,$2,$3,$4,$5,'chat','model:latest','abc123',$6,'pending',clock_timestamp()-interval '1 minute')`, oldID, uuid.NewString(), f.userID, f.deviceID, generation, sealed)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		response *http.Response
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		response, err := f.service.Infer(f.ctx, f.userID, f.deviceID, "model:latest", "abc123", "chat", []byte(`{"model":"model:latest"}`))
		resultCh <- result{response, err}
	}()
	request := f.receive(t)
	if request.Type != "infer" || request.RequestID == oldID {
		t.Fatalf("dispatched=%+v", request)
	}
	var status string
	if err = f.service.pool.QueryRow(f.ctx, `SELECT status FROM nori_home_ai_requests WHERE id=$1`, oldID).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("abandoned status=%q err=%v", status, err)
	}
	f.send(t, homeaiwire.Message{Type: "accepted", RequestID: request.RequestID, AttemptID: request.AttemptID})
	f.send(t, homeaiwire.Message{Type: "headers", RequestID: request.RequestID, AttemptID: request.AttemptID, Status: 200, ContentType: "application/json"})
	f.send(t, homeaiwire.Message{Type: "chunk", RequestID: request.RequestID, AttemptID: request.AttemptID, Sequence: 1, Data: []byte("ok")})
	f.send(t, homeaiwire.Message{Type: "complete", RequestID: request.RequestID, AttemptID: request.AttemptID})
	f.send(t, homeaiwire.Message{Type: "ready", RequestID: request.RequestID, AttemptID: request.AttemptID})
	res := <-resultCh
	if res.err != nil {
		t.Fatal(res.err)
	}
	data, err := io.ReadAll(res.response.Body)
	_ = res.response.Body.Close()
	if err != nil || string(data) != "ok" {
		t.Fatalf("response=%q err=%v", data, err)
	}
}

func TestRelayFencesExpiredRunningBeforeNextDispatch(t *testing.T) {
	f := newRelayFixture(t)
	f.connect(t)
	type result struct {
		response *http.Response
		err      error
	}
	first := make(chan result, 1)
	go func() {
		response, err := f.service.Infer(f.ctx, f.userID, f.deviceID, "model:latest", "abc123", "chat", []byte(`{"model":"model:latest"}`))
		first <- result{response, err}
	}()
	old := f.receive(t)
	f.send(t, homeaiwire.Message{Type: "accepted", RequestID: old.RequestID, AttemptID: old.AttemptID})
	for {
		var status string
		if err := f.service.pool.QueryRow(f.ctx, `SELECT status FROM nori_home_ai_requests WHERE id=$1`, old.RequestID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "running" {
			break
		}
		select {
		case <-f.ctx.Done():
			t.Fatal("request never started")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := f.service.pool.Exec(f.ctx, `UPDATE nori_home_ai_requests SET deadline=clock_timestamp()-interval '1 second' WHERE id=$1`, old.RequestID); err != nil {
		t.Fatal(err)
	}
	second := make(chan result, 1)
	go func() {
		response, err := f.service.Infer(f.ctx, f.userID, f.deviceID, "model:latest", "abc123", "chat", []byte(`{"model":"model:latest"}`))
		second <- result{response, err}
	}()
	cancel := f.receive(t)
	if cancel.Type != "cancel" || cancel.RequestID != old.RequestID {
		t.Fatalf("first message=%+v", cancel)
	}
	f.send(t, homeaiwire.Message{Type: "failed", RequestID: old.RequestID, AttemptID: old.AttemptID, Error: "cancelled"})
	f.send(t, homeaiwire.Message{Type: "ready", RequestID: old.RequestID, AttemptID: old.AttemptID})
	request := f.receive(t)
	if request.Type != "infer" || request.RequestID == old.RequestID {
		t.Fatalf("next dispatch=%+v", request)
	}
	if got := (<-first).err; !errors.Is(got, ErrInterrupted) {
		t.Fatalf("expired running request=%v", got)
	}
	f.send(t, homeaiwire.Message{Type: "accepted", RequestID: request.RequestID, AttemptID: request.AttemptID})
	f.send(t, homeaiwire.Message{Type: "headers", RequestID: request.RequestID, AttemptID: request.AttemptID, Status: 200, ContentType: "application/json"})
	f.send(t, homeaiwire.Message{Type: "chunk", RequestID: request.RequestID, AttemptID: request.AttemptID, Sequence: 1, Data: []byte("ok")})
	f.send(t, homeaiwire.Message{Type: "complete", RequestID: request.RequestID, AttemptID: request.AttemptID})
	f.send(t, homeaiwire.Message{Type: "ready", RequestID: request.RequestID, AttemptID: request.AttemptID})
	res := <-second
	if res.err != nil {
		t.Fatal(res.err)
	}
	data, err := io.ReadAll(res.response.Body)
	_ = res.response.Body.Close()
	if err != nil || string(data) != "ok" {
		t.Fatalf("next response=%q err=%v", data, err)
	}
}

func TestRelayCleanupRunsWhenListenerIsUnavailable(t *testing.T) {
	f := newRelayFixture(t)
	var generation int64
	if err := f.service.pool.QueryRow(f.ctx, `SELECT generation FROM nori_home_ai_devices WHERE id=$1`, f.deviceID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	oldID := uuid.NewString()
	sealed, err := f.service.seal(oldID, []byte(`{"model":"model:latest"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.service.pool.Exec(f.ctx, `INSERT INTO nori_home_ai_requests(id,attempt_id,user_id,device_id,generation,operation,model,digest,payload,status,deadline) VALUES($1,$2,$3,$4,$5,'chat','model:latest','abc123',$6,'pending',clock_timestamp()-interval '1 minute')`, oldID, uuid.NewString(), f.userID, f.deviceID, generation, sealed)
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(f.ctx, Options{PostgresURL: f.service.pool.Config().ConnConfig.ConnString(), PostgresListenURL: "postgres://127.0.0.1:1/unavailable?connect_timeout=1", Key: bytes.Repeat([]byte{42}, 32), VerificationURI: "http://127.0.0.1/connector/activate", MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	loopCtx, stop := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { other.cleanupLoop(loopCtx, 25*time.Millisecond); close(done) }()
	defer func() { stop(); <-done }()
	for {
		var status string
		if err = other.pool.QueryRow(f.ctx, `SELECT status FROM nori_home_ai_requests WHERE id=$1`, oldID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "failed" {
			break
		}
		select {
		case <-f.ctx.Done():
			t.Fatal("cleanup did not run without LISTEN")
		case <-time.After(15 * time.Millisecond):
		}
	}
}

func TestRelayDrainsClaimedInferenceBeforeRefresh(t *testing.T) {
	f := newRelayFixture(t)
	f.connect(t)
	type result struct {
		response *http.Response
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		response, err := f.service.Infer(f.ctx, f.userID, f.deviceID, "model:latest", "abc123", "chat", []byte(`{"model":"model:latest"}`))
		resultCh <- result{response, err}
	}()
	for {
		var count int
		if err := f.service.pool.QueryRow(f.ctx, `SELECT count(*) FROM nori_home_ai_requests WHERE device_id=$1 AND status='dispatched'`, f.deviceID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		select {
		case <-f.ctx.Done():
			t.Fatal("request was not claimed")
		case <-time.After(5 * time.Millisecond):
		}
	}
	f.send(t, homeaiwire.Message{Type: "draining"})
	request := f.receive(t)
	if request.Type != "infer" {
		t.Fatalf("claimed request must precede drain acknowledgment: %+v", request)
	}
	ack := f.receive(t)
	if ack.Type != "drained" {
		t.Fatalf("drain acknowledgment=%+v", ack)
	}
	_, err := f.service.Infer(f.ctx, f.userID, f.deviceID, "model:latest", "abc123", "chat", []byte(`{"model":"model:latest"}`))
	if !errors.Is(err, ErrOffline) {
		t.Fatalf("admission during drain=%v", err)
	}
	f.send(t, homeaiwire.Message{Type: "accepted", RequestID: request.RequestID, AttemptID: request.AttemptID})
	f.send(t, homeaiwire.Message{Type: "headers", RequestID: request.RequestID, AttemptID: request.AttemptID, Status: 200, ContentType: "application/json"})
	f.send(t, homeaiwire.Message{Type: "chunk", RequestID: request.RequestID, AttemptID: request.AttemptID, Sequence: 1, Data: []byte("ok")})
	f.send(t, homeaiwire.Message{Type: "complete", RequestID: request.RequestID, AttemptID: request.AttemptID})
	f.send(t, homeaiwire.Message{Type: "ready", RequestID: request.RequestID, AttemptID: request.AttemptID})
	res := <-resultCh
	if res.err != nil {
		t.Fatal(res.err)
	}
	data, err := io.ReadAll(res.response.Body)
	_ = res.response.Body.Close()
	if err != nil || string(data) != "ok" {
		t.Fatalf("drained response=%q err=%v", data, err)
	}
}

func TestRelayRejectsAdmissionNearAccessExpiry(t *testing.T) {
	f := newRelayFixture(t)
	f.connect(t)
	if _, err := f.service.pool.Exec(f.ctx, `UPDATE nori_home_ai_devices SET access_expires_at=clock_timestamp()+interval '5 minutes' WHERE id=$1`, f.deviceID); err != nil {
		t.Fatal(err)
	}
	_, err := f.service.Infer(f.ctx, f.userID, f.deviceID, "model:latest", "abc123", "chat", []byte(`{"model":"model:latest"}`))
	if !errors.Is(err, ErrOffline) {
		t.Fatalf("admission near expiry=%v", err)
	}
}

func TestRelayDrainFencesPending(t *testing.T) {
	f := newRelayFixture(t)
	var id socketIdentity
	id.deviceID = f.deviceID
	id.userID = f.userID
	id.connectionID = uuid.NewString()
	if err := f.service.pool.QueryRow(f.ctx, `UPDATE nori_home_ai_devices SET connection_id=$2,ollama_available=true,last_seen=clock_timestamp() WHERE id=$1 RETURNING generation`, f.deviceID, id.connectionID).Scan(&id.generation); err != nil {
		t.Fatal(err)
	}
	requestID := uuid.NewString()
	sealed, err := f.service.seal(requestID, []byte(`{"model":"model:latest"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.service.pool.Exec(f.ctx, `INSERT INTO nori_home_ai_requests(id,attempt_id,user_id,device_id,generation,operation,model,digest,payload,status,deadline) VALUES($1,$2,$3,$4,$5,'chat','model:latest','abc123',$6,'pending',clock_timestamp()+interval '1 minute')`, requestID, uuid.NewString(), f.userID, f.deviceID, id.generation, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.beginDrain(f.ctx, id); err != nil {
		t.Fatal(err)
	}
	var status, code string
	var size int
	if err = f.service.pool.QueryRow(f.ctx, `SELECT status,error_code,octet_length(payload) FROM nori_home_ai_requests WHERE id=$1`, requestID).Scan(&status, &code, &size); err != nil || status != "failed" || code != "offline" || size != 0 {
		t.Fatalf("status=%s code=%s payload size=%d err=%v", status, code, size, err)
	}
}

func TestRelayReadinessRequiresDrainMigration(t *testing.T) {
	f := newRelayFixture(t)
	if _, err := f.service.pool.Exec(f.ctx, `ALTER TABLE nori_home_ai_devices DROP COLUMN draining`); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Ready(f.ctx); err == nil {
		t.Fatal("ready without drain migration")
	}
}

func TestPairingCancellationRecoversFromLostTokenResponse(t *testing.T) {
	f := newRelayFixture(t)
	if err := f.service.Revoke(f.ctx, f.userID, f.deviceID); err != nil {
		t.Fatal(err)
	}
	cancel := func(code string) {
		t.Helper()
		res, err := http.PostForm(f.server.URL+"/oauth/revoke", url.Values{
			"client_id":       {"nori-connector"},
			"token":           {code},
			"token_type_hint": {"device_code"},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("cancel pairing HTTP %d", res.StatusCode)
		}
	}
	authorize := func(name string) deviceAuthorization {
		t.Helper()
		grant := f.start(t, name)
		if _, err := f.service.Approve(f.ctx, f.userID, grant.UserCode); err != nil {
			t.Fatal(err)
		}
		return grant
	}

	abandoned := authorize("Cancelled before redemption")
	cancel(abandoned.DeviceCode)
	if _, code := f.service.redeemGrant(f.ctx, abandoned.DeviceCode); code != "access_denied" {
		t.Fatalf("cancelled grant redeemed: %q", code)
	}

	lost := authorize("Lost redemption response")
	if _, code := f.service.redeemGrant(f.ctx, lost.DeviceCode); code != "" {
		t.Fatalf("first redemption: %q", code)
	}
	if _, err := f.service.pool.Exec(f.ctx, `DELETE FROM nori_home_ai_grants WHERE device_code_hash=$1`, tokenHash(lost.DeviceCode)); err != nil {
		t.Fatal(err)
	}
	cancel(lost.DeviceCode)
	cancel(lost.DeviceCode)
	if devices, err := f.service.List(f.ctx, f.userID); err != nil || len(devices) != 0 {
		t.Fatalf("cancelled device remains registered: %v, %v", devices, err)
	}
	replacement := authorize("Replacement after cancellation")
	if _, code := f.service.redeemGrant(f.ctx, replacement.DeviceCode); code != "" {
		t.Fatalf("replacement redemption: %q", code)
	}
}
