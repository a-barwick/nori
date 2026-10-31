// Package env is the single place entrypoints read process configuration.
package env

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

func Get(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// Load reads NORI_ENV_FILE (default ../.env), then NORI_TASK_ENV_FILE when it
// names a separate task configuration. Missing files are fine in deployed
// environments; malformed files are not. Already-set variables win.
func Load() error {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if managementCredential(name) {
			_ = os.Unsetenv(name)
		}
	}
	file := Get("NORI_ENV_FILE", "../.env")
	if err := loadRuntime(file); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not load %s; check its syntax and permissions", file)
	}
	if task := Get("NORI_TASK_ENV_FILE", file); task != file {
		if err := loadRuntime(task); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("could not load task configuration %s", task)
		}
	}
	return nil
}

func managementCredential(name string) bool {
	switch name {
	case "OPENROUTER_MANAGEMENT_KEY", "TEMPORAL_BILLING_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "NORI_SPEND_SIGNING_KEY", "NORI_SPEND_FEATURE_ISSUER_TOKEN", "NORI_SPEND_CI_ISSUER_TOKEN", "SENTRY_AUTH_TOKEN":
		return true
	}
	return strings.HasPrefix(name, "NORI_SPEND_LEASE_")
}

func loadRuntime(path string) error {
	values, err := godotenv.Read(path)
	if err != nil {
		return err
	}
	for name, value := range values {
		if _, exists := os.LookupEnv(name); !exists && !managementCredential(name) {
			if err := os.Setenv(name, value); err != nil {
				return err
			}
		}
	}
	return nil
}
