package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"nori/internal/billing"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/usage"
	"nori/internal/users"
)

type cliEnv struct {
	Users           users.Directory
	Billing         billing.Store
	ReadUsageCosts  func(ctx context.Context, user users.User, start, end time.Time, visit func(docstore.UsageCostRecord)) error
	GetStorageBytes func(ctx context.Context, user users.User) (int64, error)
	Now             func() time.Time
	Close           func()
}

func main() {
	if err := runCLIWithEnv(context.Background(), os.Args[1:], openDefaultUsers, openDefaultEnv, os.Stdout); err != nil {
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

func openDefaultEnv(ctx context.Context) (*cliEnv, error) {
	cfg, err := settings.Load()
	if err != nil {
		return nil, err
	}
	storage, err := platform.OpenStorage(ctx, cfg)
	if err != nil {
		return nil, err
	}
	dir := storage.Users()
	if setter, ok := dir.(users.TrialCapSetter); ok {
		setter.SetTrialMaxSignupsPerDay(cfg.TrialMaxSignupsPerDay)
	}
	bs := storage.BillingStore()
	if bs == nil {
		_ = storage.Close()
		return nil, errors.New("configured backend does not support billing entitlements")
	}
	return &cliEnv{
		Users:   dir,
		Billing: bs,
		ReadUsageCosts: func(ctx context.Context, user users.User, start, end time.Time, visit func(docstore.UsageCostRecord)) error {
			db := records.NewShared(storage.Backend(), user, cfg.TaskBudgetUSD)
			return db.ReadUsageCosts(ctx, start, end, visit)
		},
		GetStorageBytes: func(ctx context.Context, user users.User) (int64, error) {
			return storage.GetUserStorageBytes(ctx, user)
		},
		Now:   func() time.Time { return time.Now().UTC() },
		Close: func() { _ = storage.Close() },
	}, nil
}

func runCLI(ctx context.Context, args []string, openUsers func(context.Context) (users.Directory, error), out io.Writer) error {
	return runCLIWithEnv(ctx, args, openUsers, openDefaultEnv, out)
}

func runCLIWithEnv(
	ctx context.Context,
	args []string,
	openUsers func(context.Context) (users.Directory, error),
	openEnv func(context.Context) (*cliEnv, error),
	out io.Writer,
) error {
	if len(args) > 0 {
		switch args[0] {
		case "invite":
			return runInviteCLI(ctx, args[1:], openUsers, out)
		case "bind-identity":
			return runBindIdentity(ctx, args[1:], openUsers, out)
		case "set-tier":
			return runSetTier(ctx, args[1:], openEnv, out)
		case "suspend":
			return runSuspend(ctx, args[1:], openEnv, out)
		case "unsuspend":
			return runUnsuspend(ctx, args[1:], openEnv, out)
		case "usage":
			return runUsage(ctx, args[1:], openEnv, out)
		case "backfill-entitlements":
			return runBackfillEntitlements(ctx, args[1:], openEnv, out)
		}
	}
	return runCreateUser(ctx, args, openUsers)
}

func resolveTargetUser(ctx context.Context, dir users.Directory, email, userID string) (users.User, error) {
	email = strings.TrimSpace(email)
	userID = strings.TrimSpace(userID)
	if (email == "" && userID == "") || (email != "" && userID != "") {
		return users.User{}, errors.New("specify exactly one of --email or --user-id")
	}
	if userID != "" {
		return dir.Get(ctx, userID)
	}
	return dir.FindEmail(ctx, email)
}

func runSetTier(ctx context.Context, args []string, openEnv func(context.Context) (*cliEnv, error), out io.Writer) error {
	fs := flag.NewFlagSet("nori-user set-tier", flag.ContinueOnError)
	fs.SetOutput(out)
	email := fs.String("email", "", "user sign-in email (mutually exclusive with --user-id)")
	userID := fs.String("user-id", "", "user ID (mutually exclusive with --email)")
	tierRaw := fs.String("tier", "", "target tier: trial, byok, standard, or power")
	creditOverride := fs.Float64("credit-override-usd", math.NaN(), "optional explicit hosted credit grant override in USD (e.g. 0 or 50); resets a fresh period grant")
	clearOverride := fs.Bool("clear-credit-override", false, "clear any previous credit override and restore tier default")
	pending := fs.Bool("pending", false, "schedule paid tier change for next period_end instead of switching immediately")
	fs.Usage = func() {
		fmt.Fprintln(out, "Usage: nori-user set-tier (--email <email> | --user-id <id>) --tier <trial|byok|standard|power> [--credit-override-usd <float>] [--clear-credit-override] [--pending]")
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "Note: Setting or re-setting a paid tier immediately closes any prior active period grant,")
		fmt.Fprintln(out, "grants a fresh full-period credit grant, and triggers pg_notify('nori_wake', 'entitlement:<uid>').")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	tier, ok := billing.ParseTier(*tierRaw)
	if !ok {
		return fmt.Errorf("invalid --tier %q (want trial, byok, standard, or power)", *tierRaw)
	}

	opts := billing.SetTierOptions{
		Pending:             *pending,
		ClearCreditOverride: *clearOverride,
	}
	overrideProvided := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "credit-override-usd" {
			overrideProvided = true
		}
	})
	if overrideProvided {
		if math.IsNaN(*creditOverride) || math.IsInf(*creditOverride, 0) || *creditOverride < 0 {
			return errors.New("--credit-override-usd must be a non-negative number")
		}
		micros := billing.USDToMicros(*creditOverride)
		opts.CreditOverrideMicros = &micros
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	env, err := openEnv(ctx)
	if err != nil {
		return err
	}
	if env.Close != nil {
		defer env.Close()
	}

	user, err := resolveTargetUser(ctx, env.Users, *email, *userID)
	if err != nil {
		return fmt.Errorf("resolve user: %w", err)
	}
	ent, err := env.Billing.SetTier(ctx, user.ID, tier, opts)
	if err != nil {
		return fmt.Errorf("set tier: %w", err)
	}
	bal, _ := env.Billing.CreditBalance(ctx, user.ID)

	periodEnd := "none"
	if ent.PeriodEnd != nil {
		periodEnd = ent.PeriodEnd.UTC().Format(time.RFC3339)
	}
	pendingStr := "-"
	if ent.PendingTier != nil {
		pendingStr = string(*ent.PendingTier)
	}
	fmt.Fprintf(out, "Updated user %s (%s): tier=%s pending_tier=%s status=%s period_end=%s hosted_grant=$%.2f remaining=$%.2f\n",
		user.ID, user.Email, ent.Tier, pendingStr, ent.Status, periodEnd,
		billing.MicrosToUSD(bal.GrantedMicros), billing.MicrosToUSD(max(bal.AvailableMicros, 0)),
	)
	return nil
}

func runSuspend(ctx context.Context, args []string, openEnv func(context.Context) (*cliEnv, error), out io.Writer) error {
	fs := flag.NewFlagSet("nori-user suspend", flag.ContinueOnError)
	fs.SetOutput(out)
	email := fs.String("email", "", "user sign-in email (mutually exclusive with --user-id)")
	userID := fs.String("user-id", "", "user ID (mutually exclusive with --email)")
	reason := fs.String("reason", "", "human-readable suspension reason (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	trimmedReason := strings.TrimSpace(*reason)
	if trimmedReason == "" {
		return errors.New("usage: nori-user suspend (--email <email> | --user-id <id>) --reason <reason>")
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	env, err := openEnv(ctx)
	if err != nil {
		return err
	}
	if env.Close != nil {
		defer env.Close()
	}

	user, err := resolveTargetUser(ctx, env.Users, *email, *userID)
	if err != nil {
		return fmt.Errorf("resolve user: %w", err)
	}
	ent, err := env.Billing.Suspend(ctx, user.ID, trimmedReason)
	if err != nil {
		return fmt.Errorf("suspend user: %w", err)
	}
	fmt.Fprintf(out, "Suspended user %s (%s) at %s: %s\n",
		user.ID, user.Email, ent.SuspendedAt.UTC().Format(time.RFC3339), ent.SuspendedReason)
	return nil
}

func runUnsuspend(ctx context.Context, args []string, openEnv func(context.Context) (*cliEnv, error), out io.Writer) error {
	fs := flag.NewFlagSet("nori-user unsuspend", flag.ContinueOnError)
	fs.SetOutput(out)
	email := fs.String("email", "", "user sign-in email (mutually exclusive with --user-id)")
	userID := fs.String("user-id", "", "user ID (mutually exclusive with --email)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	env, err := openEnv(ctx)
	if err != nil {
		return err
	}
	if env.Close != nil {
		defer env.Close()
	}

	user, err := resolveTargetUser(ctx, env.Users, *email, *userID)
	if err != nil {
		return fmt.Errorf("resolve user: %w", err)
	}
	ent, err := env.Billing.Unsuspend(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("unsuspend user: %w", err)
	}
	fmt.Fprintf(out, "Unsuspended user %s (%s): tier=%s status=%s\n",
		user.ID, user.Email, ent.Tier, ent.Status)
	return nil
}

func runUsage(ctx context.Context, args []string, openEnv func(context.Context) (*cliEnv, error), out io.Writer) error {
	fs := flag.NewFlagSet("nori-user usage", flag.ContinueOnError)
	fs.SetOutput(out)
	email := fs.String("email", "", "optional user sign-in email")
	userID := fs.String("user-id", "", "optional user ID")
	days := fs.Int("days", 7, "lookback window in days (1..365)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *days < 1 || *days > 365 {
		return errors.New("--days must be between 1 and 365")
	}
	if strings.TrimSpace(*email) != "" && strings.TrimSpace(*userID) != "" {
		return errors.New("specify at most one of --email or --user-id")
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	env, err := openEnv(ctx)
	if err != nil {
		return err
	}
	if env.Close != nil {
		defer env.Close()
	}
	now := time.Now().UTC()
	if env.Now != nil {
		now = env.Now().UTC()
	}

	var targets []users.User
	if strings.TrimSpace(*email) != "" || strings.TrimSpace(*userID) != "" {
		u, err := resolveTargetUser(ctx, env.Users, *email, *userID)
		if err != nil {
			return fmt.Errorf("resolve user: %w", err)
		}
		targets = []users.User{u}
	} else {
		list, err := env.Users.List(ctx)
		if err != nil {
			return fmt.Errorf("list users: %w", err)
		}
		targets = list
	}

	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "USER_ID\tEMAIL\tTIER\tSTATUS\tSUSPENDED\tCREDIT_REM/GRANT\tHOSTED_%dd\tBYOK_%dd\tLLM/SEARCH/COMP\tSTORAGE_MB\n", *days, *days)

	var fleetHosted, fleetBYOK, fleetLocal float64
	var fleetLLM, fleetSearch, fleetComputer float64
	var suspendedCount, missingEntCount int

	for _, u := range targets {
		tierStr, statusStr, suspStr := "MISSING", "-", "no"
		var remUSD, grantUSD float64
		ent, err := env.Billing.GetEntitlement(ctx, u.ID)
		if errors.Is(err, billing.ErrEntitlementMissing) {
			missingEntCount++
		} else if err != nil {
			return fmt.Errorf("get entitlement for %s: %w", u.ID, err)
		} else {
			tierStr = string(ent.Tier)
			statusStr = string(ent.Status)
			if ent.IsSuspended() {
				suspStr = "yes"
				suspendedCount++
			}
			if bal, bErr := env.Billing.CreditBalance(ctx, u.ID); bErr == nil {
				grantUSD = billing.MicrosToUSD(bal.GrantedMicros)
				remUSD = billing.MicrosToUSD(max(bal.AvailableMicros, 0))
			}
		}

		var report usage.CostReport
		if env.ReadUsageCosts != nil {
			rep, rErr := usage.ReadCostReport(ctx, func(ctx context.Context, start, end time.Time, visit func(docstore.UsageCostRecord)) error {
				return env.ReadUsageCosts(ctx, u, start, end, visit)
			}, *days, time.UTC, now)
			if rErr != nil {
				return fmt.Errorf("read usage costs for %s: %w", u.ID, rErr)
			}
			report = rep
		}
		var storageBytes int64
		if env.GetStorageBytes != nil {
			storageBytes, _ = env.GetStorageBytes(ctx, u)
		}

		fleetHosted += report.ByBilling.Hosted.CostUSD
		fleetBYOK += report.ByBilling.BYOK.CostUSD
		fleetLocal += report.ByBilling.Local.CostUSD
		fleetLLM += report.ByCategory.LLM.CostUSD
		fleetSearch += report.ByCategory.Search.CostUSD
		fleetComputer += report.ByCategory.Computer.CostUSD

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t$%.2f/$%.2f\t$%.4f\t$%.4f\t$%.3f/$%.3f/$%.3f\t%.2f\n",
			u.ID, u.Email, tierStr, statusStr, suspStr,
			remUSD, grantUSD,
			report.ByBilling.Hosted.CostUSD,
			report.ByBilling.BYOK.CostUSD,
			report.ByCategory.LLM.CostUSD,
			report.ByCategory.Search.CostUSD,
			report.ByCategory.Computer.CostUSD,
			float64(storageBytes)/(1024*1024),
		)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	fmt.Fprintf(out, "\nFleet summary (%dd): users=%d suspended=%d missing_entitlement=%d hosted=$%.4f byok=$%.4f local=$%.4f (llm=$%.4f search=$%.4f computer=$%.4f)\n",
		*days, len(targets), suspendedCount, missingEntCount,
		fleetHosted, fleetBYOK, fleetLocal, fleetLLM, fleetSearch, fleetComputer,
	)
	if hostedAll, err := env.Billing.GetGlobalBudget(ctx, now, billing.ScopeHostedAll); err == nil {
		hostedExa, _ := env.Billing.GetGlobalBudget(ctx, now, billing.ScopeHostedExa)
		trialsToday, _ := env.Billing.GetGlobalBudget(ctx, now, billing.ScopeTrialsCreated)
		fmt.Fprintf(out, "Today global budgets (%s): hosted_all=$%.4f hosted_exa=$%.4f trials_created=%d\n",
			now.Format("2006-01-02"),
			billing.MicrosToUSD(hostedAll.SpentMicros),
			billing.MicrosToUSD(hostedExa.SpentMicros),
			trialsToday.Count,
		)
	}
	return nil
}

func runBackfillEntitlements(ctx context.Context, args []string, openEnv func(context.Context) (*cliEnv, error), out io.Writer) error {
	fs := flag.NewFlagSet("nori-user backfill-entitlements", flag.ContinueOnError)
	fs.SetOutput(out)
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	env, err := openEnv(ctx)
	if err != nil {
		return err
	}
	if env.Close != nil {
		defer env.Close()
	}

	allUsers, err := env.Users.List(ctx)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}

	backfilled := 0
	existing := 0
	for _, u := range allUsers {
		_, err := env.Billing.GetEntitlement(ctx, u.ID)
		if err == nil {
			existing++
			continue
		}
		if !errors.Is(err, billing.ErrEntitlementMissing) {
			return fmt.Errorf("check entitlement for %s: %w", u.ID, err)
		}
		if _, err := env.Billing.SetTier(ctx, u.ID, billing.TierBYOK, billing.SetTierOptions{
			Status:    billing.StatusActive,
			SkipGrant: true,
		}); err != nil {
			return fmt.Errorf("backfill entitlement for %s: %w", u.ID, err)
		}
		backfilled++
	}
	fmt.Fprintf(out, "Backfill complete: total_users=%d backfilled=%d already_present=%d\n",
		len(allUsers), backfilled, existing)
	return nil
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
