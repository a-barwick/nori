package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"nori/internal/agent"
	"nori/internal/env"
	"nori/internal/wake"
)

// config is the worker's release identity, polling windows and capabilities. It is
// read per run, so a missing release surfaces on the request that needs it.
type config struct {
	build, deployment      string
	pollWindow, idleWindow time.Duration
	capabilities           agent.Capabilities
}

// capabilities reads the credentials of the optional tools. A missing credential
// leaves that tool reporting itself unavailable.
func capabilities() (agent.Capabilities, error) {
	location, err := time.LoadLocation(env.Get("NORI_TIMEZONE", "America/Los_Angeles"))
	if err != nil {
		return agent.Capabilities{}, fmt.Errorf("NORI_TIMEZONE must be an IANA time zone")
	}
	// A local test stack points the worker at a loopback provider. Nothing else may.
	base, tokenURL := "", ""
	if address := os.Getenv("NORI_E2E_GOOGLE_URL"); address != "" {
		if os.Getenv("TEMPORAL_LOCAL") != "true" || !strings.HasPrefix(address, "http://127.0.0.1:") {
			return agent.Capabilities{}, fmt.Errorf("NORI_E2E_GOOGLE_URL requires TEMPORAL_LOCAL=true and a 127.0.0.1 address")
		}
		base, tokenURL = address, address+"/token"
	}
	return agent.Capabilities{
		SearchKey: os.Getenv("BRAVE_SEARCH_API_KEY"), Location: location,
		Calendar: agent.CalendarConfig{
			ClientID: os.Getenv("GOOGLE_CALENDAR_CLIENT_ID"), ClientSecret: os.Getenv("GOOGLE_CALENDAR_CLIENT_SECRET"),
			RefreshToken:  os.Getenv("GOOGLE_CALENDAR_REFRESH_TOKEN"),
			Account:       env.Get("GOOGLE_CALENDAR_ACCOUNT", "the connected Google account"),
			WriteCalendar: os.Getenv("GOOGLE_CALENDAR_WRITE_ID"), APIBase: base, TokenURL: tokenURL,
		},
	}, nil
}

// checkSlice validates once, before the server listens, the settings every request needs. Without it a revision
// missing one passes the platform's health check and then fails every request. Registration already needs the build
// and the store, so this adds only the model key and, in main, the computer.
func checkSlice() error {
	for _, name := range []string{"NORI_PROJECT_ID", "FIRESTORE_DATABASE_ID", "TEMPORAL_BUILD_ID", "OPENROUTER_API_KEY"} {
		if _, err := env.Required(name); err != nil {
			return err
		}
	}
	return nil
}

func loadConfig(bounded bool, waker wake.Waker) (config, error) {
	build := os.Getenv("TEMPORAL_BUILD_ID")
	if build == "" {
		return config{}, fmt.Errorf("TEMPORAL_BUILD_ID is required; use an immutable release identifier")
	}
	if bounded && !waker.WorkersNeedWake() {
		return config{}, fmt.Errorf("request worker requires durable worker wake configuration")
	}
	pollWindow, err := time.ParseDuration(env.Get("NORI_WORKER_POLL_WINDOW", "240s"))
	if err != nil || pollWindow <= 0 || pollWindow > 240*time.Second {
		return config{}, fmt.Errorf("NORI_WORKER_POLL_WINDOW must be in (0,240s]")
	}
	idleWindow, err := time.ParseDuration(env.Get("NORI_WORKER_IDLE_WINDOW", "15s"))
	if err != nil || idleWindow <= 0 {
		return config{}, fmt.Errorf("NORI_WORKER_IDLE_WINDOW must be positive")
	}
	caps, err := capabilities()
	if err != nil {
		return config{}, err
	}
	return config{
		build: build, deployment: env.Get("TEMPORAL_DEPLOYMENT_NAME", "nori-task"),
		pollWindow: pollWindow, idleWindow: idleWindow, capabilities: caps,
	}, nil
}
