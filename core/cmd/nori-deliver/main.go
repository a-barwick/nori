// nori-deliver repairs pending delivery once during an authorized active session.
package main

import (
	"context"
	"log"
	"nori/internal/env"
	"nori/internal/observability"
	"nori/internal/platform"
	"nori/internal/task"
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
	wakes, e := env.WorkerWake(ctx)
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
	c, e := task.Dial(ctx)
	if e != nil {
		observability.Fatal(e)
	}
	defer c.Close()
	if e = task.RepairClosed(ctx, store, c, platform.Computer()); e != nil {
		observability.Fatal(e)
	}
	if e = task.Deliver(ctx, store, c, wakes); e != nil {
		observability.Fatal(e)
	}
	log.Print("pending command delivery acknowledged")
}
