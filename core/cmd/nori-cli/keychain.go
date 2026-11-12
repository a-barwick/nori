package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"nori/internal/clientauth"
)

const serviceName = "nori-cli"

var errCredentialMissing = errors.New("Nori credential not found")

func credentialMissing(err error, code int) bool {
	var failure *exec.ExitError
	return errors.As(err, &failure) && failure.ExitCode() == code
}

func macKeychainArgs(action, account string) []string {
	args := []string{action, "-s", serviceName, "-a", account}
	if action == "find-generic-password" {
		args = append(args, "-w")
	}
	if path := os.Getenv("NORI_CLI_KEYCHAIN"); path != "" {
		args = append(args, path)
	}
	return args
}

func readCredential(ctx context.Context, account string) (string, error) {
	var command *exec.Cmd
	var missing int
	switch runtime.GOOS {
	case "darwin":
		command = exec.CommandContext(ctx, "/usr/bin/security", macKeychainArgs("find-generic-password", account)...)
		missing = 44
	case "linux":
		command = exec.CommandContext(ctx, "secret-tool", "lookup", "service", serviceName, "account", account)
		missing = 1
	default:
		return "", errors.New("remote profiles require macOS Keychain or Linux Secret Service")
	}
	data, err := command.Output()
	if credentialMissing(err, missing) {
		return "", errCredentialMissing
	}
	if err != nil {
		return "", errors.New("could not read the OS credential store")
	}
	token := strings.TrimSpace(string(data))
	if _, valid := clientauth.Parse(token); !valid {
		return "", errCredentialMissing
	}
	return token, nil
}

func storeCredential(ctx context.Context, account, token string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		if _, valid := clientauth.Parse(token); !valid {
			return errors.New("invalid credential")
		}
		// security -i reads the command from stdin. The token never appears in argv.
		input := fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s", serviceName, account, token)
		if path := os.Getenv("NORI_CLI_KEYCHAIN"); path != "" {
			if strings.ContainsAny(path, "\r\n") {
				return errors.New("invalid keychain path")
			}
			input += " " + strconv.Quote(path)
		}
		command = exec.CommandContext(ctx, "/usr/bin/security", "-i")
		command.Stdin = strings.NewReader(input + "\n")
	case "linux":
		command = exec.CommandContext(ctx, "secret-tool", "store", "--label=Nori CLI", "service", serviceName, "account", account)
		command.Stdin = strings.NewReader(token)
	default:
		return errors.New("remote profiles require macOS Keychain or Linux Secret Service")
	}
	if err := command.Run(); err != nil {
		return errors.New("could not save to the OS credential store")
	}
	saved, err := readCredential(ctx, account)
	if err != nil || saved != token {
		return errors.New("could not confirm the credential in the OS store")
	}
	return nil
}

func removeCredential(ctx context.Context, account string) error {
	var command *exec.Cmd
	var missing int
	switch runtime.GOOS {
	case "darwin":
		command = exec.CommandContext(ctx, "/usr/bin/security", macKeychainArgs("delete-generic-password", account)...)
		missing = 44
	case "linux":
		command = exec.CommandContext(ctx, "secret-tool", "clear", "service", serviceName, "account", account)
		missing = 1
	default:
		return errors.New("remote profiles require macOS Keychain or Linux Secret Service")
	}
	err := command.Run()
	if err != nil && !credentialMissing(err, missing) {
		return errors.New("could not remove the OS credential")
	}
	return nil
}
