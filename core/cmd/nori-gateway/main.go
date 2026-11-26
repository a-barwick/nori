package main

import (
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
	"nori/internal/httpserver"
	"nori/internal/netguard"
)

func randomToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

type gatewayKeys struct {
	openAI     string
	anthropic  string
	openRouter string
}

// loadGatewayKeys reads dedicated *_TEST_API_KEY variables only and refuses
// key reuse with production *_API_KEY / *_PROD_API_KEY variables.
func loadGatewayKeys(getenv func(string) string) (gatewayKeys, error) {
	read := func(name string) string { return strings.TrimSpace(getenv(name)) }
	checkIsolated := func(testVar string, prodVars ...string) (string, error) {
		val := read(testVar)
		if val == "" {
			return "", nil
		}
		for _, prodVar := range prodVars {
			if prod := read(prodVar); prod != "" && prod == val {
				return "", fmt.Errorf("%s must not reuse %s", testVar, prodVar)
			}
		}
		return val, nil
	}
	openAI, err := checkIsolated("OPENAI_TEST_API_KEY", "OPENAI_API_KEY", "OPENAI_PROD_API_KEY")
	if err != nil {
		return gatewayKeys{}, err
	}
	anthropic, err := checkIsolated("ANTHROPIC_TEST_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_PROD_API_KEY")
	if err != nil {
		return gatewayKeys{}, err
	}
	openRouter, err := checkIsolated("OPENROUTER_TEST_API_KEY", "OPENROUTER_API_KEY", "OPENROUTER_PROD_API_KEY")
	if err != nil {
		return gatewayKeys{}, err
	}
	return gatewayKeys{
		openAI:     openAI,
		anthropic:  anthropic,
		openRouter: openRouter,
	}, nil
}

func main() {
	listenAddr := flag.String("listen", "127.0.0.1:0", "Loopback address to bind (default 127.0.0.1:0)")
	maxUSD := flag.Float64("max-usd", 0, "Hard cumulative USD exposure ceiling (0 disables)")
	maxCalls := flag.Int("max-calls", 0, "Hard cumulative admitted LLM call ceiling (0 disables)")
	readyFile := flag.String("ready-file", "", "Optional path to write ready JSON {addr,url} once listening")
	flag.Parse()

	host, _, err := net.SplitHostPort(*listenAddr)
	if err != nil || !netguard.IsLoopbackHost(host) {
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

	keys, err := loadGatewayKeys(os.Getenv)
	if err != nil {
		slog.Error("nori-gateway key isolation check failed", "err", err)
		os.Exit(2)
	}

	srv, err := gateway.New(gateway.Config{
		Token:         token,
		OpenAIKey:     keys.openAI,
		AnthropicKey:  keys.anthropic,
		OpenRouterKey: keys.openRouter,
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

	server := &http.Server{
		Addr:              boundAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("nori-gateway listening", "url", baseURL, "max_usd", *maxUSD, "max_calls", *maxCalls)
	if err := httpserver.Serve(ctx, server, ln, 5*time.Second); err != nil {
		slog.Error("gateway server error", "err", err)
		os.Exit(1)
	}
}
