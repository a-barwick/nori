// nori-deliver repairs pending delivery once during an authorized active session.
package main

import (
	"context"
	"log"
	"time"

	"nori/internal/dispatch"
	"nori/internal/observability"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/users"
	"nori/internal/wake"
)

func main() {
	s, e := settings.Load()
	if e != nil {
		log.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stopTelemetry := observability.Init(ctx, "nori-deliver", s)
	defer observability.Shutdown(stopTelemetry)
	waker, e := platform.Waker(ctx, s, false)
	if e != nil {
		observability.Fatal(e)
	}
	directory, e := platform.OpenUsers(ctx, s)
	if e != nil {
		observability.Fatal(e)
	}
	defer directory.Close()
	rows, e := directory.List(ctx)
	if e != nil {
		observability.Fatal(e)
	}
	for _, row := range rows {
		if e = deliver(ctx, s, waker, row); e != nil {
			observability.Fatal(e)
		}
	}
	log.Print("pending command delivery acknowledged")
}
func deliver(ctx context.Context, s settings.Settings, waker wake.Waker, row users.User) error {
	s = platform.ForUser(s, row)
	store, e := platform.OpenStore(ctx, s, row)
	if e != nil {
		return e
	}
	defer store.Close()
	control, e := store.Control(ctx)
	if e != nil {
		return e
	}
	if e = control.Check(); e != nil {
		return e
	}
	engine, e := platform.Engine(ctx, s, row.ID)
	if e != nil {
		return e
	}
	defer engine.Close()
	dispatcher := platform.Dispatcher(s, store, engine, waker, row.ID)
	if e = dispatcher.Reconcile(ctx, dispatch.RepairClosed); e != nil {
		return e
	}
	return dispatcher.Reconcile(ctx, dispatch.Commands)
}
