package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"nori/internal/conversation"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func main() {
	if err := godotenv.Load(env("NORI_ENV_FILE", ".env")); err != nil && !os.IsNotExist(err) {
		log.Fatal("could not load the configured .env; check its syntax and permissions")
	}
	key := os.Getenv("OPENROUTER_API_KEY")
	if strings.TrimSpace(key) == "" {
		log.Fatal("OPENROUTER_API_KEY is missing; add it to the existing .env selected by NORI_ENV_FILE")
	}
	base := env("NORI_AGENT_BASE_URL", "https://openrouter.ai/api/v1")
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		log.Fatal("NORI_AGENT_BASE_URL must be an HTTPS API base URL")
	}
	model := env("NORI_AGENT_MODEL", "meta/muse-spark-1.3-contributor")
	runner := conversation.New(conversation.NewModel(base, key, model))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok", "model": model})
	})
	mux.HandleFunc("POST /api/session", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, runner.Snapshot())
	})
	mux.HandleFunc("POST /api/turn", func(w http.ResponseWriter, r *http.Request) {
		var input conversation.Input
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, 400, "Send a JSON object with id and message.")
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			writeError(w, 400, "Send one JSON object.")
			return
		}
		input.Message = strings.TrimSpace(input.Message)
		if _, err := uuid.Parse(input.ID); err != nil || input.Message == "" || len(input.Message) > 8192 {
			writeError(w, 400, "A UUID id and a message of 1–8192 bytes are required.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 75*time.Second)
		defer cancel()
		session, err := runner.HandleInput(ctx, input)
		if err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, conversation.ErrBusy) || errors.Is(err, conversation.ErrConflict) {
				status = http.StatusConflict
			}
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, 200, session)
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			writeError(w, 403, "Cross-origin requests are not allowed.")
			return
		}
		if r.Method == http.MethodPost && strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
			writeError(w, 415, "Content-Type must be application/json.")
			return
		}
		mux.ServeHTTP(w, r)
	})
	server := &http.Server{Addr: net.JoinHostPort("127.0.0.1", env("PORT", "8080")), Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 85 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server.BaseContext = func(net.Listener) context.Context { return ctx }
	go func() {
		<-ctx.Done()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(cleanup)
	}()
	log.Printf("Nori local agent on http://%s model=%s; conversation and accepted dispatches live in memory", server.Addr, model)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
