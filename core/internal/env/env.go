// Package env is the single place entrypoints read process configuration.
package env

import (
	"fmt"
	"os"

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
	file := Get("NORI_ENV_FILE", "../.env")
	if err := godotenv.Load(file); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not load %s; check its syntax and permissions", file)
	}
	if task := Get("NORI_TASK_ENV_FILE", file); task != file {
		if err := godotenv.Load(task); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("could not load task configuration %s", task)
		}
	}
	return nil
}
