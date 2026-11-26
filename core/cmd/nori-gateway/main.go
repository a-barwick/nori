package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"nori/internal/gateway"
)

func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func randomToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func main() {
	listenAddr := flag.String("listen", "127.0.0.1:0", "Loopback address to bind (default 127.0.0.1:0)")
	maxUSD := flag.Float64("max-usd", 0, "Hard cumulative USD exposure ceiling (0 disables)")
	maxCalls := flag.Int("max-calls", 0, "Hard cumulative admitted LLM call ceiling (0 disables)")
	readyFile := flag.String("ready-file", "", "Optional path to write ready JSON {addr,url} once listening")
	flag.Parse()

	host, _, err := net.SplitHostPort(*listenAddr)
	if err != nil || !isLoopbackHost(host) {
		slog.Error("nori-gateway refuses to bind non-loopback address", "listen", *listenAddr)
		os.Exit(2)
	}

	token := strings.TrimSpace(os.Getenv("NORI_GATEWAY_TOKEN"))
	if token == "" {
		token, err = randomToken()
		if err != nil {
			slog.Error("failed generating gateway token", "err", err)
			os.Exit(1)
		}
	}

	openaiKey := cmp.Or(strings.TrimSpace(os.Getenv("OPENAI_TEST_API_KEY")), strings.TrimSpace(os.Getenv("OPENAI_API_KEY")))
	anthropicKey := cmp.Or(strings.TrimSpace(os.Getenv("ANTHROPIC_TEST_API_KEY")), strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")))
	openrouterKey := cmp.Or(strings.TrimSpace(os.Getenv("OPENROUTER_TEST_API_KEY")), strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")))

	srv, err := gateway.New(gateway.Config{
		Token:         token,
		OpenAIKey:     openaiKey,
		AnthropicKey:  anthropicKey,
		OpenRouterKey: openrouterKey,
		MaxUSD:        *maxUSD,
		MaxCalls:      *maxCalls,
	})
	if err != nil {
		slog.Error("failed initializing gateway", "err", err)
		os.Exit(1)
	}

	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		slog.Error("failed listening on loopback address", "listen", *listenAddr, "err", err)
		os.Exit(1)
	}
	boundAddr := ln.Addr().String()
	baseURL := fmt.Sprintf("http://%s/v1", boundAddr)

	if *readyFile != "" {
		payload, _ := json.Marshal(map[string]any{
			"addr": boundAddr,
			"url":  baseURL,
		})
		tmp := *readyFile + ".tmp"
		if err := os.WriteFile(tmp, payload, 0o600); err != nil {
			slog.Error("failed writing ready file", "err", err)
			os.Exit(1)
		}
		if err := os.Rename(tmp, *readyFile); err != nil {
			slog.Error("failed renaming ready file", "err", err)
			os.Exit(1)
		}
	}

	httpServer := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	slog.Info("nori-gateway listening", "url", baseURL, "max_usd", *maxUSD, "max_calls", *maxCalls)
	if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
		slog.Error("gateway server error", "err", err)
		os.Exit(1)
	}
}
