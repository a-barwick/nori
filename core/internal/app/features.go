package app

import "nori/internal/settings"

func validateStartup(cfg settings.Settings) error {
	outerKeyName, outerKey := cfg.Models.OuterCredential()
	if _, err := settings.Required(outerKeyName, outerKey); err != nil {
		return err
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
	return nil
}
