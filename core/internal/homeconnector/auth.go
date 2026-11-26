package homeconnector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"nori/internal/homeaiwire"
)

var ErrPairRequired = errors.New("pair this connector again")

type Pairing struct {
	UserCode        string    `json:"user_code"`
	VerificationURI string    `json:"verification_uri"`
	ExpiresAt       time.Time `json:"expires_at"`
}

func netParseIP(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func oauthConfig(base string) *oauth2.Config {
	return &oauth2.Config{
		ClientID: "nori-connector",
		Endpoint: oauth2.Endpoint{
			DeviceAuthURL: base + "/oauth/device_authorization",
			TokenURL:      base + "/oauth/token",
			AuthStyle:     oauth2.AuthStyleInParams,
		},
	}
}

func relayHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func Pair(ctx context.Context, cfg Config, deviceName string, notify func(Pairing) error) error {
	var err error
	cfg, err = cfg.Validate()
	if err != nil {
		return err
	}
	if DesktopMode == "1" {
		if !desktopSharingEnabled(cfg.StateDir) {
			return errors.New("enable home sharing before pairing")
		}
		var cancel context.CancelFunc
		ctx, cancel = desktopSharingContext(ctx, cfg.StateDir)
		defer cancel()
	}
	if strings.TrimSpace(deviceName) == "" {
		deviceName, _ = os.Hostname()
	}
	deviceName = strings.TrimSpace(deviceName)
	if len(deviceName) == 0 || len(deviceName) > 100 {
		return errors.New("device name must be between 1 and 100 characters")
	}
	err = withStateLock(cfg.StateDir, func() error {
		_, err := readCredential(cfg.StateDir)
		if err == nil {
			return errors.New("connector is already paired; unpair it first")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	auth := oauthConfig(cfg.RelayURL)
	oauthCtx := context.WithValue(ctx, oauth2.HTTPClient, relayHTTPClient())
	grant, err := auth.DeviceAuth(oauthCtx, oauth2.SetAuthURLParam("device_name", deviceName))
	if err != nil {
		return fmt.Errorf("start pairing: %w", err)
	}
	if grant.UserCode == "" || grant.DeviceCode == "" || grant.Expiry.IsZero() || grant.Interval < 1 || grant.Interval > 60 {
		return errors.New("relay returned an invalid pairing response")
	}
	if DesktopMode == "1" {
		if err := rememberDesktopGrant(cfg, grant.DeviceCode); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = DesktopRevokePending(cleanupCtx, cfg)
			return err
		}
	}
	verify, err := url.Parse(grant.VerificationURI)
	if err != nil || verify == nil || verify.Hostname() == "" || verify.User != nil || verify.Fragment != "" {
		return errors.New("relay returned an invalid verification URL")
	}
	verifyIP := netParseIP(verify.Hostname())
	if verify.Scheme != "https" && !(verify.Scheme == "http" && (verify.Hostname() == "localhost" || verifyIP)) {
		return errors.New("relay returned an insecure verification URL")
	}
	if err := notify(Pairing{UserCode: grant.UserCode, VerificationURI: grant.VerificationURI, ExpiresAt: grant.Expiry}); err != nil {
		return err
	}
	token, err := auth.DeviceAccessToken(oauthCtx, grant)
	if err != nil {
		return fmt.Errorf("pair connector: %w", err)
	}
	if token.AccessToken == "" || token.RefreshToken == "" || token.Expiry.IsZero() {
		return errors.New("relay returned incomplete credentials")
	}
	credential := Credential{RelayURL: cfg.RelayURL, AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, Expiry: token.Expiry}
	return storePairedCredential(cfg, credential, systemCredentialStore{})
}

func storePairedCredential(cfg Config, credential Credential, store credentialStore) error {
	err := withStateLock(cfg.StateDir, func() error {
		if DesktopMode == "1" && !desktopSharingEnabled(cfg.StateDir) {
			return errors.New("home sharing was turned off while pairing")
		}
		_, err := store.read(cfg.StateDir)
		if err == nil {
			return errors.New("another pairing completed on this machine")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := store.save(cfg.StateDir, credential); err != nil {
			return err
		}
		if DesktopMode == "1" {
			// A saved device credential makes the grant record safe to discard.
			// A failed deletion is recovered when the desktop next checks.
			_ = store.remove(cfg.StateDir + ".pairing")
		}
		return nil
	})
	if err == nil {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if revokeErr := revokeCredential(cleanupCtx, credential); revokeErr != nil {
		return fmt.Errorf("%w; disconnect this device in More → Models", err)
	}
	return err
}

type credentialStore interface {
	read(string) (Credential, error)
	save(string, Credential) error
	remove(string) error
}

type systemCredentialStore struct{}

func (systemCredentialStore) read(dir string) (Credential, error) { return readCredential(dir) }
func (systemCredentialStore) save(dir string, credential Credential) error {
	return saveCredential(dir, credential)
}
func (systemCredentialStore) remove(dir string) error { return deleteCredential(dir) }

func AccessToken(ctx context.Context, cfg Config) (Credential, error) {
	return accessToken(ctx, cfg, systemCredentialStore{}, false)
}

func ForceRefresh(ctx context.Context, cfg Config) (Credential, error) {
	return accessToken(ctx, cfg, systemCredentialStore{}, true)
}

func accessToken(ctx context.Context, cfg Config, store credentialStore, force bool) (Credential, error) {
	var credential Credential
	err := withStateLock(cfg.StateDir, func() error {
		var err error
		credential, err = store.read(cfg.StateDir)
		if errors.Is(err, os.ErrNotExist) {
			return ErrPairRequired
		}
		if err != nil {
			return err
		}
		if credential.RelayURL != cfg.RelayURL {
			return errors.New("connector is paired to a different relay")
		}
		if credential.AccessToken == "" || credential.RefreshToken == "" || credential.Expiry.IsZero() {
			return ErrPairRequired
		}
		if !force && time.Until(credential.Expiry) > homeaiwire.RefreshLead {
			return nil
		}
		ctx = context.WithValue(ctx, oauth2.HTTPClient, relayHTTPClient())
		token := &oauth2.Token{AccessToken: credential.AccessToken, RefreshToken: credential.RefreshToken, Expiry: time.Now().Add(-time.Minute)}
		fresh, err := oauthConfig(cfg.RelayURL).TokenSource(ctx, token).Token()
		if err != nil {
			var oauthErr *oauth2.RetrieveError
			if errors.As(err, &oauthErr) && (oauthErr.ErrorCode == "invalid_grant" || oauthErr.ErrorCode == "access_denied") {
				_ = store.remove(cfg.StateDir)
				return fmt.Errorf("%w: credential refresh rejected", ErrPairRequired)
			}
			return errors.New("connector credential refresh is temporarily unavailable")
		}
		if fresh.AccessToken == "" || fresh.RefreshToken == "" || fresh.Expiry.IsZero() || fresh.RefreshToken == credential.RefreshToken {
			_ = store.remove(cfg.StateDir)
			return fmt.Errorf("%w: invalid credential refresh", ErrPairRequired)
		}
		credential.AccessToken, credential.RefreshToken, credential.Expiry = fresh.AccessToken, fresh.RefreshToken, fresh.Expiry
		if err := store.save(cfg.StateDir, credential); err != nil {
			_ = store.remove(cfg.StateDir)
			return fmt.Errorf("%w: save credentials: %v", ErrPairRequired, err)
		}
		return nil
	})
	return credential, err
}

func Unpair(ctx context.Context, cfg Config) error {
	var err error
	cfg, err = cfg.Validate()
	if err != nil {
		return err
	}
	return withStateLock(cfg.StateDir, func() error {
		credential, err := readCredential(cfg.StateDir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if credential.RelayURL != cfg.RelayURL {
			return errors.New("connector is paired to a different relay")
		}
		if err := revokeCredential(ctx, credential); err != nil {
			return err
		}
		if err := deleteCredential(cfg.StateDir); err != nil {
			return err
		}
		return writeStatus(cfg.StateDir, Status{State: "unpaired", UpdatedAt: time.Now().UTC()})
	})
}

func revokeCredential(ctx context.Context, credential Credential) error {
	form := url.Values{"client_id": {"nori-connector"}, "token": {credential.RefreshToken}}
	return revokeWithForm(ctx, credential.RelayURL, form)
}

func revokeDeviceGrant(ctx context.Context, relayURL, deviceCode string) error {
	form := url.Values{
		"client_id":       {"nori-connector"},
		"token":           {deviceCode},
		"token_type_hint": {"device_code"},
	}
	return revokeWithForm(ctx, relayURL, form)
}

func revokeWithForm(ctx context.Context, relayURL string, form url.Values) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, relayURL+"/oauth/revoke", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := relayHTTPClient().Do(req)
	if err != nil {
		return errors.New("unable to reach the connector service")
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("revoke connector: HTTP %d", resp.StatusCode)
	}
	return nil
}

func CredentialPresent(cfg Config) (bool, error) {
	present := false
	err := withStateLock(cfg.StateDir, func() error {
		credential, err := readCredential(cfg.StateDir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		present = credential.RelayURL == cfg.RelayURL && credential.RefreshToken != ""
		return nil
	})
	return present, err
}
