// nori-user provisions a user row. It never moves the user's retained records.
package main

import (
	"cmp"
	"context"
	"flag"
	"log"
	"time"

	"github.com/google/uuid"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/users"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	id := flag.String("id", "", "user UUID; owner is reserved for the existing user's data")
	email := flag.String("email", "", "sign-in email")
	name := flag.String("name", "", "display name")
	zone := flag.String("timezone", "UTC", "IANA time zone")
	computer := flag.String("computer-instance", "", "retained Compute Engine instance")
	computerZone := flag.String("computer-zone", "", "Compute Engine zone")
	local := flag.String("local-computer-url", "", "loopback development computer URL")
	flag.Parse()
	cfg, err := settings.Load()
	if err != nil {
		return err
	}
	row := users.User{ID: cmp.Or(*id, uuid.NewString()), Email: users.Email(*email), DisplayName: *name, TimeZone: *zone,
		Computer: users.Computer{Instance: *computer, Zone: *computerZone, LocalURL: *local}}
	if err := row.Validate(); err != nil {
		return err
	}
	if row.Computer.LocalURL != "" {
		if err := platform.CheckComputer(platform.ForUser(cfg, row)); err != nil {
			return err
		}
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
	log.Printf("Saved user %s.", row.ID)
	return nil
}
