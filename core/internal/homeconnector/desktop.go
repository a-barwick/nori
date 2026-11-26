package homeconnector

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const desktopSharingFile = "sharing-enabled"

func desktopSharingEnabled(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, desktopSharingFile))
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0
}

func desktopSharingContext(parent context.Context, dir string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !desktopSharingEnabled(dir) {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

func DesktopEnable(cfg Config) error {
	return withStateLock(cfg.StateDir, func() error {
		for _, suffix := range []string{".pending-revocation", ".pairing"} {
			if _, err := readCredential(cfg.StateDir + suffix); err == nil {
				return errors.New("connect to Nori to finish disconnecting this Mac before pairing again")
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if !desktopSharingEnabled(cfg.StateDir) {
			if _, err := readCredential(cfg.StateDir); err == nil {
				return errors.New("finish disconnecting this Mac before pairing again")
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return atomicWrite(filepath.Join(cfg.StateDir, desktopSharingFile), []byte("enabled\n"))
	})
}

func rememberDesktopGrant(cfg Config, deviceCode string) error {
	return withStateLock(cfg.StateDir, func() error {
		grantDir := cfg.StateDir + ".pairing"
		if _, err := readCredential(grantDir); err == nil {
			return errors.New("finish the previous pairing before starting another")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		grant := Credential{RelayURL: cfg.RelayURL, RefreshToken: deviceCode}
		if err := saveCredential(grantDir, grant); err != nil {
			return err
		}
		if !desktopSharingEnabled(cfg.StateDir) {
			return errors.New("home sharing was turned off while pairing")
		}
		return nil
	})
}

// DesktopDisable stops future sessions before attempting remote revocation. The
// pending credential remains in the credential store until the relay confirms it.
func DesktopDisable(ctx context.Context, cfg Config) (bool, error) {
	if err := ensureStateDir(cfg.StateDir); err != nil {
		return false, err
	}
	if err := os.Remove(filepath.Join(cfg.StateDir, desktopSharingFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	var pending bool
	err := withStateLock(cfg.StateDir, func() error {
		if err := os.Remove(filepath.Join(cfg.StateDir, desktopSharingFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		var err error
		pending, err = desktopRevokePendingLocked(ctx, cfg, systemCredentialStore{})
		return err
	})
	return pending, err
}

func DesktopRevokePending(ctx context.Context, cfg Config) (bool, error) {
	return desktopRevokePending(ctx, cfg, systemCredentialStore{})
}

func DesktopRevocationPending(cfg Config) (bool, error) {
	return desktopRevocationPending(cfg, systemCredentialStore{})
}

func desktopRevocationPending(cfg Config, store credentialStore) (bool, error) {
	pending := false
	err := withStateLock(cfg.StateDir, func() error {
		_, err := store.read(cfg.StateDir + ".pending-revocation")
		if err == nil {
			pending = true
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		active, activeErr := store.read(cfg.StateDir)
		if activeErr != nil && !errors.Is(activeErr, os.ErrNotExist) {
			return activeErr
		}
		_, grantErr := store.read(cfg.StateDir + ".pairing")
		if grantErr != nil && !errors.Is(grantErr, os.ErrNotExist) {
			return grantErr
		}
		pending = (activeErr == nil && (!desktopSharingEnabled(cfg.StateDir) || active.RelayURL != cfg.RelayURL)) ||
			(grantErr == nil && activeErr != nil)
		return nil
	})
	return pending, err
}

func desktopRevokePending(ctx context.Context, cfg Config, store credentialStore) (bool, error) {
	pending := false
	err := withStateLock(cfg.StateDir, func() error {
		var err error
		pending, err = desktopRevokePendingLocked(ctx, cfg, store)
		return err
	})
	return pending, err
}

func desktopRevokePendingLocked(ctx context.Context, cfg Config, store credentialStore) (bool, error) {
	pendingDir := cfg.StateDir + ".pending-revocation"
	grantDir := cfg.StateDir + ".pairing"
	credential, pendingErr := store.read(pendingDir)
	active, activeErr := store.read(cfg.StateDir)
	grant, grantErr := store.read(grantDir)
	for _, err := range []error{pendingErr, activeErr, grantErr} {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}

	enabled := desktopSharingEnabled(cfg.StateDir)
	if enabled && activeErr == nil && grantErr == nil {
		if err := store.remove(grantDir); err != nil {
			return false, fmt.Errorf("remove completed pairing grant: %w", err)
		}
		grantErr = os.ErrNotExist
	}
	if !enabled && activeErr == nil {
		if pendingErr == nil && credential.RefreshToken != active.RefreshToken {
			return true, errors.New("multiple connector credentials require disconnect recovery")
		}
		if errors.Is(pendingErr, os.ErrNotExist) {
			if err := store.save(pendingDir, active); err != nil {
				return true, err
			}
			credential, pendingErr = active, nil
		}
		if err := store.remove(cfg.StateDir); err != nil {
			return true, err
		}
	}

	pending := false
	if pendingErr == nil {
		pending = true
		if !validStoredRelayOrigin(credential.RelayURL) {
			return true, errors.New("pending connector has an invalid relay address")
		}
		revokeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := revokeCredential(revokeCtx, credential)
		cancel()
		if err == nil {
			if err := store.remove(pendingDir); err != nil {
				return true, fmt.Errorf("remove revoked connector credential: %w", err)
			}
			pending = false
		}
	}
	if grantErr == nil {
		if enabled {
			return true, nil
		}
		if !validStoredRelayOrigin(grant.RelayURL) {
			return true, errors.New("pending pairing has an invalid relay address")
		}
		revokeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := revokeDeviceGrant(revokeCtx, grant.RelayURL, grant.RefreshToken)
		cancel()
		if err != nil {
			return true, nil
		}
		if err := store.remove(grantDir); err != nil {
			return true, fmt.Errorf("remove canceled pairing grant: %w", err)
		}
	}
	return pending, nil
}

func validStoredRelayOrigin(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed == nil || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(parsed.Hostname())
	return parsed.Scheme == "http" && (strings.EqualFold(parsed.Hostname(), "localhost") || ip != nil && ip.IsLoopback())
}

func (c *Connector) serveDesktop(ctx context.Context) error {
	if err := ensureStateDir(c.config.StateDir); err != nil {
		return err
	}
	for ctx.Err() == nil {
		if !desktopSharingEnabled(c.config.StateDir) {
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
			continue
		}

		sessionCtx, cancel := desktopSharingContext(ctx, c.config.StateDir)
		err := c.serve(sessionCtx)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}
