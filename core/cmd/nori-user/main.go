// nori-user provisions a user row. It never moves the user's retained records.
package main

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/users"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	legacy := flag.Bool("import-legacy", false, "import the current user's settings from the selected root .env, then remove those settings from that file")
	id := flag.String("id", "", "user UUID; owner is reserved for the existing user's data")
	email := flag.String("email", "", "sign-in email")
	name := flag.String("name", "", "display name")
	zone := flag.String("timezone", "UTC", "IANA time zone")
	notify := flag.String("notification-email", "", "notification email; empty uses the calendar account; off disables email")
	computer := flag.String("computer-instance", "", "retained Compute Engine instance")
	computerZone := flag.String("computer-zone", "", "Compute Engine zone")
	local := flag.String("local-computer-url", "", "loopback development computer URL")
	flag.Parse()
	cfg, err := settings.Load()
	if err != nil {
		return err
	}
	row := users.User{ID: cmp.Or(*id, uuid.NewString()), Email: users.Email(*email), DisplayName: *name, TimeZone: *zone, NotifyEmail: *notify,
		Computer: users.Computer{Instance: *computer, Zone: *computerZone, LocalURL: *local}}
	if *legacy {
		env, err := godotenv.Read(cfg.EnvFile)
		if err != nil {
			return fmt.Errorf("could not read legacy .env")
		}
		row.ID = users.LegacyID
		row.Email = users.Email(cmp.Or(*email, env["OWNER_EMAIL"], users.LegacyOwner))
		row.TimeZone = cmp.Or(env["NORI_TIMEZONE"], "America/Los_Angeles")
		row.NotifyEmail = env["NORI_NOTIFY_EMAIL"]
		row.Calendar = users.Calendar{RefreshToken: env["GOOGLE_CALENDAR_REFRESH_TOKEN"], Account: env["GOOGLE_CALENDAR_ACCOUNT"], WriteID: env["GOOGLE_CALENDAR_WRITE_ID"]}
		row.Computer = users.Computer{Instance: cmp.Or(*computer, env["COMPUTER_INSTANCE"]), Zone: cmp.Or(*computerZone, env["COMPUTER_ZONE"]), LocalURL: cmp.Or(*local, env["NORI_LOCAL_COMPUTER_URL"])}
	}
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
	if *legacy {
		existing, err := directory.Get(ctx, users.LegacyID)
		if err == nil && existing != row {
			return fmt.Errorf("owner row already exists with different details; nothing changed")
		}
		if err != nil && !errors.Is(err, state.ErrNotFound) {
			return err
		}
		if errors.Is(err, state.ErrNotFound) {
			if err = directory.Create(ctx, row); err != nil {
				return err
			}
		}
		// Verify the durable row before removing its former settings.
		saved, err := directory.Get(ctx, row.ID)
		if err != nil {
			return err
		}
		if saved != row {
			return fmt.Errorf("user row did not match; legacy settings retained")
		}
		if err := removeLegacySettings(cfg.EnvFile); err != nil {
			return fmt.Errorf("user row saved, but old settings could not be removed: %w", err)
		}
	} else if err := directory.Create(ctx, row); err != nil {
		return err
	}
	fmt.Printf("Saved user %s.\n", row.ID)
	return nil
}

func removeLegacySettings(file string) error {
	path, err := filepath.EvalSymlinks(file)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	names := map[string]bool{"NORI_TIMEZONE": true, "NORI_NOTIFY_EMAIL": true, "GOOGLE_CALENDAR_REFRESH_TOKEN": true, "GOOGLE_CALENDAR_ACCOUNT": true, "GOOGLE_CALENDAR_WRITE_ID": true, "COMPUTER_INSTANCE": true, "COMPUTER_ZONE": true, "NORI_LOCAL_COMPUTER_URL": true}
	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		name, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(line), "export "), "=")
		if !names[strings.TrimSpace(name)] {
			lines = append(lines, line)
		}
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	next, err := os.CreateTemp(filepath.Dir(path), ".env-user-*")
	if err != nil {
		return err
	}
	defer os.Remove(next.Name())
	if err = next.Chmod(0600); err == nil {
		_, err = next.WriteString(strings.Join(lines, "\n") + "\n")
	}
	if err = errors.Join(err, next.Close()); err != nil {
		return err
	}
	return os.Rename(next.Name(), path)
}
