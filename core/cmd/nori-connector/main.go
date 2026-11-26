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
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: nori-connector <pair|serve|status|doctor|unpair> [options]")
	}
	command := os.Args[1]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	relay := flags.String("url", "", "connector relay URL")
	stateDir := flags.String("state-dir", "", "private state directory")
	ollama := flags.String("ollama-url", "", "Ollama loopback URL")
	jsonOutput := flags.Bool("json", false, "write machine-readable output")
	deviceName := flags.String("device-name", "", "name shown while pairing")
	if err := flags.Parse(os.Args[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	cfg, err := (homeconnector.Config{RelayURL: *relay, StateDir: *stateDir, OllamaURL: *ollama}).Validate()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	encoder := json.NewEncoder(os.Stdout)
	switch command {
	case "desktop-enable":
		if homeconnector.DesktopMode != "1" {
			return errors.New("desktop sharing controls are unavailable")
		}
		if err := homeconnector.DesktopEnable(cfg); err != nil {
			return err
		}
		return encoder.Encode(map[string]bool{"enabled": true})
	case "desktop-disable", "desktop-revoke-pending":
		if homeconnector.DesktopMode != "1" {
			return errors.New("desktop sharing controls are unavailable")
		}
		var pending bool
		var err error
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
		err := homeconnector.Pair(ctx, cfg, *deviceName, func(pairing homeconnector.Pairing) error {
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
		connector, err := homeconnector.New(cfg)
		if err != nil {
			return err
		}
		return connector.Serve(ctx)
	case "doctor":
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
