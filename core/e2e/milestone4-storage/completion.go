package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/genai"
	"google.golang.org/protobuf/proto"
	"nori/internal/observability"
	"nori/internal/state"
	"nori/internal/task"
)

type telemetryPacket struct {
	destination, path string
	body              []byte
}

// All collectors and credentials here are disposable local fixture values. The
// real exporter/filter code runs, but no telemetry leaves the loopback servers.
func completionBoundaries(ctx context.Context, store *state.Google, id, other string, run state.Run, operation state.OperationRecord) (map[string]bool, error) {
	var mu sync.Mutex
	packets := []telemetryPacket{}
	handler := func(destination string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var reader io.Reader = io.LimitReader(r.Body, 4<<20)
			if r.Header.Get("Content-Encoding") == "gzip" {
				compressed, err := gzip.NewReader(reader)
				if err != nil {
					http.Error(w, "invalid compression", 400)
					return
				}
				defer compressed.Close()
				reader = compressed
			}
			body, err := io.ReadAll(io.LimitReader(reader, 4<<20))
			if err != nil {
				http.Error(w, "invalid export", 400)
				return
			}
			mu.Lock()
			packets = append(packets, telemetryPacket{destination, r.URL.Path, body})
			mu.Unlock()
			if strings.HasSuffix(r.URL.Path, "/envelope/") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("{}"))
			} else {
				w.Header().Set("Content-Type", "application/x-protobuf")
			}
		})
	}
	sentry := httptest.NewServer(handler("sentry"))
	defer sentry.Close()
	arize := httptest.NewTLSServer(handler("arize"))
	defer arize.Close()
	certificate := filepath.Join(os.Getenv("NORI_LOCAL_ARTIFACT_DIR"), "collector.pem")
	certificates := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: arize.Certificate().Raw})
	if err := os.WriteFile(certificate, certificates, 0600); err != nil {
		return nil, err
	}
	for key, value := range map[string]string{
		"SENTRY_DSN": strings.Replace(sentry.URL, "://", "://fixture@", 1) + "/1", "SENTRY_TRACES_SAMPLE_RATE": "1", "SENTRY_ENVIRONMENT": "e2e",
		"NORI_SENTRY_SPANS_PER_HOUR": "1000", "NORI_SENTRY_ERRORS_PER_HOUR": "100",
		"ARIZE_API_KEY": "local-fixture-key", "ARIZE_SPACE_ID": "local-fixture-space", "ARIZE_OTLP_ENDPOINT": arize.URL + "/v1/traces",
		"ARIZE_OTLP_CERTIFICATE": certificate, "NORI_ARIZE_CAPTURE_CONTENT": "true",
	} {
		if err := os.Setenv(key, value); err != nil {
			return nil, err
		}
	}
	shutdown := observability.Init(ctx, "nori-m4-unpaid-boundary")
	flushed := false
	defer func() {
		if !flushed {
			observability.Shutdown(shutdown)
		}
	}()
	a := &task.Activities{Store: store} // Missing Jev key is the real unavailable path.
	report := map[string]bool{}
	synced, err := a.SyncHistory(ctx, task.HistoryInput{Step: task.Step{RunID: id, Revision: run.Revision}})
	if err != nil {
		return nil, err
	}
	report["history activity returns only a compact immutable reference"] = len(synced.Ref) == 64 && synced.Position == run.Position
	makeProposal := func(runID, callID string, evidence, unmet []any, extra []*genai.Content) (string, error) {
		contents := append([]*genai.Content{}, extra...)
		contents = append(contents, &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: callID, Name: "complete", Args: map[string]any{"answer": "PRIVATE_HISTORY_SENTINEL: observed result", "evidence": evidence, "unmet": unmet}}}}})
		data, e := json.Marshal(contents)
		if e != nil {
			return "", e
		}
		return store.AppendHistory(ctx, runID, "", data)
	}
	check := func(label, ref string, corrections int) (task.CompletionOutcome, error) {
		return a.CheckCompletion(ctx, task.CompletionInput{Step: task.Step{RunID: id, Revision: run.Revision}, Ref: ref, ID: id + "-boundary-" + label, Position: run.Position, CorrectionAttempt: corrections})
	}
	valid, err := makeProposal(id, "complete-valid", []any{operation.ID}, []any{}, nil)
	if err != nil {
		return nil, err
	}
	unavailable, err := check("unavailable", valid, 0)
	if err != nil {
		return nil, err
	}
	report["missing Jev key produces unverified outcome rather than completion"] = unavailable.Action == "unverified" && strings.Contains(unavailable.Detail, "not completed")
	invalid, err := makeProposal(id, "complete-invalid", []any{"../../not-evidence"}, []any{}, nil)
	if err != nil {
		return nil, err
	}
	rejected, err := check("invalid", invalid, 0)
	if err != nil {
		return nil, err
	}
	report["invalid evidence is recorded and rejected before verifier I/O"] = rejected.Action == "correct"
	foreign, err := store.AppendHistory(ctx, other, "", []byte(`{"foreign":true}`))
	if err != nil {
		return nil, err
	}
	foreignProposal, err := makeProposal(id, "complete-foreign", []any{foreign}, []any{}, nil)
	if err != nil {
		return nil, err
	}
	rejected, err = check("foreign", foreignProposal, 0)
	if err != nil {
		return nil, err
	}
	report["valid hash from another task cannot ground completion"] = rejected.Action == "correct"
	unpaired := []*genai.Content{{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "unpaired-read", Name: "browser_navigate", Args: map[string]any{"url": "https://example.org"}}}}}}
	missing, err := makeProposal(id, "complete-unpaired", []any{operation.ID}, []any{}, unpaired)
	if err != nil {
		return nil, err
	}
	rejected, err = check("unpaired", missing, 0)
	if err != nil {
		return nil, err
	}
	report["unpaired tool call prevents completion"] = rejected.Action == "correct"
	partial, err := makeProposal(id, "complete-unmet", []any{operation.ID}, []any{"The requested verification is missing."}, nil)
	if err != nil {
		return nil, err
	}
	exhausted, err := check("exhausted", partial, 2)
	if err != nil {
		return nil, err
	}
	report["two read-only correction attempts end with truthful partial outcome"] = exhausted.Action == "partial" && strings.Contains(exhausted.Detail, "not completed")
	write := state.OperationRecord{ID: id + "-uncertain-write", CallID: "unresolved-write", Name: "shell", Revision: run.Revision, EffectClass: "unknown", Outcome: "uncertain", Attempt: 1}
	if err = store.PutOperation(ctx, id, write); err != nil {
		return nil, err
	}
	ask, err := check("uncertain-write", valid, 0)
	if err != nil {
		return nil, err
	}
	report["unresolved write asks the user without executing another operation"] = ask.Action == "ask"
	operations, err := store.Operations(ctx, id)
	if err != nil {
		return nil, err
	}
	report["completion checks add no execution operations"] = len(operations) == 2
	observability.Shutdown(shutdown)
	flushed = true
	mu.Lock()
	saved := append([]telemetryPacket{}, packets...)
	mu.Unlock()
	names := map[string]map[string]bool{"sentry": {}, "arize": {}}
	traces := map[string]map[string]bool{"sentry": {}, "arize": {}}
	private := false
	errorExport := false
	truthfulAttrs := false
	for _, packet := range saved {
		private = private || bytes.Contains(packet.body, []byte("PRIVATE_HISTORY_SENTINEL"))
		if packet.destination == "sentry" && strings.HasSuffix(packet.path, "/envelope/") && bytes.Contains(packet.body, []byte(`"exception"`)) {
			errorExport = true
		}
		if !strings.Contains(packet.path, "traces") {
			continue
		}
		var exported collectortrace.ExportTraceServiceRequest
		if proto.Unmarshal(packet.body, &exported) != nil {
			return nil, errors.New("collector received invalid OTLP trace export")
		}
		for _, resource := range exported.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				for _, span := range scope.Spans {
					names[packet.destination][span.Name] = true
					if span.Name == "task.completion.check" {
						traces[packet.destination][string(span.TraceId)] = true
						for _, attr := range span.Attributes {
							if attr.Key == "nori.completion.verified" && !attr.Value.GetBoolValue() {
								truthfulAttrs = true
							}
						}
					}
				}
			}
		}
	}
	for _, destination := range []string{"sentry", "arize"} {
		report[destination+" collector receives history and completion spans"] = names[destination]["task.history.sync"] && names[destination]["task.completion.check"] && names[destination]["completion jev-latest"]
	}
	correlated := false
	for traceID := range traces["sentry"] {
		correlated = correlated || traces["arize"][traceID]
	}
	report["Sentry and Arize share actual completion trace identity"] = correlated
	report["unavailable verification exports false completion evidence"] = truthfulAttrs
	report["verifier failure reaches local Sentry error collector"] = errorExport
	report["opaque task history is absent from both telemetry exports"] = !private
	return report, nil
}
