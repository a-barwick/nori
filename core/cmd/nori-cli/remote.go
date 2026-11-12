package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"time"

	"github.com/google/uuid"
	"nori/internal/clientauth"
	"nori/internal/httpjson"
)

var confirmationCode = regexp.MustCompile(`^[A-F0-9]{8}$`)

func credentialAccount(name string, gateway, web *url.URL) string {
	hash := sha256.Sum256([]byte(name + "\n" + gateway.String() + "\n" + web.String()))
	return hex.EncodeToString(hash[:])
}

func invalidCredential(err error) bool {
	var failure *requestError
	return errors.As(err, &failure) && failure.Status == http.StatusUnauthorized && failure.Code == "invalid_client_token"
}

func (a *cli) remote(ctx context.Context, name string, gateway, web *url.URL, args []string) error {
	account := credentialAccount(name, gateway, web)
	switch args[0] {
	case "login":
		ctx, cancel := context.WithTimeout(ctx, a.timeout)
		defer cancel()
		return a.login(ctx, account, web, args[1:])
	case "logout":
		if len(args) != 1 {
			return errors.New("logout takes no arguments")
		}
		ctx, cancel := context.WithTimeout(ctx, a.timeout)
		defer cancel()
		return a.logout(ctx, account)
	}
	token, err := readCredential(ctx, account)
	if err == nil {
		a.client.token = token
		err = a.runCommand(ctx, args)
	}
	if errors.Is(err, errCredentialMissing) || invalidCredential(err) {
		return fmt.Errorf("sign in again with nori-cli --profile %s login", name)
	}
	return err
}

func (a *cli) login(ctx context.Context, account string, web *url.URL, args []string) error {
	flags := a.flags("login")
	host, _ := os.Hostname()
	name := flags.String("name", "Nori CLI on "+host, "label shown in Settings")
	noOpen := flags.Bool("no-open", false, "print the approval URL without opening it")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("login takes no positional arguments")
	}
	saved, err := readCredential(ctx, account)
	if err == nil {
		a.client.token = saved
		err = a.client.call(ctx, "GET", "/v1/capabilities", nil, nil)
		if err == nil {
			_, err = fmt.Fprintln(a.out, "Already signed in.")
			return err
		}
		if !invalidCredential(err) {
			return err
		}
		a.client.token = ""
	} else if !errors.Is(err, errCredentialMissing) {
		return err
	}
	secret, proof := clientauth.NewSecret(), clientauth.NewSecret()
	start := clientauth.DeviceStart{Name: *name, CredentialHash: clientauth.Hash(secret), ProofHash: clientauth.ProofHash(proof)}
	var challenge clientauth.DeviceChallenge
	if err := a.client.call(ctx, "POST", clientauth.DeviceStartPath, start, &challenge); err != nil {
		return err
	}
	if httpjson.RequestID(challenge.ID) == "" || !confirmationCode.MatchString(challenge.Code) ||
		challenge.IntervalSeconds < 1 || challenge.IntervalSeconds > 30 || !challenge.ExpiresAt.After(time.Now()) {
		return errors.New("Nori returned an invalid sign-in challenge")
	}
	link := web.ResolveReference(&url.URL{Path: "/settings/clients", RawQuery: url.Values{"authorize": {challenge.ID}}.Encode()})
	fmt.Fprintf(a.err, "Approve this CLI at %s\nConfirmation code %s\n", link.String(), challenge.Code)
	if !*noOpen {
		openBrowser(link.String())
	}
	poll := clientauth.DevicePoll{ID: challenge.ID, Proof: proof}
	ctx, cancel := context.WithDeadline(ctx, challenge.ExpiresAt.Add(clientauth.DevicePollGrace))
	defer cancel()
	interval := time.Duration(challenge.IntervalSeconds) * time.Second
	delay := interval
	for {
		if err := pause(ctx, delay); err != nil {
			return fmt.Errorf("sign-in expired or was interrupted: %w", err)
		}
		var status clientauth.DeviceStatus
		if err := a.client.call(ctx, "POST", clientauth.DeviceStatusPath, poll, &status); err != nil {
			if retryable(err) {
				delay = max(interval, retryDelay(err, interval))
				continue
			}
			return err
		}
		delay = interval
		switch status.Status {
		case "pending":
			continue
		case "approved":
			token := clientauth.Token(status.OwnerID, status.TokenID, secret)
			if _, valid := clientauth.Parse(token); !valid {
				return errors.New("Nori returned an invalid credential ID")
			}
			if err := storeCredential(ctx, account, token); err != nil {
				a.client.token = token
				_ = a.revokeCredential(ctx)
				return err
			}
			_, err := fmt.Fprintln(a.out, "Signed in.")
			return err
		default:
			return errors.New("Nori returned an invalid sign-in status")
		}
	}
}

func openBrowser(address string) {
	program := "xdg-open"
	if runtime.GOOS == "darwin" {
		program = "/usr/bin/open"
	}
	command := exec.Command(program, address)
	if command.Start() == nil {
		go func() { _ = command.Wait() }()
	}
}

func (a *cli) revokeCredential(ctx context.Context) error {
	err := a.client.call(ctx, "POST", "/v1/client-token/revoke", map[string]string{"request_id": uuid.NewString()}, nil)
	if invalidCredential(err) {
		return nil
	}
	return err
}

func (a *cli) logout(ctx context.Context, account string) error {
	token, err := readCredential(ctx, account)
	if errors.Is(err, errCredentialMissing) {
		_, err = fmt.Fprintln(a.out, "Already signed out.")
		return err
	}
	if err != nil {
		return err
	}
	a.client.token = token
	if err := a.revokeCredential(ctx); err != nil {
		return err
	}
	if err := removeCredential(ctx, account); err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.out, "Signed out.")
	return err
}
