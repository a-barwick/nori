package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"nori/internal/billing"
	"nori/internal/computer"
	"nori/internal/computer/fake"
	"nori/internal/settings"
)

func TestFilterRFBClientMessage(t *testing.T) {
	// FramebufferUpdateRequest (type 3, 10 bytes)
	fbReq := make([]byte, 10)
	fbReq[0] = 3
	// KeyEvent (type 4, 8 bytes)
	keyEv := make([]byte, 8)
	keyEv[0] = 4
	// PointerEvent (type 5, 6 bytes)
	ptrEv := make([]byte, 6)
	ptrEv[0] = 5
	// ClientCutText (type 6, 8 + 4 bytes)
	cutEv := make([]byte, 12)
	cutEv[0] = 6
	binary.BigEndian.PutUint32(cutEv[4:8], 4)
	copy(cutEv[8:], "clip")

	// Combined packet: fbReq + keyEv + ptrEv + cutEv
	combined := append(append(append(append([]byte{}, fbReq...), keyEv...), ptrEv...), cutEv...)

	// In view mode: only fbReq survives; KeyEvent, PointerEvent, ClientCutText are dropped.
	out, dropped := FilterRFBClientMessage(computer.SessionModeView, combined)
	if !dropped {
		t.Fatalf("expected dropped=true in view mode")
	}
	if !bytes.Equal(out, fbReq) {
		t.Fatalf("filtered bytes len=%d, want only fbReq len=%d", len(out), len(fbReq))
	}

	// In control mode: everything passes through untouched.
	outCtrl, droppedCtrl := FilterRFBClientMessage(computer.SessionModeControl, combined)
	if droppedCtrl || !bytes.Equal(outCtrl, combined) {
		t.Fatalf("expected control mode to forward all frames untouched")
	}
}

func TestComputerAPIAndDesktopWebSocketProxy(t *testing.T) {
	// Spin up a mock upstream noVNC/websockify server that records received client frames.
	receivedCh := make(chan []byte, 16)
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-internal-token" {
			http.Error(w, "missing internal auth header", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			receivedCh <- data
		}
	}))
	defer upstreamSrv.Close()

	fp := fake.New()
	fp.DesktopWSURL = "ws" + strings.TrimPrefix(upstreamSrv.URL, "http") + "/websockify"
	mgr := computer.NewManager(fp, computer.NewMemoryStore(), computer.Config{})
	compAPI := &ComputerAPI{Manager: mgr, UserID: "user-ws-1"}

	mux := http.NewServeMux()
	compAPI.register(mux)
	apiSrv := httptest.NewServer(mux)
	defer apiSrv.Close()

	// 1. GET /v1/computer -> not_provisioned
	resp, err := http.Get(apiSrv.URL + "/v1/computer")
	if err != nil {
		t.Fatalf("GET /v1/computer: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/computer = %d", resp.StatusCode)
	}
	var st computerStatusResponse
	_ = json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if !st.Enabled || st.Status != "not_provisioned" {
		t.Fatalf("unexpected initial status: %+v", st)
	}

	// 2. POST /v1/computer/sessions -> creates view session
	resp, err = http.Post(apiSrv.URL+"/v1/computer/sessions", "application/json", strings.NewReader(`{"mode":"view","task_id":"task-1"}`))
	if err != nil {
		t.Fatalf("POST /v1/computer/sessions: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /v1/computer/sessions = %d", resp.StatusCode)
	}
	var created createComputerSessionResponse
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.Token == "" || created.Mode != "view" {
		t.Fatalf("unexpected created session: %+v", created)
	}

	// 3. Connect WebSocket to Nori Core proxy in view mode.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(apiSrv.URL, "http") + created.WSPath + "?token=" + created.Token
	clientConn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		t.Fatalf("dial proxy WS: %v", err)
	}
	defer clientConn.Close(websocket.StatusNormalClosure, "")

	// Complete minimal RFB handshake (3 messages).
	_ = clientConn.Write(ctx, websocket.MessageBinary, []byte("RFB 003.008\n"))
	_ = clientConn.Write(ctx, websocket.MessageBinary, []byte{1})
	_ = clientConn.Write(ctx, websocket.MessageBinary, []byte{1})
	for range 3 {
		<-receivedCh
	}

	// Send KeyEvent (type 4) followed by FramebufferUpdateRequest (type 3) in view mode.
	keyEv := []byte{4, 1, 0, 0, 0, 0, 0, 65}
	fbReq := []byte{3, 0, 0, 0, 0, 0, 5, 0, 3, 32}
	_ = clientConn.Write(ctx, websocket.MessageBinary, keyEv)
	_ = clientConn.Write(ctx, websocket.MessageBinary, fbReq)

	select {
	case got := <-receivedCh:
		if !bytes.Equal(got, fbReq) {
			t.Fatalf("expected KeyEvent dropped and FramebufferUpdateRequest forwarded, got %x", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for forwarded FramebufferUpdateRequest")
	}

	// 4. Switch session to control mode via PATCH /v1/computer/sessions/{id} and verify KeyEvent now passes!
	patchReq, _ := http.NewRequest(http.MethodPatch, apiSrv.URL+"/v1/computer/sessions/"+created.ID, strings.NewReader(`{"mode":"control"}`))
	patchReq.Header.Set("Content-Type", "application/json")
	patchResp, err := http.DefaultClient.Do(patchReq)
	if err != nil || patchResp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH session mode = %d, %v", patchResp.StatusCode, err)
	}
	patchResp.Body.Close()

	_ = clientConn.Write(ctx, websocket.MessageBinary, keyEv)
	select {
	case got := <-receivedCh:
		if !bytes.Equal(got, keyEv) {
			t.Fatalf("expected control mode to forward KeyEvent, got %x", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for KeyEvent after taking control")
	}

	// Switch back to view mode and verify synchronous handback takes effect IMMEDIATELY (no 300ms wait!).
	patchView, _ := http.NewRequest(http.MethodPatch, apiSrv.URL+"/v1/computer/sessions/"+created.ID, strings.NewReader(`{"mode":"view"}`))
	patchView.Header.Set("Content-Type", "application/json")
	pvResp, err := http.DefaultClient.Do(patchView)
	if err != nil || pvResp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH view mode = %d, %v", pvResp.StatusCode, err)
	}
	pvResp.Body.Close()

	// Send KeyEvent + FramebufferUpdateRequest immediately without sleeping; KeyEvent must be dropped!
	_ = clientConn.Write(ctx, websocket.MessageBinary, keyEv)
	_ = clientConn.Write(ctx, websocket.MessageBinary, fbReq)
	select {
	case got := <-receivedCh:
		if !bytes.Equal(got, fbReq) {
			t.Fatalf("expected immediate handback to drop KeyEvent and forward fbReq, got %x", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for fbReq after immediate handback")
	}
}

func TestRFBClientParserFragmentedAndCoalesced(t *testing.T) {
	p := newRFBClientParser()

	// Split 12-byte handshake across 2 chunks, with SecurityType + ClientInit + KeyEvent coalesced into chunk 2.
	keyEv := []byte{4, 1, 0, 0, 0, 0, 0, 65}
	fbReq := []byte{3, 0, 0, 0, 0, 0, 5, 0, 3, 32}

	out1, dropped1, human1, err1 := p.Feed(computer.SessionModeView, []byte("RFB 003"))
	if err1 != nil || len(out1) != 0 || dropped1 || human1 {
		t.Fatalf("chunk 1 = (%x, %v, %v, %v), want buffered partial handshake", out1, dropped1, human1, err1)
	}

	chunk2 := append([]byte(".008\n"), 1, 1)
	chunk2 = append(chunk2, keyEv...)
	chunk2 = append(chunk2, fbReq...)
	out2, dropped2, human2, err2 := p.Feed(computer.SessionModeView, chunk2)
	if err2 != nil || !dropped2 || human2 {
		t.Fatalf("chunk 2 = (%x, dropped=%v, human=%v, err=%v), want KeyEvent dropped in view mode", out2, dropped2, human2, err2)
	}
	wantOut2 := append(append([]byte("RFB 003.008\n"), 1, 1), fbReq...)
	if !bytes.Equal(out2, wantOut2) {
		t.Fatalf("out2 = %x, want %x", out2, wantOut2)
	}

	// Fragmented PointerEvent across 3 WebSocket chunks in view mode must be buffered and completely dropped.
	ptrEv := []byte{5, 1, 0, 100, 0, 200}
	for idx, part := range [][]byte{ptrEv[:2], ptrEv[2:4], ptrEv[4:]} {
		out, dropped, human, err := p.Feed(computer.SessionModeView, part)
		if err != nil || len(out) != 0 || human {
			t.Fatalf("ptr chunk %d = (%x, %v, %v, %v)", idx, out, dropped, human, err)
		}
		if idx == 2 && !dropped {
			t.Fatalf("expected final chunk of fragmented PointerEvent to report dropped=true")
		}
	}

	// FramebufferUpdateRequest in control mode does NOT count as human input (cannot keep idle tab alive).
	outFB, droppedFB, humanFB, errFB := p.Feed(computer.SessionModeControl, fbReq)
	if errFB != nil || droppedFB || humanFB || !bytes.Equal(outFB, fbReq) {
		t.Fatalf("control fbReq = (%x, dropped=%v, human=%v, err=%v), want human=false", outFB, droppedFB, humanFB, errFB)
	}

	// KeyEvent in control mode DOES count as human input.
	outK, droppedK, humanK, errK := p.Feed(computer.SessionModeControl, keyEv)
	if errK != nil || droppedK || !humanK || !bytes.Equal(outK, keyEv) {
		t.Fatalf("control keyEv = (%x, dropped=%v, human=%v, err=%v), want human=true", outK, droppedK, humanK, errK)
	}
}

func TestDesktopWSHardeningCapsSingleUseLifetimePollAndForceClose(t *testing.T) {
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer upstreamSrv.Close()

	fp := fake.New()
	fp.DesktopWSURL = "ws" + strings.TrimPrefix(upstreamSrv.URL, "http") + "/websockify"
	compStore := computer.NewMemoryStore()
	bs := billing.NewMemoryStore(nil)
	bsvc := billing.NewService(bs, false, billing.GlobalBudgetConfig{})
	_, _ = bs.SetTier(context.Background(), "user-std", billing.TierStandard, billing.SetTierOptions{})
	_, _ = bs.SetTier(context.Background(), "user-pwr", billing.TierPower, billing.SetTierOptions{})

	mgr := computer.NewManager(fp, compStore, computer.Config{})
	mgr.SetBilling(bsvc)
	rl := NewRateLimiter(settings.DefaultRateLimit())

	newServer := func(userID string, maxLife, poll time.Duration) (*ComputerAPI, *httptest.Server) {
		compAPI := &ComputerAPI{
			Manager:        mgr,
			UserID:         userID,
			RateLimiter:    rl,
			Billing:        bsvc,
			MaxWSLifetime:  maxLife,
			WSPollInterval: poll,
		}
		mux := http.NewServeMux()
		compAPI.register(mux)
		return compAPI, httptest.NewServer(mux)
	}

	createSess := func(t *testing.T, baseURL string) createComputerSessionResponse {
		t.Helper()
		resp, err := http.Post(baseURL+"/v1/computer/sessions", "application/json", strings.NewReader(`{"mode":"view"}`))
		if err != nil || resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST sessions = %v, %v", resp, err)
		}
		defer resp.Body.Close()
		var out createComputerSessionResponse
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Standard tier (MaxDesktopWS = 1): single-use token + WS concurrency cap + CloseDesktopSessions.
	stdAPI, stdSrv := newServer("user-std", 2*time.Hour, 5*time.Second)
	defer stdSrv.Close()

	s1 := createSess(t, stdSrv.URL)
	wsURL1 := "ws" + strings.TrimPrefix(stdSrv.URL, "http") + s1.WSPath + "?token=" + s1.Token
	conn1, _, err := websocket.Dial(ctx, wsURL1, &websocket.DialOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		t.Fatalf("dial #1: %v", err)
	}

	// Second concurrent WS for standard user hits 429 desktop_ws_limit_exceeded!
	s2 := createSess(t, stdSrv.URL)
	wsURL2 := "ws" + strings.TrimPrefix(stdSrv.URL, "http") + s2.WSPath + "?token=" + s2.Token
	_, resp2, err2 := websocket.Dial(ctx, wsURL2, &websocket.DialOptions{Subprotocols: []string{"binary"}})
	if err2 == nil || resp2 == nil || resp2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for 2nd standard WS, got resp=%v err=%v", resp2, err2)
	}
	resp2.Body.Close()

	// CloseDesktopSessions immediately closes conn1 with close code 4004 (session revoked)!
	closed := stdAPI.CloseDesktopSessions("user-std")
	if closed < 1 {
		t.Fatalf("CloseDesktopSessions = %d, want >= 1", closed)
	}
	readCtx, readCancel := context.WithTimeout(ctx, 2*time.Second)
	_, _, readErr := conn1.Read(readCtx)
	readCancel()
	if readErr == nil {
		t.Fatal("expected conn1 to close after CloseDesktopSessions")
	}
	if code := websocket.CloseStatus(readErr); code != DesktopWSCloseSessionRevoked {
		t.Fatalf("CloseDesktopSessions WS close code = %d, want %d (4004)", code, DesktopWSCloseSessionRevoked)
	}
	_ = conn1.Close(websocket.StatusNormalClosure, "")

	// Wait briefly for server handler defer to release slot, then verify single-use token replay of s1 fails with 401!
	time.Sleep(50 * time.Millisecond)
	_, replayResp, replayErr := websocket.Dial(ctx, wsURL1, &websocket.DialOptions{Subprotocols: []string{"binary"}})
	if replayErr == nil || replayResp == nil || replayResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 on replayed single-use token, got resp=%v err=%v", replayResp, replayErr)
	}
	replayResp.Body.Close()

	// 2. Power tier (MaxDesktopWS = 2): 2 concurrent sockets succeed, 3rd returns 429.
	_, pwrSrv := newServer("user-pwr", 2*time.Hour, 5*time.Second)
	defer pwrSrv.Close()

	p1 := createSess(t, pwrSrv.URL)
	p2 := createSess(t, pwrSrv.URL)
	p3 := createSess(t, pwrSrv.URL)
	c1, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(pwrSrv.URL, "http")+p1.WSPath+"?token="+p1.Token, &websocket.DialOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		t.Fatalf("power dial #1: %v", err)
	}
	defer c1.Close(websocket.StatusNormalClosure, "")
	c2, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(pwrSrv.URL, "http")+p2.WSPath+"?token="+p2.Token, &websocket.DialOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		t.Fatalf("power dial #2: %v", err)
	}
	defer c2.Close(websocket.StatusNormalClosure, "")
	_, r3, e3 := websocket.Dial(ctx, "ws"+strings.TrimPrefix(pwrSrv.URL, "http")+p3.WSPath+"?token="+p3.Token, &websocket.DialOptions{Subprotocols: []string{"binary"}})
	if e3 == nil || r3 == nil || r3.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for 3rd power WS, got resp=%v err=%v", r3, e3)
	}
	r3.Body.Close()

	// 3. Max WS lifetime closes connection automatically with close code 4001.
	_, shortSrv := newServer("user-std", 80*time.Millisecond, 5*time.Second)
	defer shortSrv.Close()
	sShort := createSess(t, shortSrv.URL)
	cShort, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(shortSrv.URL, "http")+sShort.WSPath+"?token="+sShort.Token, &websocket.DialOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		t.Fatalf("short lifetime dial: %v", err)
	}
	defer cShort.Close(websocket.StatusNormalClosure, "")
	lifeCtx, lifeCancel := context.WithTimeout(ctx, 2*time.Second)
	_, _, lifeErr := cShort.Read(lifeCtx)
	lifeCancel()
	if lifeErr == nil {
		t.Fatal("expected WS connection to close when MaxWSLifetime elapsed")
	}
	if code := websocket.CloseStatus(lifeErr); code != DesktopWSCloseMaxLifetime {
		t.Fatalf("MaxWSLifetime WS close code = %d, want %d (4001)", code, DesktopWSCloseMaxLifetime)
	}

	// 4. Computer sleep closes active WS with close code 4002.
	time.Sleep(50 * time.Millisecond)
	_, sleepSrv := newServer("user-std", 2*time.Hour, 5*time.Second)
	defer sleepSrv.Close()
	sSleep := createSess(t, sleepSrv.URL)
	cSleep, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(sleepSrv.URL, "http")+sSleep.WSPath+"?token="+sSleep.Token, &websocket.DialOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		t.Fatalf("sleep dial: %v", err)
	}
	defer cSleep.Close(websocket.StatusNormalClosure, "")
	if err := mgr.Sleep(ctx, "user-std"); err != nil {
		t.Fatalf("mgr.Sleep: %v", err)
	}
	sleepCtx, sleepCancel := context.WithTimeout(ctx, 2*time.Second)
	_, _, sleepErr := cSleep.Read(sleepCtx)
	sleepCancel()
	if sleepErr == nil {
		t.Fatal("expected WS connection to close on computer sleep")
	}
	if code := websocket.CloseStatus(sleepErr); code != DesktopWSCloseIdleSleeping {
		t.Fatalf("Sleep WS close code = %d, want %d (4002)", code, DesktopWSCloseIdleSleeping)
	}

	// 5. Periodic entitlement poll closes open WS cleanly with close code 4003 when user is suspended mid-session.
	time.Sleep(50 * time.Millisecond)
	_, pollSrv := newServer("user-std", 2*time.Hour, 40*time.Millisecond)
	defer pollSrv.Close()
	sPoll := createSess(t, pollSrv.URL)
	cPoll, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(pollSrv.URL, "http")+sPoll.WSPath+"?token="+sPoll.Token, &websocket.DialOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		t.Fatalf("poll dial: %v", err)
	}
	defer cPoll.Close(websocket.StatusNormalClosure, "")
	_, _ = bsvc.Suspend(ctx, "user-std", "policy")
	pollCtx, pollCancel := context.WithTimeout(ctx, 2*time.Second)
	_, _, pollErr := cPoll.Read(pollCtx)
	pollCancel()
	if pollErr == nil {
		t.Fatal("expected open WS to close on entitlement poll after suspension")
	}
	if code := websocket.CloseStatus(pollErr); code != DesktopWSCloseEntitlement {
		t.Fatalf("Entitlement poll WS close code = %d, want %d (4003)", code, DesktopWSCloseEntitlement)
	}
}

func TestDesktopEntitlementRespectsDisableMetering(t *testing.T) {
	ctx := context.Background()
	bs := billing.NewMemoryStore(nil)
	bsvc := billing.NewService(bs, false, billing.GlobalBudgetConfig{})
	// BYOK user has $0 hosted credit, but DisableMetering=true should allow CheckDesktopAccess.
	if _, err := bs.SetTier(ctx, "user-byok", billing.TierBYOK, billing.SetTierOptions{}); err != nil {
		t.Fatalf("SetTier byok: %v", err)
	}
	mgr := computer.NewManager(fake.New(), computer.NewMemoryStore(), computer.Config{DisableMetering: true})
	mgr.SetBilling(bsvc)
	compAPI := &ComputerAPI{Manager: mgr, UserID: "user-byok", Billing: bsvc}
	if _, err := compAPI.checkDesktopEntitlement(ctx); err != nil {
		t.Fatalf("checkDesktopEntitlement with DisableMetering=true = %v, want nil", err)
	}
}
