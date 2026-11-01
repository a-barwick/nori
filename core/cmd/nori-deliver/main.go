// nori-deliver repairs pending delivery once during an authorized active session.
package main

import (
	"context"
	"log"
	"time"

	"nori/internal/dispatch"
	"nori/internal/env"
	"nori/internal/observability"
	"nori/internal/platform"
)

func main() {
	if e := env.Load(); e != nil {
		log.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stopTelemetry := observability.Init(ctx, "nori-deliver")
	defer observability.Shutdown(stopTelemetry)
	waker, e := platform.Waker(ctx, false)
	if e != nil {
		observability.Fatal(e)
	}
	store, e := platform.OpenStore(ctx)
	if e != nil {
		observability.Fatal(e)
	}
	defer store.Close()
	control, e := store.Control(ctx)
	if e != nil {
		observability.Fatal(e)
	}
	if e = control.Check(); e != nil {
		observability.Fatal(e)
	}
	engine, e := platform.Engine(ctx)
	if e != nil {
		observability.Fatal(e)
	}
	defer engine.Close()
	dispatcher := platform.Dispatcher(store, engine, waker)
	// Repair first, and deliver nothing if it fails.
	if e = dispatcher.Reconcile(ctx, dispatch.RepairClosed); e != nil {
		observability.Fatal(e)
	}
	if e = dispatcher.Reconcile(ctx, dispatch.Commands); e != nil {
		observability.Fatal(e)
	}
	log.Print("pending command delivery acknowledged")
}
