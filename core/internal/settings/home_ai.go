package settings

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"nori/internal/netguard"
)

type HomeAI struct {
	Key, VerificationURL, PostgresURL, PostgresListenURL, RelayHost string
	MaxConns                                                        int
	MaxConnsRaw                                                     string
	WorkEnabled                                                     bool
	Downloads                                                       HomeAIDownloads
}

type HomeAIDownloads struct {
	MacOS      string `json:"macos,omitempty"`
	LinuxAMD64 string `json:"linux_amd64,omitempty"`
	LinuxARM64 string `json:"linux_arm64,omitempty"`
}

func homeAIFromEnvironment() HomeAI {
	value := func(name string) string { return strings.TrimSpace(os.Getenv(name)) }
	rawMaxConns := value("NORI_HOME_AI_MAX_CONNS")
	maxConns := 4
	if rawMaxConns != "" {
		if parsed, err := strconv.Atoi(rawMaxConns); err == nil {
			maxConns = parsed
		} else {
			maxConns = -1
		}
	}
	return HomeAI{
		Key: value("NORI_HOME_AI_KEY"), VerificationURL: value("NORI_HOME_AI_VERIFY_URL"),
		PostgresURL: value("NORI_HOME_AI_POSTGRES_URL"), PostgresListenURL: value("NORI_HOME_AI_POSTGRES_LISTEN_URL"),
		RelayHost:   strings.ToLower(value("NORI_HOME_AI_RELAY_HOST")),
		MaxConns:    maxConns,
		MaxConnsRaw: rawMaxConns,
		WorkEnabled: value("NORI_HOME_AI_WORK_ENABLED") == "true",
		Downloads: HomeAIDownloads{
			MacOS:      value("NORI_HOME_AI_DOWNLOAD_MACOS"),
			LinuxAMD64: value("NORI_HOME_AI_DOWNLOAD_LINUX_AMD64"),
			LinuxARM64: value("NORI_HOME_AI_DOWNLOAD_LINUX_ARM64"),
		},
	}
}

func (c HomeAI) Enabled() bool { return c.Key != "" }

func (c HomeAI) EncryptionKey() ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(c.Key)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("NORI_HOME_AI_KEY must encode a 32-byte key in base64")
	}
	return key, nil
}

func (c HomeAI) validate(cloud string) error {
	if !c.Enabled() && c.VerificationURL == "" && c.PostgresURL == "" && c.PostgresListenURL == "" && c.RelayHost == "" && c.MaxConnsRaw == "" && c.Downloads == (HomeAIDownloads{}) && !c.WorkEnabled {
		return nil
	}
	if c.MaxConnsRaw != "" && (c.MaxConns < 1 || c.MaxConns > 100) {
		return fmt.Errorf("NORI_HOME_AI_MAX_CONNS must be between 1 and 100")
	}
	if c.RelayHost != "" && strings.ContainsAny(c.RelayHost, "/:?# \t\r\n") {
		return fmt.Errorf("NORI_HOME_AI_RELAY_HOST must be a bare hostname")
	}
	if _, err := c.EncryptionKey(); err != nil {
		return err
	}
	u, err := url.Parse(c.VerificationURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && (cloud != "local" || u.Scheme != "http" || !netguard.IsLoopbackHost(u.Hostname()))) {
		return fmt.Errorf("NORI_HOME_AI_VERIFY_URL must be an HTTPS pairing page URL")
	}
	for _, raw := range []string{c.Downloads.MacOS, c.Downloads.LinuxAMD64, c.Downloads.LinuxARM64} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("NORI_HOME_AI_DOWNLOAD_* URLs must be HTTPS artifact URLs")
		}
	}
	return nil
}
