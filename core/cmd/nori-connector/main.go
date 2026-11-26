package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"nori/internal/homeconnector"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type commonFlags struct {
	relay    string
	stateDir string
	ollama   string
}

func bindCommonFlags(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{}
	fs.StringVar(&c.relay, "url", "", "connector relay URL")
	fs.StringVar(&c.stateDir, "state-dir", "", "private state directory")
	fs.StringVar(&c.ollama, "ollama-url", "", "Ollama loopback URL")
	return c
}

func parseSubcommand(fs *flag.FlagSet, common *commonFlags, args []string) (homeconnector.Config, error) {
	if err := fs.Parse(args); err != nil {
		return homeconnector.Config{}, err
	}
	if fs.NArg() != 0 {
		return homeconnector.Config{}, errors.New("unexpected arguments")
	}
	return (homeconnector.Config{RelayURL: common.relay, StateDir: common.stateDir, OllamaURL: common.ollama}).Validate()
}

func usageError() error {
	if homeconnector.DesktopMode == "1" {
		return errors.New("usage: nori-connector <pair|serve|status|doctor|unpair|desktop-enable|desktop-disable|desktop-revoke-pending> [options]")
	}
	return errors.New("usage: nori-connector <pair|serve|status|doctor|unpair> [options]")
}

func run(args []string) error {
	if len(args) < 1 {
		return usageError()
	}
	command, rest := args[0], args[1:]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	encoder := json.NewEncoder(os.Stdout)

	switch command {
	case "desktop-enable", "desktop-disable", "desktop-revoke-pending":
		if homeconnector.DesktopMode != "1" {
			return errors.New("desktop sharing controls are unavailable")
		}
		fs := flag.NewFlagSet(command, flag.ContinueOnError)
		common := bindCommonFlags(fs)
		cfg, err := parseSubcommand(fs, common, rest)
		if err != nil {
			return err
		}
		if command == "desktop-enable" {
			if err := homeconnector.DesktopEnable(cfg); err != nil {
				return err
			}
			return encoder.Encode(map[string]bool{"enabled": true})
		}
		var pending bool
		if command == "desktop-disable" {
			pending, err = homeconnector.DesktopDisable(ctx, cfg)
		} else {
			pending, err = homeconnector.DesktopRevokePending(ctx, cfg)
		}
		if err != nil {
			return err
		}
		return encoder.Encode(map[string]bool{"revocation_pending": pending})

	case "pair":
		fs := flag.NewFlagSet(command, flag.ContinueOnError)
		common := bindCommonFlags(fs)
		jsonOutput := fs.Bool("json", false, "write machine-readable output")
		deviceName := fs.String("device-name", "", "name shown while pairing")
		cfg, err := parseSubcommand(fs, common, rest)
		if err != nil {
			return err
		}
		err = homeconnector.Pair(ctx, cfg, *deviceName, func(pairing homeconnector.Pairing) error {
			if *jsonOutput {
				return encoder.Encode(struct {
					Type string `json:"type"`
					homeconnector.Pairing
				}{Type: "pairing", Pairing: pairing})
			}
			fmt.Printf("Open %s and enter %s\n", pairing.VerificationURI, pairing.UserCode)
			return nil
		})
		if err != nil {
			return err
		}
		if *jsonOutput {
			return encoder.Encode(map[string]string{"type": "paired"})
		}
		fmt.Println("Connector paired.")
		return nil

	case "serve":
		fs := flag.NewFlagSet(command, flag.ContinueOnError)
		common := bindCommonFlags(fs)
		cfg, err := parseSubcommand(fs, common, rest)
		if err != nil {
			return err
		}
		connector, err := homeconnector.New(cfg)
		if err != nil {
			return err
		}
		return connector.Serve(ctx)

	case "doctor":
		fs := flag.NewFlagSet(command, flag.ContinueOnError)
		common := bindCommonFlags(fs)
		jsonOutput := fs.Bool("json", false, "write machine-readable output")
		cfg, err := parseSubcommand(fs, common, rest)
		if err != nil {
			return err
		}
		connector, err := homeconnector.New(cfg)
		if err != nil {
			return err
		}
		status, checkErr := connector.Doctor(ctx)
		if *jsonOutput {
			if err := encoder.Encode(status); err != nil {
				return err
			}
		} else {
			fmt.Println(status.Message)
		}
		return checkErr

	case "status":
		fs := flag.NewFlagSet(command, flag.ContinueOnError)
		common := bindCommonFlags(fs)
		jsonOutput := fs.Bool("json", false, "write machine-readable output")
		cfg, err := parseSubcommand(fs, common, rest)
		if err != nil {
			return err
		}
		status, err := homeconnector.ReadStatus(cfg.StateDir)
		if homeconnector.IsMissingStatus(err) {
			status = homeconnector.Status{State: "stopped"}
		} else if err != nil {
			return err
		}
		status.Paired, err = homeconnector.CredentialPresent(cfg)
		if err != nil {
			return err
		}
		if homeconnector.DesktopMode == "1" {
			status.RevocationPending, err = homeconnector.DesktopRevocationPending(cfg)
			if err != nil {
				return err
			}
		}
		if !status.UpdatedAt.IsZero() && time.Since(status.UpdatedAt) > 90*time.Second {
			status.State = "stopped"
		}
		if *jsonOutput {
			return encoder.Encode(status)
		}
		message := strings.ReplaceAll(status.State, "_", " ")
		if status.State == "online" && !status.OllamaAvailable {
			message = "connected; Ollama unavailable"
		}
		fmt.Println(message)
		return nil

	case "unpair":
		fs := flag.NewFlagSet(command, flag.ContinueOnError)
		common := bindCommonFlags(fs)
		jsonOutput := fs.Bool("json", false, "write machine-readable output")
		cfg, err := parseSubcommand(fs, common, rest)
		if err != nil {
			return err
		}
		if err := homeconnector.Unpair(ctx, cfg); err != nil {
			return err
		}
		if *jsonOutput {
			return encoder.Encode(map[string]string{"type": "unpaired"})
		}
		fmt.Println("Connector unpaired.")
		return nil

	default:
		return fmt.Errorf("unknown command %q", command)
	}
}
