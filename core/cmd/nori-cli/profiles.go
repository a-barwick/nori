package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const localProfile = "local"

var profileName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type profile struct {
	Gateway string `json:"gateway"`
	Web     string `json:"web"`
}

func configDir() (string, error) {
	if configured := os.Getenv("NORI_CLI_CONFIG_DIR"); configured != "" {
		return filepath.Abs(configured)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "nori-cli"), nil
}

func writeConfig(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".profile-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(body)
	if err := errors.Join(writeErr, tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func profileSelection(dir, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "active-profile"))
	if errors.Is(err, os.ErrNotExist) {
		return localProfile, nil
	}
	return strings.TrimSpace(string(data)), err
}

func readProfile(dir, name string) (profile, error) {
	var value profile
	if !profileName.MatchString(name) || name == localProfile {
		return value, fmt.Errorf("invalid remote profile name")
	}
	data, err := os.ReadFile(filepath.Join(dir, "profiles", name+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return value, fmt.Errorf("profile %q does not exist", name)
	}
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return value, fmt.Errorf("profile %q is unreadable", name)
	}
	return value, nil
}

func (p profile) urls() (*url.URL, *url.URL, error) {
	gateway, err := endpoint(p.Gateway, true)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway URL: %w", err)
	}
	web, err := endpoint(p.Web, true)
	if err != nil {
		return nil, nil, fmt.Errorf("web URL: %w", err)
	}
	if gateway.Scheme == "https" && web.Scheme != "https" {
		return nil, nil, errors.New("a hosted gateway needs an HTTPS web origin")
	}
	return gateway, web, nil
}

func profileCommand(dir string, args []string, out, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("use profile add, use or list")
	}
	switch args[0] {
	case "add":
		a := cli{err: stderr}
		flags := a.flags("profile add")
		gateway := flags.String("gateway", "", "client gateway origin")
		web := flags.String("web", "", "Nori web app origin")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		name := flags.Arg(0)
		if flags.NArg() != 1 || !profileName.MatchString(name) || name == localProfile {
			return errors.New("supply one remote profile name using lowercase letters, digits, underscores or hyphens")
		}
		value := profile{*gateway, *web}
		gatewayURL, webURL, err := value.urls()
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(profile{gatewayURL.String(), webURL.String()}, "", "  ")
		if err != nil {
			return err
		}
		path := filepath.Join(dir, "profiles", name+".json")
		return writeConfig(path, append(data, '\n'))
	case "use":
		if len(args) != 2 {
			return errors.New("profile use requires one profile name")
		}
		if args[1] != localProfile {
			if _, err := readProfile(dir, args[1]); err != nil {
				return err
			}
		}
		return writeConfig(filepath.Join(dir, "active-profile"), []byte(args[1]+"\n"))
	case "list":
		if len(args) != 1 {
			return errors.New("profile list takes no arguments")
		}
		active, err := profileSelection(dir, "")
		if err != nil {
			return err
		}
		display := func(name string) {
			marker := " "
			if name == active {
				marker = "*"
			}
			fmt.Fprintf(out, "%s %s\n", marker, name)
		}
		display(localProfile)
		entries, err := os.ReadDir(filepath.Join(dir, "profiles"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, entry := range entries {
			if name, ok := strings.CutSuffix(entry.Name(), ".json"); ok && !entry.IsDir() && profileName.MatchString(name) {
				display(name)
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown profile command %q", args[0])
	}
}
