package settings

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Computer configures Nori's Personal Agent Computer subsystem.
type Computer struct {
	Provider            string // disabled | sprites | docker | fake
	SpritesAPIToken     string
	SpritesOrg          string
	SpritesBaseURL      string
	DockerImage         string
	DockerLocalhostOnly bool
	MemoryWatchdogMB    int
	MaxDiskGB           int
}

func computerFromEnvironment() Computer {
	value := func(name string) string { return strings.TrimSpace(os.Getenv(name)) }
	provider := strings.ToLower(value("NORI_COMPUTER_PROVIDER"))
	if provider == "" {
		provider = "disabled"
	}
	memMB := 3072
	if raw := value("NORI_COMPUTER_MEMORY_WATCHDOG_MB"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			memMB = parsed
		} else {
			memMB = -1
		}
	}
	maxDiskGB := 10
	if raw := value("NORI_COMPUTER_MAX_DISK_GB"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			maxDiskGB = parsed
		} else {
			maxDiskGB = -1
		}
	}
	return Computer{
		Provider:            provider,
		SpritesAPIToken:     value("NORI_SPRITES_API_TOKEN"),
		SpritesOrg:          value("NORI_SPRITES_ORG"),
		SpritesBaseURL:      value("NORI_SPRITES_BASE_URL"),
		DockerImage:         value("NORI_COMPUTER_DOCKER_IMAGE"),
		DockerLocalhostOnly: value("NORI_COMPUTER_DOCKER_LOCALHOST_ONLY") == "true",
		MemoryWatchdogMB:    memMB,
		MaxDiskGB:           maxDiskGB,
	}
}

func (c Computer) Enabled() bool {
	return c.Provider != "" && c.Provider != "disabled"
}

func (c Computer) validate(cloud string) error {
	switch c.Provider {
	case "", "disabled":
		return nil
	case "sprites":
		if c.SpritesAPIToken == "" {
			return fmt.Errorf("NORI_SPRITES_API_TOKEN must be configured when NORI_COMPUTER_PROVIDER=sprites")
		}
	case "docker":
		if cloud != "local" {
			return fmt.Errorf("NORI_COMPUTER_PROVIDER=docker requires NORI_CLOUD=local")
		}
	case "fake":
		if cloud != "local" {
			return fmt.Errorf("NORI_COMPUTER_PROVIDER=fake requires NORI_CLOUD=local")
		}
	default:
		return fmt.Errorf("NORI_COMPUTER_PROVIDER must be disabled, sprites, docker, or fake")
	}
	if c.MemoryWatchdogMB < 512 || c.MemoryWatchdogMB > 32768 {
		return fmt.Errorf("NORI_COMPUTER_MEMORY_WATCHDOG_MB must be between 512 and 32768")
	}
	if c.MaxDiskGB < 1 || c.MaxDiskGB > 100 {
		return fmt.Errorf("NORI_COMPUTER_MAX_DISK_GB must be between 1 and 100")
	}
	return nil
}
