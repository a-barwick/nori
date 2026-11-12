package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"nori/internal/clientauth"
	"nori/internal/users"
)

const usage = `Usage: nori-cli [--url URL] [--user ID | --email EMAIL] [--profile NAME] [--json] COMMAND

  chat [--conversation ID|assistant]
  ask [--conversation ID|assistant] [--request-id UUID] [--attach FILE] TEXT
  conversations
  tasks [TASK]
  task start [--request-id UUID] TEXT
  task answer [--request-id UUID] INTERACTION TEXT
  task approve|decline [--request-id UUID] INTERACTION
  task stop [--request-id UUID] TASK
  upload [--request-id UUID] CONVERSATION FILE
  result [--out FILE] ID
  profile add --gateway URL --web URL NAME
  profile use NAME | profile list
  login [--name LABEL] [--no-open]
  logout
`

type cli struct {
	client  *client
	out     io.Writer
	err     io.Writer
	in      io.Reader
	machine bool
	timeout time.Duration
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "nori-cli:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) error {
	flags := flag.NewFlagSet("nori-cli", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, usage); flags.PrintDefaults() }
	localURL := flags.String("url", envDefault("NORI_LOCAL_AGENT_URL", "http://127.0.0.1:8080"), "loopback Core URL")
	owner := flags.String("user", os.Getenv("NORI_USER_ID"), "local user ID")
	email := flags.String("email", os.Getenv("NORI_LOCAL_EMAIL"), "local sign-in email")
	profileFlag := flags.String("profile", "", "local or a configured remote profile")
	machine := flags.Bool("json", false, "write JSON output")
	timeout := flags.Duration("timeout", 5*time.Minute, "time to wait for each command or message")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() == 0 {
		flags.Usage()
		return nil
	}
	dir, err := configDir()
	if err != nil {
		return err
	}
	if flags.Arg(0) == "profile" {
		return profileCommand(dir, flags.Args()[1:], out, stderr)
	}
	name, err := profileSelection(dir, *profileFlag)
	if err != nil {
		return err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 35 * time.Second
	c := &client{http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
	defer c.http.CloseIdleConnections()
	app := &cli{client: c, in: in, out: out, err: stderr, machine: *machine, timeout: *timeout}
	if name != localProfile {
		config, err := readProfile(dir, name)
		if err != nil {
			return err
		}
		gateway, web, err := config.urls()
		if err != nil {
			return err
		}
		c.base = gateway
		return app.remote(ctx, name, gateway, web, flags.Args())
	}
	if flags.Arg(0) == "login" || flags.Arg(0) == "logout" {
		return errors.New("select a remote profile for login or logout")
	}
	c.base, err = endpoint(*localURL, false)
	if err != nil {
		return err
	}
	if *email != "" && *owner == "" {
		var identity struct{ ID string }
		if err := c.call(ctx, "POST", "/api/users/resolve", map[string]string{"email": *email}, &identity); err != nil {
			return err
		}
		*owner = identity.ID
	}
	if !users.ValidID(*owner) {
		return errors.New("set --user or --email for the local Nori user")
	}
	c.owner = *owner
	return app.runCommand(ctx, flags.Args())
}

func (a *cli) runCommand(ctx context.Context, args []string) error {
	if args[0] == "chat" || args[0] == "ask" {
		return a.command(ctx, args)
	}
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	return a.command(ctx, args)
}

func endpoint(raw string, hosted bool) (*url.URL, error) {
	base, err := url.Parse(strings.TrimRight(raw, "/") + "/")
	if err != nil || base.Hostname() == "" || base.User != nil || base.Fragment != "" || base.RawQuery != "" || base.Path != "/" {
		return nil, errors.New("supply an origin URL with no credentials or path")
	}
	if hosted && base.Scheme == "https" {
		return base, nil
	}
	ip := net.ParseIP(base.Hostname())
	if base.Scheme == "http" && (base.Hostname() == "localhost" || ip != nil && ip.IsLoopback()) {
		return base, nil
	}
	if hosted {
		return nil, errors.New("remote Nori requires HTTPS or a loopback HTTP sandbox")
	}
	return nil, errors.New("local Nori requires a loopback HTTP origin")
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func (a *cli) flags(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(a.err)
	return flags
}

func (a *cli) emit(value any) error {
	return json.NewEncoder(a.out).Encode(value)
}

func (a *cli) scope() string {
	if credential, ok := clientauth.Parse(a.client.token); ok {
		return a.client.base.String() + credential.Owner
	}
	return a.client.base.String() + a.client.owner
}

func (a *cli) command(ctx context.Context, args []string) error {
	switch args[0] {
	case "chat", "ask":
		return a.converse(ctx, args[0], args[1:])
	case "conversations":
		var list struct{ Conversations []conversationView }
		if err := a.client.call(ctx, "GET", "/v1/conversations", nil, &list); err != nil {
			return err
		}
		if a.machine {
			return a.emit(list.Conversations)
		}
		for _, item := range list.Conversations {
			fmt.Fprintf(a.out, "%s\t%s\n", item.ID, item.Title)
		}
		return nil
	case "tasks":
		return a.tasks(ctx, args[1:])
	case "task":
		return a.task(ctx, args[1:])
	case "upload":
		return a.uploadCommand(ctx, args[1:])
	case "result":
		return a.result(ctx, args[1:])
	default:
		return fmt.Errorf("unknown command %q. Use --help", args[0])
	}
}
