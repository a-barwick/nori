package settings

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

type UserProvisioningMode string

const (
	UserProvisioningManual UserProvisioningMode = "manual"
	UserProvisioningInvite UserProvisioningMode = "invite"
	UserProvisioningOpen   UserProvisioningMode = "open"
)

func ParseUserProvisioningMode(raw string) (UserProvisioningMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(UserProvisioningManual):
		return UserProvisioningManual, nil
	case string(UserProvisioningInvite):
		return UserProvisioningInvite, nil
	case string(UserProvisioningOpen):
		return UserProvisioningOpen, nil
	default:
		return "", fmt.Errorf("invalid NORI_USER_PROVISIONING %q (want manual, invite, or open)", raw)
	}
}

func LoadUserProvisioningMode() (UserProvisioningMode, error) {
	return ParseUserProvisioningMode(os.Getenv("NORI_USER_PROVISIONING"))
}

func UserProvisioningModeFromEnv() UserProvisioningMode {
	mode, err := LoadUserProvisioningMode()
	if err != nil {
		slog.Warn("invalid NORI_USER_PROVISIONING; falling back to manual", "err", err)
		return UserProvisioningManual
	}
	return mode
}
