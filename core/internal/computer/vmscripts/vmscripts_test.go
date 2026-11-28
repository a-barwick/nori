package vmscripts_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nori/internal/computer/vmscripts"
)

func TestDesktopStartScriptSyntaxAndInvariants(t *testing.T) {
	if strings.TrimSpace(vmscripts.Version) == "" {
		t.Fatal("Version must not be empty")
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "nori-desktop-start")
	if err := os.WriteFile(scriptPath, []byte(vmscripts.DesktopStartSH), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", "-n", scriptPath).CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n nori-desktop-start failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"cd /",
		"is_ws_rfb_ready()",
		`GATE_SCRIPT="/home/nori/.nori/bin/nori-desktop-gate"`,
		`is_ws_rfb_ready "${WS_INTERNAL_PORT}"`,
		`is_ws_rfb_ready "${DESKTOP_PORT}" "${TOKEN_FILE}"`,
		`"(deleted)"`,
		`[r]emote-debugging-port=${CDP_PORT}`,
	} {
		if !strings.Contains(vmscripts.DesktopStartSH, want) {
			t.Fatalf("nori-desktop-start missing %q", want)
		}
	}
}

func extractShellFunc(t *testing.T, script, funcName string) string {
	t.Helper()
	start := strings.Index(script, funcName+"() {")
	if start < 0 {
		t.Fatalf("function %s not found", funcName)
	}
	end := strings.Index(script[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("end of function %s not found", funcName)
	}
	return script[start : start+end+3]
}

func TestIsWSRFBReadyProbeAgainstMockServers(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available in PATH")
	}
	fnDef := extractShellFunc(t, vmscripts.DesktopStartSH, "is_ws_rfb_ready")

	runProbe := func(port int, tokenFile string) error {
		cmdStr := fnDef + fmt.Sprintf("\nis_ws_rfb_ready %d %q\n", port, tokenFile)
		return exec.Command("bash", "-c", cmdStr).Run()
	}

	// 1. Healthy server sending 101 + fragmented WS frame header and RFB banner -> passes.
	healthyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer healthyLn.Close()
	go func() {
		for {
			conn, aErr := healthyLn.Accept()
			if aErr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, rErr := r.ReadString('\n')
					if rErr != nil || line == "\r\n" {
						break
					}
				}
				_, _ = io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
				_, _ = c.Write([]byte{0x82, 0x0c})
				time.Sleep(25 * time.Millisecond)
				_, _ = io.WriteString(c, "RFB 003.008\n")
			}(conn)
		}
	}()
	if err := runProbe(healthyLn.Addr().(*net.TCPAddr).Port, ""); err != nil {
		t.Fatalf("is_ws_rfb_ready failed against healthy fragmented RFB server: %v", err)
	}

	// 2. Wedged server that accepts TCP and immediately closes (like websockify Errno 2) -> fails.
	wedgedLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer wedgedLn.Close()
	go func() {
		for {
			conn, aErr := wedgedLn.Accept()
			if aErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	if err := runProbe(wedgedLn.Addr().(*net.TCPAddr).Port, ""); err == nil {
		t.Fatal("is_ws_rfb_ready unexpectedly succeeded against wedged closing server")
	}
}

func TestDesktopGateAuthAndUpgradeProxy(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available in PATH")
	}

	// Mock upstream websockify listener that verifies token stripping and replies 101 + RFB banner.
	upstreamLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstreamLn.Close()
	upstreamPort := upstreamLn.Addr().(*net.TCPAddr).Port

	sawTokenHeader := make(chan bool, 4)
	go func() {
		for {
			conn, aErr := upstreamLn.Accept()
			if aErr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				reader := bufio.NewReader(c)
				leaked := false
				for {
					line, rErr := reader.ReadString('\n')
					if rErr != nil {
						return
					}
					if strings.HasPrefix(strings.ToLower(line), "x-nori-desktop-token:") {
						leaked = true
					}
					if line == "\r\n" {
						break
					}
				}
				sawTokenHeader <- leaked
				_, _ = io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: test\r\nSec-WebSocket-Protocol: binary\r\n\r\n")
				_, _ = c.Write([]byte{0x82, 0x0c})
				_, _ = io.WriteString(c, "RFB 003.008\n")
			}(conn)
		}
	}()

	// Reserve an ephemeral port for the gate.
	probeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gatePort := probeLn.Addr().(*net.TCPAddr).Port
	_ = probeLn.Close()

	dir := t.TempDir()
	gatePath := filepath.Join(dir, "nori-desktop-gate")
	tokenPath := filepath.Join(dir, "desktop.token")
	const secretToken = "test_desktop_token_0123456789abcdef01234567"
	if err := os.WriteFile(gatePath, []byte(vmscripts.DesktopGateJS), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte(secretToken), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, nodePath, gatePath, tokenPath, fmt.Sprintf("%d", gatePort), fmt.Sprintf("%d", upstreamPort))
	if err := cmd.Start(); err != nil {
		t.Fatalf("start nori-desktop-gate: %v", err)
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
	}()

	// Wait for gate to listen.
	gateAddr := fmt.Sprintf("127.0.0.1:%d", gatePort)
	ready := false
	for range 30 {
		c, dErr := net.DialTimeout("tcp", gateAddr, 100*time.Millisecond)
		if dErr == nil {
			_ = c.Close()
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("nori-desktop-gate did not listen on %s", gateAddr)
	}

	// 1. Plain HTTP returns 404.
	resp, err := http.Get("http://" + gateAddr + "/websockify")
	if err != nil {
		t.Fatalf("GET gate: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET status = %d, want 404", resp.StatusCode)
	}

	sendUpgrade := func(token string) string {
		t.Helper()
		conn, dErr := net.DialTimeout("tcp", gateAddr, time.Second)
		if dErr != nil {
			t.Fatalf("dial gate: %v", dErr)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		rawKey := make([]byte, 16)
		_, _ = rand.Read(rawKey)
		req := fmt.Sprintf(
			"GET /websockify HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: binary\r\n",
			gateAddr, base64.StdEncoding.EncodeToString(rawKey),
		)
		if token != "" {
			req += "X-Nori-Desktop-Token: " + token + "\r\n"
		}
		req += "\r\n"
		if _, wErr := io.WriteString(conn, req); wErr != nil {
			t.Fatalf("write upgrade: %v", wErr)
		}
		buf := make([]byte, 1024)
		n, _ := conn.Read(buf)
		return string(buf[:n])
	}

	// 2. Missing or wrong token returns 401.
	if got := sendUpgrade(""); !strings.HasPrefix(got, "HTTP/1.1 401 ") {
		t.Fatalf("missing-token upgrade = %q, want 401", got)
	}
	if got := sendUpgrade("wrong-token"); !strings.HasPrefix(got, "HTTP/1.1 401 ") {
		t.Fatalf("wrong-token upgrade = %q, want 401", got)
	}

	// 3. Valid token proxies 101 + RFB frame and strips X-Nori-Desktop-Token from upstream headers.
	got := sendUpgrade(secretToken)
	if !strings.HasPrefix(got, "HTTP/1.1 101 ") || !strings.Contains(got, "RFB 003.008") {
		t.Fatalf("valid-token upgrade = %q, want 101 + RFB banner", got)
	}
	select {
	case leaked := <-sawTokenHeader:
		if leaked {
			t.Fatal("X-Nori-Desktop-Token leaked to upstream websockify")
		}
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive upgrade request")
	}

	// 4. End-to-end is_ws_rfb_ready probe passes against gate with token file and fails without token file.
	if _, err := exec.LookPath("python3"); err == nil {
		fnDef := extractShellFunc(t, vmscripts.DesktopStartSH, "is_ws_rfb_ready")
		if err := exec.Command("bash", "-c", fnDef+fmt.Sprintf("\nis_ws_rfb_ready %d %q\n", gatePort, tokenPath)).Run(); err != nil {
			t.Fatalf("is_ws_rfb_ready with token failed against gate: %v", err)
		}
		if err := exec.Command("bash", "-c", fnDef+fmt.Sprintf("\nis_ws_rfb_ready %d\n", gatePort)).Run(); err == nil {
			t.Fatal("is_ws_rfb_ready without token unexpectedly succeeded against gate")
		}
	}
}
