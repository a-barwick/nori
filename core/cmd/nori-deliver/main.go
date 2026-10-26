// nori-deliver repairs pending delivery once during an authorized active session.
package main

import (
	"context"
	"github.com/joho/godotenv"
	"log"
	"nori/internal/computer"
	"nori/internal/state"
	"nori/internal/task"
	"os"
	"time"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func main() {
	if e := godotenv.Load(env("NORI_ENV_FILE", "../.env")); e != nil && !os.IsNotExist(e) {
		log.Fatal("could not load root .env")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, e := state.NewGoogle(ctx, env("NORI_PROJECT_ID", "bot-111549"), env("FIRESTORE_DATABASE_ID", "nori-poc"), os.Getenv("ARTIFACT_BUCKET"))
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
	config := computer.Config{Project: env("NORI_PROJECT_ID", "bot-111549"), Zone: os.Getenv("COMPUTER_ZONE"), Instance: env("COMPUTER_INSTANCE", "nori-computer")}
	if e = task.RepairClosed(ctx, store, c, config); e != nil {
		log.Fatal(e)
	}
	if e = task.Deliver(ctx, store, c); e != nil {
		log.Fatal(e)
	}
	log.Print("pending command delivery acknowledged")
}
