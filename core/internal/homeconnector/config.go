package homeconnector

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nori/internal/homeaiwire"
)

var DefaultRelayURL string
var Version = "dev"

type Config struct {
	RelayURL  string
	StateDir  string
	OllamaURL string
}

func (c Config) Validate() (Config, error) {
	c.RelayURL = strings.TrimRight(strings.TrimSpace(c.RelayURL), "/")
	if c.RelayURL == "" {
		c.RelayURL = strings.TrimRight(strings.TrimSpace(os.Getenv("NORI_CONNECTOR_RELAY_URL")), "/")
	}
	if c.RelayURL == "" {
		c.RelayURL = strings.TrimRight(strings.TrimSpace(DefaultRelayURL), "/")
	}
	u, err := url.Parse(c.RelayURL)
	if err != nil || u == nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" {
		return c, errors.New("set a valid connector relay URL")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := strings.EqualFold(u.Hostname(), "localhost") || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return c, errors.New("connector relay URL must use HTTPS")
	}
	if c.StateDir == "" {
		c.StateDir = os.Getenv("NORI_CONNECTOR_STATE_DIR")
	}
	if c.StateDir == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return c, fmt.Errorf("locate connector state: %w", err)
		}
		c.StateDir = filepath.Join(dir, "nori-connector")
	}
	if !filepath.IsAbs(c.StateDir) {
		return c, errors.New("connector state directory must be an absolute path")
	}
	if c.OllamaURL == "" {
		c.OllamaURL = os.Getenv("NORI_CONNECTOR_OLLAMA_URL")
	}
	if c.OllamaURL == "" {
		c.OllamaURL = "http://127.0.0.1:11434"
	}
	ollama, err := url.Parse(c.OllamaURL)
	if err != nil || ollama == nil || ollama.Scheme != "http" || ollama.User != nil || ollama.RawQuery != "" || ollama.ForceQuery || ollama.Fragment != "" || ollama.Path != "" {
		return c, errors.New("Ollama URL must be an HTTP loopback URL")
	}
	ollamaIP := net.ParseIP(ollama.Hostname())
	if ollamaIP == nil || !ollamaIP.IsLoopback() {
		return c, errors.New("Ollama URL must use a numeric loopback address")
	}
	c.OllamaURL = strings.TrimRight(c.OllamaURL, "/")
	return c, nil
}

type Credential struct {
	RelayURL     string    `json:"relay_url"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
}

type Status struct {
	State           string             `json:"state"`
	Paired          bool               `json:"paired"`
	Message         string             `json:"message,omitempty"`
	OllamaAvailable bool               `json:"ollama_available"`
	Models          []homeaiwire.Model `json:"models,omitempty"`
	UpdatedAt       time.Time          `json:"updated_at"`
}
