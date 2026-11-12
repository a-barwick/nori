package app

import "nori/internal/settings"

// Background tasks are the assistant mode. Disabling them leaves independent
// chats available without Temporal, Jev or an inner-worker key.
func validateStartup(cfg settings.Settings) error {
	if !cfg.Models.UsesGateway() {
		if _, err := settings.Required("OPENAI_API_KEY", cfg.Models.OpenAIKey); err != nil {
			return err
		}
	}
	if cfg.Features.DisableBackgroundTasks {
		return nil
	}
	if !cfg.Features.DisableJev {
		if _, err := settings.Required("JEV_API_KEY", cfg.Models.JevKey); err != nil {
			return err
		}
	}
	taskKeyName, taskKey := cfg.Models.TaskCredential()
	if _, err := settings.Required(taskKeyName, taskKey); err != nil {
		return err
	}
	return cfg.Temporal.Validate()
}
