// nori-user provisions a user row. It never moves the user's retained records.
package main

import (
	"cmp"
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/users"
)

func main() {
	if err := run(); err != nil {
		slog.Error("nori-user failed", "err", err)
		os.Exit(1)
	}
}
func run() error {
	id := flag.String("id", "", "user UUID; owner is reserved for the existing user's data")
	email := flag.String("email", "", "sign-in email")
	name := flag.String("name", "", "display name")
	zone := flag.String("timezone", "UTC", "IANA time zone")
	flag.Parse()
	cfg, err := settings.Load()
	if err != nil {
		return err
	}
	row := users.User{ID: cmp.Or(*id, uuid.NewString()), Email: users.Email(*email), DisplayName: *name, TimeZone: *zone}
	if err := row.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	directory, err := platform.OpenUsers(ctx, cfg)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Create(ctx, row); err != nil {
		return err
	}
	slog.InfoContext(ctx, "saved user", "user_id", row.ID)
	return nil
}
