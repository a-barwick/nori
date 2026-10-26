package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"go.temporal.io/sdk/client"
	"google.golang.org/genai"
	"nori/internal/agent"
	"nori/internal/computer"
	"nori/internal/state"
	"nori/internal/task"
)

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func main() {
	if err := godotenv.Load(env("NORI_ENV_FILE", "../.env")); err != nil && !os.IsNotExist(err) {
		log.Fatal("could not load .env; check its syntax and permissions")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	project := env("NORI_PROJECT_ID", "bot-111549")
	database := env("FIRESTORE_DATABASE_ID", "nori-poc")
	modelLocation := env("MODEL_LOCATION", "global")
	address := net.JoinHostPort(env("BIND_HOST", "127.0.0.1"), env("PORT", "8080"))
	log.Printf("Nori startup project=%s database=%s model_location=%s listen_address=%s", project, database, modelLocation, address)
	log.Print("Nori initializing Firestore and storage clients")
	store, e := state.NewGoogle(ctx, project, database, os.Getenv("ARTIFACT_BUCKET"))
	if e != nil {
		log.Fatal(e)
	}
	defer store.Close()
	log.Print("Nori initializing Vertex model client")
	modelAttempts := int32(1)
	model, e := genai.NewClient(ctx, &genai.ClientConfig{Backend: genai.BackendVertexAI, Project: project, Location: modelLocation, HTTPOptions: genai.HTTPOptions{RetryOptions: &genai.HTTPRetryOptions{Attempts: &modelAttempts}}})
	if e != nil {
		log.Fatal(e)
	}
	// List price of MODEL_NAME (Vertex AI standard, checked 2026-10-25).
	price, e := agent.ParsePrice(env("MODEL_INPUT_USD_PER_MTOK", "0.25"), env("MODEL_OUTPUT_USD_PER_MTOK", "1.50"))
	if e != nil {
		log.Fatal(e)
	}
	service := agent.New(store, model, env("MODEL_NAME", "gemini-3.1-flash-lite"), price, computer.Config{Project: project, Zone: os.Getenv("COMPUTER_ZONE"), Instance: env("COMPUTER_INSTANCE", "nori-computer")})
	mode := env("NORI_EXECUTION_MODE", "inline")
	if mode != "inline" && mode != "temporal" {
		log.Fatal("NORI_EXECUTION_MODE must be inline or temporal")
	}
	var temporalClient client.Client
	if mode == "temporal" {
		temporalClient, e = task.Dial(ctx)
		if e != nil {
			log.Fatal(e)
		}
		defer temporalClient.Close()
	}
	deliver := func(parent context.Context) {
		deliveryCtx, cancel := context.WithTimeout(parent, 2*time.Second)
		defer cancel()
		if e := task.Deliver(deliveryCtx, store, temporalClient); e != nil {
			log.Printf("command accepted; delivery pending repair: %v", e)
		}
	}
	if temporalClient != nil {
		go func() {
			if e := task.RepairClosed(ctx, store, temporalClient, service.Computer); e != nil {
				log.Printf("terminal recovery: %v", e)
			}
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				deliver(ctx)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	health := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok", "project": project, "database": database, "model_location": modelLocation})
	}
	// Native generic invocation forwards POST; this route performs no cloud reads.
	mux.HandleFunc("POST /api/healthz", health)
	mux.HandleFunc("POST /healthz", health)
	mux.HandleFunc("POST /api/resume", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !decode(w, r, &body) {
			return
		}
		if temporalClient == nil {
			writeJSON(w, 503, map[string]string{"error": "Temporal delivery is not configured"})
			return
		}
		if e := task.Deliver(r.Context(), store, temporalClient); e != nil {
			writeError(w, e)
			return
		}
		if e := task.Wake(r.Context(), store, temporalClient); e != nil {
			writeError(w, e)
			return
		}
		writeJSON(w, 200, map[string]bool{"resumed": true})
	})
	mux.HandleFunc("POST /api/session", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !decode(w, r, &body) {
			return
		}
		session, e := store.Session(r.Context())
		if e != nil {
			writeError(w, e)
			return
		}
		writeJSON(w, 200, session)
	})
	mux.HandleFunc("POST /api/run", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID      string `json:"id"`
			Message string `json:"message"`
		}
		if !decode(w, r, &body) {
			return
		}
		if !validID(body.ID) || strings.TrimSpace(body.Message) == "" || len(body.Message) > 8192 {
			writeJSON(w, 400, map[string]string{"error": "A UUID id and a message of 1–8192 bytes are required."})
			return
		}
		if temporalClient != nil {
			run, e := store.Accept(r.Context(), state.Command{ID: body.ID, RunID: body.ID, Kind: "start", Text: strings.TrimSpace(body.Message)})
			if e != nil {
				writeError(w, e)
				return
			}
			deliver(r.Context())
			writeJSON(w, http.StatusAccepted, map[string]any{"run": run})
			return
		}
		run, e := service.Run(r.Context(), body.ID, strings.TrimSpace(body.Message))
		if e != nil {
			writeError(w, e)
			return
		}
		writeJSON(w, 200, map[string]any{"run": run})
	})
	mux.HandleFunc("POST /api/cancel", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID string `json:"id"`
		}
		if !decode(w, r, &body) {
			return
		}
		if !validID(body.ID) {
			writeJSON(w, 400, map[string]string{"error": "id must be a UUID"})
			return
		}
		current, e := store.GetRun(r.Context(), body.ID)
		if e != nil {
			writeError(w, e)
			return
		}
		if current.Temporal {
			if temporalClient == nil {
				writeJSON(w, 503, map[string]string{"error": "Temporal delivery is not configured"})
				return
			}
			commandID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(body.ID+"-cancel")).String()
			run, e := store.Accept(r.Context(), state.Command{ID: commandID, RunID: body.ID, Kind: "cancel"})
			if e != nil {
				writeError(w, e)
				return
			}
			deliver(r.Context())
			// A paused task whose start was never delivered has no workflow to
			// wake the worker pool. Repair only proven closed/missing executions;
			// this path never starts a workflow or a retained computer.
			repairCtx, cancelRepair := context.WithTimeout(r.Context(), 5*time.Second)
			control, controlErr := store.Control(repairCtx)
			if controlErr == nil {
				if admissionErr := control.Check(); admissionErr != nil {
					if repairErr := task.RepairClosed(repairCtx, store, temporalClient, service.Computer); repairErr != nil {
						log.Printf("Stop terminal recovery pending: %v", repairErr)
					}
					if repaired, readErr := store.GetRun(repairCtx, run.ID); readErr == nil {
						run = repaired
					}
				}
			}
			cancelRepair()
			writeJSON(w, http.StatusAccepted, map[string]any{"run": run})
			return
		}
		run, e := service.Cancel(r.Context(), body.ID)
		if e != nil {
			writeError(w, e)
			return
		}
		writeJSON(w, 200, map[string]any{"run": run})
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
		if !validID(body.ID) || !validID(body.RunID) || strings.TrimSpace(body.Message) == "" || len(body.Message) > 8192 {
			writeJSON(w, 400, map[string]string{"error": "UUID id, run_id and message of 1–8192 bytes required"})
			return
		}
		if temporalClient == nil {
			writeJSON(w, 503, map[string]string{"error": "Temporal execution is not enabled"})
			return
		}
		run, e := store.Accept(r.Context(), state.Command{ID: body.ID, RunID: body.RunID, Kind: state.InputKind(body.Message), Text: strings.TrimSpace(body.Message)})
		if e != nil {
			writeError(w, e)
			return
		}
		deliver(r.Context())
		writeJSON(w, http.StatusAccepted, map[string]any{"run": run})
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
			writeJSON(w, 400, map[string]string{"error": "run_id and artifact_id must be UUIDs"})
			return
		}
		data, e := store.GetPNG(r.Context(), body.RunID, body.ArtifactID)
		if e != nil {
			writeError(w, e)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data)
	})
	// Identity is fixed in state.Owner. Deploy this listener behind runtime IAM;
	// only the private Next.js web service account may invoke it.
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 150 * time.Second, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	errCh := make(chan error, 1)
	go func() { log.Printf("Nori listening on %s", server.Addr); errCh <- server.ListenAndServe() }()
	select {
	case e := <-errCh:
		if !errors.Is(e, http.ErrServerClosed) {
			log.Fatal(e)
		}
	case <-ctx.Done():
		service.Shutdown()
		shutdown, done := context.WithTimeout(context.Background(), 25*time.Second)
		defer done()
		if e := server.Shutdown(shutdown); e != nil {
			log.Printf("shutdown: %v", e)
		}
	}
}
func validID(value string) bool {
	parsed, e := uuid.Parse(value)
	return e == nil && parsed.String() == value
}
func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(value); e != nil {
		writeJSON(w, 400, map[string]string{"error": "Invalid JSON request"})
		return false
	}
	var extra any
	if e := decoder.Decode(&extra); e != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "Only one JSON object is allowed"})
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, e error) {
	code := 500
	switch {
	case errors.Is(e, state.ErrPaused):
		code = 403
	case errors.Is(e, state.ErrBusy), errors.Is(e, state.ErrDuplicate):
		code = 409
	case errors.Is(e, state.ErrNotFound):
		code = 404
	}
	log.Printf("request failed: %v", e)
	text := e.Error()
	if code == 500 {
		text = "Nori could not finish this request. Check service configuration and logs."
	}
	writeJSON(w, code, map[string]string{"error": fmt.Sprint(text)})
}
