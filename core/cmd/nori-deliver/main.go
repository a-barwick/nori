// nori-deliver repairs pending delivery once during an authorized active session.
package main

import (
	"context"
	"log"
	"nori/internal/computer"
	"nori/internal/env"
	"nori/internal/observability"
	"nori/internal/state"
	"nori/internal/task"
	"os"
	"time"
)

func main() {
	if e := env.Load(); e != nil {
		log.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stopTelemetry := observability.Init(ctx, "nori-deliver")
	defer observability.Shutdown(stopTelemetry)
	store, e := state.NewGoogle(ctx, env.Get("NORI_PROJECT_ID", "bot-111549"), env.Get("FIRESTORE_DATABASE_ID", "nori-poc"), os.Getenv("ARTIFACT_BUCKET"))
	if e != nil {
		log.Fatal(e)
	}
	defer store.Close()
	control, e := store.Control(ctx)
	if e != nil {
		log.Fatal(e)
	}
	if e = control.Check(); e != nil {
		log.Fatal(e)
	}
	c, e := task.Dial(ctx)
	if e != nil {
		log.Fatal(e)
	}
	defer c.Close()
	config := computer.Config{Project: env.Get("NORI_PROJECT_ID", "bot-111549"), Zone: os.Getenv("COMPUTER_ZONE"), Instance: env.Get("COMPUTER_INSTANCE", "nori-computer")}
	if e = task.RepairClosed(ctx, store, c, config); e != nil {
		log.Fatal(e)
	}
	if e = task.Deliver(ctx, store, c); e != nil {
		log.Fatal(e)
	}
	log.Print("pending command delivery acknowledged")
}
