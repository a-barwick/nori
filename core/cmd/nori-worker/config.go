package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"time"

	"nori/internal/agent"
	"nori/internal/platform"
	"nori/internal/settings"
)

// config is the worker's release identity, polling windows and capabilities,
// parsed once before the process opens a listener or connects to providers.
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
	return agent.Capabilities{
		SearchKey: s.SearchKey, Location: location,
		Calendar: agent.CalendarConfig{
			ClientID: s.Calendar.ClientID, ClientSecret: s.Calendar.ClientSecret,
			RefreshToken:  s.Calendar.RefreshToken,
			Account:       s.Calendar.Account,
			WriteCalendar: s.Calendar.WriteID,
		},
	}, nil
}

func loadConfig(s settings.Settings) (config, error) {
	for _, setting := range [][2]string{{"NORI_PROJECT_ID", s.Store.Project}, {"FIRESTORE_DATABASE_ID", s.Store.Database}, {"TEMPORAL_BUILD_ID", s.Temporal.BuildID}, {"OPENROUTER_API_KEY", s.Models.OpenRouterKey}} {
		if _, err := settings.Required(setting[0], setting[1]); err != nil {
			return config{}, err
		}
	}
	if err := s.Temporal.Validate(); err != nil {
		return config{}, err
	}
	if err := platform.CheckComputer(s); err != nil {
		return config{}, err
	}
	if s.Port != "" {
		// Request workers must support registration and durable continuation wakes.
		for _, setting := range [][2]string{{"TEMPORAL_DEPLOYMENT_NAME", s.Temporal.DeploymentName}, {"NORI_WORKER_SERVICES", s.Wake.WorkerServices}} {
			if _, err := settings.Required(setting[0], setting[1]); err != nil {
				return config{}, err
			}
		}
		var routes map[string]string
		if err := json.Unmarshal([]byte(s.Wake.WorkerServices), &routes); err != nil || len(routes) == 0 {
			return config{}, fmt.Errorf("NORI_WORKER_SERVICES must be a nonempty JSON map of worker builds to service URLs")
		}
		if _, err := settings.Required("NORI_WORKER_SERVICES route for TEMPORAL_BUILD_ID", routes[s.Temporal.BuildID]); err != nil {
			return config{}, err
		}
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
		build: s.Temporal.BuildID, deployment: cmp.Or(s.Temporal.DeploymentName, "nori-task"),
		pollWindow: pollWindow, idleWindow: idleWindow, capabilities: caps,
	}, nil
}
