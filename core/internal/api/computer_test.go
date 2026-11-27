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
	"nori/internal/computer"
	"nori/internal/computer/fake"
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
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/computer = %d, %v", resp.StatusCode, err)
	}
	var st computerStatusResponse
	_ = json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if !st.Enabled || st.Status != "not_provisioned" {
		t.Fatalf("unexpected initial status: %+v", st)
	}

	// 2. POST /v1/computer/sessions -> creates view session
	resp, err = http.Post(apiSrv.URL+"/v1/computer/sessions", "application/json", strings.NewReader(`{"mode":"view","task_id":"task-1"}`))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /v1/computer/sessions = %d, %v", resp.StatusCode, err)
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
