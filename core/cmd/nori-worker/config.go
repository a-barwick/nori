package main

import (
	"fmt"
	"time"

	"nori/internal/agent"
	"nori/internal/settings"
)

type config struct {
	build, deployment      string
	pollWindow, idleWindow time.Duration
}

func capabilities(s settings.Settings) (agent.Capabilities, error) {
	location, err := time.LoadLocation(s.TimeZone)
	if err != nil {
		return agent.Capabilities{}, fmt.Errorf("user time zone must be an IANA time zone")
	}
	return agent.Capabilities{
		SearchKey: s.SearchKey, Location: location,
	}, nil
}

func loadConfig(s settings.Settings) (config, error) {
	if s.Features.DisableBackgroundTasks {
		return config{}, fmt.Errorf("background tasks are not enabled")
	}
	required := [][2]string{{"TEMPORAL_BUILD_ID", s.Temporal.BuildID}, {"OPENROUTER_API_KEY", s.Models.OpenRouterKey}}
	switch s.Store.Backend {
	case "", "firestore":
		required = append(required, [2]string{"NORI_PROJECT_ID", s.Store.Project}, [2]string{"FIRESTORE_DATABASE_ID", s.Store.Database})
	case "postgres":
		required = append(required, [2]string{"NORI_POSTGRES_URL", s.Store.PostgresURL})
	default:
		return config{}, fmt.Errorf("NORI_STORE_BACKEND must be firestore or postgres")
	}
	for _, setting := range required {
		if _, err := settings.Required(setting[0], setting[1]); err != nil {
			return config{}, err
		}
	}
	if err := s.Temporal.Validate(); err != nil {
		return config{}, err
	}
	if s.Port != "" {
		if _, err := settings.Required("TEMPORAL_DEPLOYMENT_NAME", s.Temporal.DeploymentName); err != nil {
			return config{}, err
		}
		if len(s.Wake.WorkerServices) == 0 {
			return config{}, &settings.MissingError{Name: "NORI_WORKER_SERVICES"}
		}
		if _, err := settings.Required("NORI_WORKER_SERVICES route for TEMPORAL_BUILD_ID", s.Wake.WorkerServices[s.Temporal.BuildID]); err != nil {
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
	return config{
		build: s.Temporal.BuildID, deployment: s.Temporal.EffectiveDeploymentName(),
		pollWindow: pollWindow, idleWindow: idleWindow,
	}, nil
}
