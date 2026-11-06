package main

import (
	"cmp"
	"fmt"
	"strings"
	"time"

	"nori/internal/agent"
	"nori/internal/settings"
	"nori/internal/wake"
)

// config is the worker's release identity, polling windows and capabilities. It is
// checked per run from the startup settings, so a missing release fails the request that needs it.
type config struct {
	build, deployment      string
	pollWindow, idleWindow time.Duration
	capabilities           agent.Capabilities
}

// capabilities reads the credentials of the optional tools. A missing credential
// leaves that tool reporting itself unavailable.
func capabilities(s settings.Settings) (agent.Capabilities, error) {
	location, err := time.LoadLocation(s.TimeZone)
	if err != nil {
		return agent.Capabilities{}, fmt.Errorf("NORI_TIMEZONE must be an IANA time zone")
	}
	// A local test stack points the worker at a loopback provider. Nothing else may.
	base, tokenURL := "", ""
	if address := s.Test.GoogleURL; address != "" {
		if !s.Temporal.Local || !strings.HasPrefix(address, "http://127.0.0.1:") {
			return agent.Capabilities{}, fmt.Errorf("NORI_E2E_GOOGLE_URL requires TEMPORAL_LOCAL=true and a 127.0.0.1 address")
		}
		base, tokenURL = address, address+"/token"
	}
	var offset time.Duration
	if value := s.Test.Now; value != "" {
		at, err := time.Parse(time.RFC3339, value)
		if err != nil || !s.Temporal.Local {
			return agent.Capabilities{}, fmt.Errorf("NORI_E2E_NOW requires TEMPORAL_LOCAL=true and an RFC 3339 time")
		}
		offset = time.Until(at)
	}
	return agent.Capabilities{
		ClockOffset: offset,
		SearchKey:   s.SearchKey, Location: location,
		Calendar: agent.CalendarConfig{
			ClientID: s.Calendar.ClientID, ClientSecret: s.Calendar.ClientSecret,
			RefreshToken:  s.Calendar.RefreshToken,
			Account:       s.Calendar.Account,
			WriteCalendar: s.Calendar.WriteID, APIBase: base, TokenURL: tokenURL,
		},
	}, nil
}

// checkSlice validates once, before the server listens, the settings every request needs. Without it a revision
// missing one passes the platform's health check and then fails every request. Registration already needs the build
// and the store, so this adds only the model key and, in main, the computer.
func checkSlice(s settings.Settings) error {
	for _, setting := range [][2]string{{"NORI_PROJECT_ID", s.Store.Project}, {"FIRESTORE_DATABASE_ID", s.Store.Database}, {"TEMPORAL_BUILD_ID", s.Temporal.BuildID}, {"OPENROUTER_API_KEY", s.Models.OpenRouterKey}} {
		if _, err := settings.Required(setting[0], setting[1]); err != nil {
			return err
		}
	}
	return nil
}

func loadConfig(s settings.Settings, bounded bool, waker wake.Waker) (config, error) {
	build := s.Temporal.BuildID
	if build == "" {
		return config{}, fmt.Errorf("TEMPORAL_BUILD_ID is required; use an immutable release identifier")
	}
	if bounded && !waker.WorkersNeedWake() {
		return config{}, fmt.Errorf("request worker requires durable worker wake configuration")
	}
	pollWindow, err := time.ParseDuration(s.Worker.PollWindow)
	if err != nil || pollWindow <= 0 || pollWindow > 240*time.Second {
		return config{}, fmt.Errorf("NORI_WORKER_POLL_WINDOW must be in (0,240s]")
	}
	idleWindow, err := time.ParseDuration(s.Worker.IdleWindow)
	if err != nil || idleWindow <= 0 {
		return config{}, fmt.Errorf("NORI_WORKER_IDLE_WINDOW must be positive")
	}
	caps, err := capabilities(s)
	if err != nil {
		return config{}, err
	}
	return config{
		build: build, deployment: cmp.Or(s.Temporal.DeploymentName, "nori-task"),
		pollWindow: pollWindow, idleWindow: idleWindow, capabilities: caps,
	}, nil
}
