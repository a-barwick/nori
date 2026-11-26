package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/users"
)

func main() {
	if err := runCLI(context.Background(), os.Args[1:], openDefaultUsers, os.Stdout); err != nil {
		slog.Error("nori-user failed", "err", err)
		os.Exit(1)
	}
}

func openDefaultUsers(ctx context.Context) (users.Directory, error) {
	cfg, err := settings.Load()
	if err != nil {
		return nil, err
	}
	return platform.OpenUsers(ctx, cfg)
}

func runCLI(ctx context.Context, args []string, openUsers func(context.Context) (users.Directory, error), out io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "invite":
			return runInviteCLI(ctx, args[1:], openUsers, out)
		case "bind-identity":
			return runBindIdentity(ctx, args[1:], openUsers, out)
		}
	}
	return runCreateUser(ctx, args, openUsers)
}

func runBindIdentity(ctx context.Context, args []string, openUsers func(context.Context) (users.Directory, error), out io.Writer) error {
	fs := flag.NewFlagSet("nori-user bind-identity", flag.ContinueOnError)
	id := fs.String("id", "", "existing Nori user ID")
	issuer := fs.String("issuer", "", "verified identity issuer")
	subject := fs.String("subject", "", "verified identity subject")
	if err := fs.Parse(args); err != nil {
		return err
	}
	identity := users.Identity{Issuer: *issuer, Subject: *subject}
	if !users.ValidID(*id) || identity.Validate() != nil || fs.NArg() != 0 {
		return errors.New("usage: nori-user bind-identity --id ID --issuer ISSUER --subject SUBJECT")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	directory, err := openUsers(ctx)
	if err != nil {
		return err
	}
	defer directory.Close()
	store, ok := directory.(users.IdentityDirectory)
	if !ok {
		return errors.New("configured user directory does not support identity binding")
	}
	if _, err := store.BindIdentity(ctx, *id, identity); err != nil {
		return err
	}
	fmt.Fprintf(out, "Bound sign-in identity for %s\n", *id)
	return nil
}

func runCreateUser(ctx context.Context, args []string, openUsers func(context.Context) (users.Directory, error)) error {
	fs := flag.NewFlagSet("nori-user", flag.ContinueOnError)
	id := fs.String("id", "", "user UUID; owner is reserved for the existing user's data")
	email := fs.String("email", "", "sign-in email")
	name := fs.String("name", "", "display name")
	zone := fs.String("timezone", "UTC", "IANA time zone")
	if err := fs.Parse(args); err != nil {
		return err
	}
	row := users.User{
		ID:          cmp.Or(*id, uuid.NewString()),
		Email:       users.Email(*email),
		DisplayName: *name,
		TimeZone:    *zone,
	}
	if err := row.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	directory, err := openUsers(ctx)
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

func runInviteCLI(ctx context.Context, args []string, openUsers func(context.Context) (users.Directory, error), out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: nori-user invite <create|list|revoke> [flags]")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	directory, err := openUsers(ctx)
	if err != nil {
		return err
	}
	defer directory.Close()
	store, ok := directory.(users.InviteStore)
	if !ok {
		return errors.New("configured user directory does not support invite codes")
	}
	switch args[0] {
	case "create":
		return runInviteCreate(ctx, args[1:], store, out)
	case "list":
		return runInviteList(ctx, args[1:], store, out)
	case "revoke":
		return runInviteRevoke(ctx, args[1:], store, out)
	default:
		return fmt.Errorf("unknown invite subcommand %q (want create, list, or revoke)", args[0])
	}
}

func runInviteCreate(ctx context.Context, args []string, store users.InviteStore, out io.Writer) error {
	fs := flag.NewFlagSet("nori-user invite create", flag.ContinueOnError)
	code := fs.String("code", "", "custom invite code (optional; random code generated if omitted)")
	maxUses := fs.Int("max-uses", 1, "maximum number of redemptions")
	email := fs.String("email", "", "restrict invite to a single email address")
	domain := fs.String("domain", "", "restrict invite to an email domain")
	expiresIn := fs.Duration("expires", 0, "optional expiration duration (e.g. 72h)")
	note := fs.String("note", "", "optional admin note")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var expiresAt *time.Time
	if *expiresIn > 0 {
		t := time.Now().UTC().Add(*expiresIn)
		expiresAt = &t
	} else if *expiresIn < 0 {
		return errors.New("-expires must be positive")
	}
	inv, err := store.CreateInvite(ctx, users.CreateInviteOptions{
		Code:      *code,
		MaxUses:   *maxUses,
		Email:     *email,
		Domain:    *domain,
		Note:      *note,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s\n", inv.Code)
	return nil
}

func runInviteList(ctx context.Context, args []string, store users.InviteStore, out io.Writer) error {
	fs := flag.NewFlagSet("nori-user invite list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	invites, err := store.ListInvites(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "CODE\tSTATUS\tUSES\tRESTRICTION\tEXPIRES\tNOTE")
	now := time.Now().UTC()
	for _, inv := range invites {
		restriction := "-"
		switch {
		case inv.Email != "" && inv.Domain != "":
			restriction = inv.Email + " (@" + inv.Domain + ")"
		case inv.Email != "":
			restriction = inv.Email
		case inv.Domain != "":
			restriction = "@" + inv.Domain
		}
		expires := "never"
		if inv.ExpiresAt != nil {
			expires = inv.ExpiresAt.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(
			tw,
			"%s\t%s\t%d/%d\t%s\t%s\t%s\n",
			inv.Code,
			inv.Status(now),
			inv.UseCount,
			inv.MaxUses,
			restriction,
			expires,
			cmp.Or(inv.Note, "-"),
		)
	}
	return tw.Flush()
}

func runInviteRevoke(ctx context.Context, args []string, store users.InviteStore, out io.Writer) error {
	fs := flag.NewFlagSet("nori-user invite revoke", flag.ContinueOnError)
	code := fs.String("code", "", "invite code to revoke")
	if err := fs.Parse(args); err != nil {
		return err
	}
	target := strings.TrimSpace(*code)
	if target == "" && fs.NArg() > 0 {
		target = strings.TrimSpace(fs.Arg(0))
	}
	if target == "" {
		return errors.New("invite code is required (-code <code> or positional argument)")
	}
	inv, err := store.RevokeInvite(ctx, target)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "revoked %s\n", inv.Code)
	return nil
}
