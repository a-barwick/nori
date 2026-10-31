// This executable exercises real Firestore transactions without model/provider
// calls. It accepts only unique, loopback-emulator fixture databases.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/uuid"
	"nori/internal/state"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	host, project, database := os.Getenv("FIRESTORE_EMULATOR_HOST"), os.Getenv("NORI_M4_STORAGE_PROJECT"), os.Getenv("NORI_M4_STORAGE_DATABASE")
	if !regexp.MustCompile(`^127\.0\.0\.1:\d+$`).MatchString(host) || !regexp.MustCompile(`^nori-m4-storage-[a-f0-9]{12}$`).MatchString(project) || !regexp.MustCompile(`^m4-storage-[a-f0-9]{12}$`).MatchString(database) {
		return errors.New("storage fixture requires a unique isolated loopback emulator database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store, err := state.NewGoogle(ctx, project, database, "unused-storage-fixture")
	if err != nil {
		return err
	}
	defer store.Close()
	id, other := uuid.NewString(), uuid.NewString()
	var task state.Run
	for _, runID := range []string{id, other} {
		r, err := store.Accept(ctx, state.Command{ID: runID, RunID: runID, Kind: "start", Text: "Fixture-only task; never delivered to a worker."})
		if err != nil {
			return err
		}
		if err = store.Incorporate(ctx, runID, r.Revision, r.Position); err != nil {
			return err
		}
		if runID == id {
			task = r
		}
	}
	report := map[string]bool{}
	first := []byte(" {\n  \"opaque\": \"PRIVATE_HISTORY_SENTINEL\", \"items\": [1,2] } ")
	second := []byte(`{"answer":"exact second block"}`)
	ref, err := store.AppendHistory(ctx, id, "", first)
	if err != nil {
		return err
	}
	duplicate, err := store.AppendHistory(ctx, id, "", first)
	if err != nil {
		return err
	}
	report["duplicate append retains immutable identity"] = duplicate == ref
	head, err := store.AppendHistory(ctx, id, ref, second)
	if err != nil {
		return err
	}
	blocks, err := store.ReadHistory(ctx, id, head)
	if err != nil {
		return err
	}
	report["history retains exact bytes in chronological order"] = len(blocks) == 2 && bytes.Equal(blocks[0], first) && bytes.Equal(blocks[1], second)
	_, err = store.ReadHistory(ctx, other, head)
	report["another task cannot resolve the same history reference"] = err != nil
	_, err = store.AppendHistory(ctx, other, ref, second)
	report["foreign parent cannot be appended"] = err != nil

	operationID := id + "-m1-t1"
	if err = store.Admit(ctx, id, task.Revision, operationID); err != nil {
		return err
	}
	op := state.OperationRecord{ID: operationID, CallID: "fixture-call", Name: "browser_navigate", Revision: task.Revision, EffectClass: "read", Outcome: "pending"}
	if err = store.PutOperation(ctx, id, op); err != nil {
		return err
	}
	result, err := store.AppendHistory(ctx, id, "", []byte(`{"title":"Example","operation_id":"fixture"}`))
	if err != nil {
		return err
	}
	op.Outcome, op.ResultRef, op.Attempt = "confirmed", result, 1
	if err = store.PutOperation(ctx, id, op); err != nil {
		return err
	}
	report["pending operation can record its first completed attempt"] = true
	if err = store.PutOperation(ctx, id, op); err != nil {
		return err
	}
	report["completed operation replay is idempotent"] = true
	changed := op
	changed.Outcome = "failed"
	report["terminal operation outcome cannot be replaced"] = store.PutOperation(ctx, id, changed) != nil
	operations, err := store.Operations(ctx, id)
	if err != nil {
		return err
	}
	report["authoritative operation is retained once"] = len(operations) == 1 && operations[0].Outcome == "confirmed" && operations[0].Attempt == 1

	check := state.CompletionCheck{ID: id + "-check1", Revision: task.Revision, Position: task.Position, Proposal: state.CompletionProposal{Answer: "Example observed", Evidence: []string{operationID}}}
	if err = store.PutCompletionCheck(ctx, id, check); err != nil {
		return err
	}
	check.JevAvailable, check.JevChoice, check.JevScore = true, "yes", .9
	if err = store.PutCompletionCheck(ctx, id, check); err != nil {
		return err
	}
	if err = store.PutCompletionCheck(ctx, id, check); err != nil {
		return err
	}
	report["completion check supports one final verification and exact replay"] = true
	check.Proposal.Answer = "Different claim"
	report["finalized completion proposal cannot be replaced"] = store.PutCompletionCheck(ctx, id, check) != nil
	boundaries, err := completionBoundaries(ctx, store, id, other, task, op)
	if err != nil {
		return err
	}
	for name, passed := range boundaries {
		report[name] = passed
	}
	if address := os.Getenv("NORI_M4_STORAGE_TEMPORAL_ADDRESS"); address != "" {
		checkpoint, err := checkpointBoundary(ctx, store, address)
		if err != nil {
			return err
		}
		for name, passed := range checkpoint {
			report[name] = passed
		}
	}

	// Reopen the client to prove reads depend on persisted records, not process
	// memory. Inspect admission metadata using the actual Firestore client.
	if err = os.Unsetenv("FIRESTORE_EMULATOR_HOST"); err != nil {
		return err
	}
	if err = os.Setenv("NORI_STATE_EMULATOR_HOST", host); err != nil {
		return err
	}
	reopened, err := state.NewGoogle(ctx, project, database, "unused-storage-fixture")
	_ = os.Unsetenv("NORI_STATE_EMULATOR_HOST")
	_ = os.Setenv("FIRESTORE_EMULATOR_HOST", host)
	if err != nil {
		return err
	}
	defer reopened.Close()
	blocks, err = reopened.ReadHistory(ctx, id, head)
	if err != nil {
		return err
	}
	report["new client restores exact saved history"] = len(blocks) == 2 && bytes.Equal(blocks[0], first)
	report["app-only emulator endpoint reopens retained evidence without shared emulator env"] = len(blocks) == 2 && bytes.Equal(blocks[1], second)
	db, err := firestore.NewClientWithDatabase(ctx, project, database)
	if err != nil {
		return err
	}
	defer db.Close()
	root := db.Collection("owners").Doc(state.Owner).Collection("runs").Doc(id)
	doc, err := root.Collection("operations").Doc(operationID).Get(ctx)
	if err != nil {
		return err
	}
	_, admitted := doc.Data()["admitted_at"]
	report["operation evidence preserves its admission receipt"] = admitted
	if _, err = root.Collection("history").Doc(ref).Update(ctx, []firestore.Update{{Path: "data", Value: []byte(`{"tampered":true}`)}}); err != nil {
		return err
	}
	_, err = reopened.ReadHistory(ctx, id, head)
	report["modified history fails closed rather than returning partial context"] = err != nil
	for name, passed := range report {
		if !passed {
			return fmt.Errorf("storage boundary failed: %s", name)
		}
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}
