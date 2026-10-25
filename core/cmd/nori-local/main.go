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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"nori/internal/computer"
	"nori/internal/conversation"
	"nori/internal/state"
	"nori/internal/task"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func main() {
	envFile := env("NORI_ENV_FILE", ".env")
	if err := godotenv.Load(envFile); err != nil && !os.IsNotExist(err) {
		log.Fatal("could not load the configured .env; check its syntax and permissions")
	}
	if err := godotenv.Load(env("NORI_TASK_ENV_FILE", filepath.Join(filepath.Dir(envFile), "..", ".env"))); err != nil && !os.IsNotExist(err) {
		log.Fatal("could not load task configuration")
	}
	key := os.Getenv("OPENROUTER_API_KEY")
	if strings.TrimSpace(key) == "" {
		log.Fatal("OPENROUTER_API_KEY is missing; add it to the existing .env selected by NORI_ENV_FILE")
	}
	jevKey := os.Getenv("JEV_API_KEY")
	if strings.TrimSpace(jevKey) == "" {
		log.Fatal("JEV_API_KEY is missing from the selected .env")
	}
	base := env("NORI_AGENT_BASE_URL", "https://openrouter.ai/api/v1")
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		log.Fatal("NORI_AGENT_BASE_URL must be an HTTPS API base URL")
	}
	model := env("NORI_AGENT_MODEL", "meta/muse-spark-1.3-contributor")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	project := env("NORI_PROJECT_ID", "bot-111549")
	store, err := state.NewGoogle(ctx, project, env("FIRESTORE_DATABASE_ID", "nori-poc"), env("ARTIFACT_BUCKET", "bot-111549-nori-poc"))
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	temporalClient, err := task.Dial(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer temporalClient.Close()
	tasks := &conversation.Tasks{Store: store, Client: temporalClient, Computer: computer.Config{Project: project, Zone: env("COMPUTER_ZONE", "us-central1-a"), Instance: env("COMPUTER_INSTANCE", "nori-computer")}}
	runner := conversation.New(conversation.NewModel(base, key, model), conversation.NewClassifier(jevKey), tasks)
	if seed := os.Getenv("NORI_CONVERSATION_SEED"); seed != "" {
		data, err := os.ReadFile(seed)
		if err != nil {
			log.Fatal("could not read the saved local conversation")
		}
		var previous conversation.Session
		if err := json.Unmarshal(data, &previous); err != nil {
			log.Fatal("saved local conversation is invalid")
		}
		runner.RestoreConversation(previous.Messages)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok", "model": model})
	})
	mux.HandleFunc("POST /api/session", func(w http.ResponseWriter, r *http.Request) {
		session, err := runner.Snapshot(r.Context())
		if err != nil {
			writeError(w, 502, err.Error())
			return
		}
		writeJSON(w, 200, session)
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
		ctx, cancel := context.WithTimeout(r.Context(), 95*time.Second)
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
	mux.HandleFunc("POST /api/input", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID      string `json:"id"`
			RunID   string `json:"run_id"`
			Message string `json:"message"`
		}
		if !decode(w, r, &body) {
			return
		}
		body.Message = strings.TrimSpace(body.Message)
		if !validID(body.ID) || !validID(body.RunID) || body.Message == "" || len(body.Message) > 8192 {
			writeError(w, 400, "UUID id, run_id, and a message of 1–8192 bytes are required.")
			return
		}
		run, err := tasks.Update(r.Context(), body.ID, body.RunID, body.Message)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		writeJSON(w, 202, map[string]any{"run": run})
	})
	mux.HandleFunc("POST /api/cancel", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID string `json:"id"`
		}
		if !decode(w, r, &body) {
			return
		}
		if !validID(body.ID) {
			writeError(w, 400, "id must be a UUID")
			return
		}
		run, err := tasks.Cancel(r.Context(), body.ID)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		writeJSON(w, 202, map[string]any{"run": run})
	})
	mux.HandleFunc("POST /api/artifact", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RunID      string `json:"run_id"`
			ArtifactID string `json:"artifact_id"`
		}
		if !decode(w, r, &body) {
			return
		}
		if !validID(body.RunID) || !validID(body.ArtifactID) {
			writeError(w, 400, "run_id and artifact_id must be UUIDs")
			return
		}
		data, err := store.GetPNG(r.Context(), body.RunID, body.ArtifactID)
		if err != nil {
			writeError(w, 404, "Screenshot unavailable.")
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
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
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 105 * time.Second, IdleTimeout: 60 * time.Second}
	server.BaseContext = func(net.Listener) context.Context { return ctx }
	go func() {
		<-ctx.Done()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(cleanup)
	}()
	log.Printf("Nori outer agent on http://%s model=%s; Jev hints and existing Firestore/Temporal tasks", server.Addr, model)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func validID(value string) bool { _, err := uuid.Parse(value); return err == nil }

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		writeError(w, 400, "Send a valid JSON object.")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		writeError(w, 400, "Send one JSON object.")
		return false
	}
	return true
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
