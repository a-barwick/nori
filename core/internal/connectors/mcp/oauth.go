package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
	"nori/internal/connections"
	"nori/internal/secret"
	"nori/internal/state"
)

const (
	CollectionAuthAttempts = "mcp_auth_attempts"
	authAttemptLifetime    = 15 * time.Minute
)

var ErrNoRegistration = errors.New("this MCP server's sign-in needs an app registered with the provider; it does not offer dynamic client registration")

type oauthCredential struct {
	Token           *oauth2.Token `json:"token"`
	CredentialEpoch string        `json:"credential_epoch,omitempty"`
	ClientID        string        `json:"client_id"`
	ClientSecret    string        `json:"client_secret,omitempty"`
	AuthURL         string        `json:"auth_url"`
	TokenURL        string        `json:"token_url"`
	RevokeURL       string        `json:"revoke_url,omitempty"`
	Resource        string        `json:"resource"`
	Scopes          []string      `json:"scopes,omitempty"`
}

func (c oauthCredential) config(redirect string) *oauth2.Config {
	return &oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, RedirectURL: redirect, Scopes: c.Scopes,
		Endpoint: oauth2.Endpoint{AuthURL: c.AuthURL, TokenURL: c.TokenURL}}
}

type authAttempt struct {
	ID         string          `json:"id"`
	ServerID   string          `json:"server_id"`
	Verifier   string          `json:"verifier"`
	Credential oauthCredential `json:"credential"`
	ReturnTo   string          `json:"return_to"`
	ExpiresAt  time.Time       `json:"expires_at"`
}

type AuthAttempt struct {
	ID           string    `json:"id"`
	AuthorizeURL string    `json:"authorize_url"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func stateKey(value string) string {
	sum := sha256.Sum256([]byte("nori-mcp-oauth-state:" + value))
	return "mca_" + hex.EncodeToString(sum[:20])
}

func (s *Service) httpClient() *http.Client {
	return s.newHTTPClient(30 * time.Second)
}

func (s *Service) discoverAuth(ctx context.Context, endpoint string) (*oauthex.ProtectedResourceMetadata, *oauthex.AuthServerMeta, error) {
	if err := s.validEndpoint(endpoint); err != nil {
		return nil, nil, err
	}
	client := s.httpClient()
	var candidates []string
	if req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)); err == nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			if challenges, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate")); err == nil {
				for _, c := range challenges {
					if strings.EqualFold(c.Scheme, "bearer") && c.Params["resource_metadata"] != "" {
						candidates = append(candidates, c.Params["resource_metadata"])
					}
				}
			}
		}
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, nil, err
	}
	origin := u.Scheme + "://" + u.Host
	if path := strings.TrimSuffix(u.Path, "/"); path != "" {
		candidates = append(candidates, origin+"/.well-known/oauth-protected-resource"+path)
	}
	candidates = append(candidates, origin+"/.well-known/oauth-protected-resource")
	var prm *oauthex.ProtectedResourceMetadata
	var lastErr error
	for _, candidate := range candidates {
		if err := s.validEndpoint(candidate); err != nil {
			lastErr = err
			continue
		}
		if prm, lastErr = oauthex.GetProtectedResourceMetadata(ctx, candidate, endpoint, client); lastErr == nil && prm != nil {
			break
		}
	}
	if prm == nil || len(prm.AuthorizationServers) == 0 {
		return nil, nil, fmt.Errorf("%w: the server publishes no protected-resource metadata naming an authorization server (%v)", ErrUnsupported, lastErr)
	}
	issuer := strings.TrimSuffix(prm.AuthorizationServers[0], "/")
	if err := s.validEndpoint(issuer); err != nil {
		return nil, nil, fmt.Errorf("%w: the authorization server URL is not allowed (%v)", ErrUnsupported, err)
	}
	iu, err := url.Parse(issuer)
	if err != nil {
		return nil, nil, err
	}
	asOrigin, asPath := iu.Scheme+"://"+iu.Host, strings.TrimSuffix(iu.Path, "/")
	for _, metadata := range []string{asOrigin + "/.well-known/oauth-authorization-server" + asPath, asOrigin + "/.well-known/openid-configuration" + asPath, issuer + "/.well-known/openid-configuration"} {
		meta, err := oauthex.GetAuthServerMeta(ctx, metadata, issuer, client)
		if err == nil && meta != nil && meta.AuthorizationEndpoint != "" && meta.TokenEndpoint != "" {
			if err := s.validEndpoint(meta.AuthorizationEndpoint); err != nil {
				lastErr = err
				continue
			}
			if err := s.validEndpoint(meta.TokenEndpoint); err != nil {
				lastErr = err
				continue
			}
			if meta.RegistrationEndpoint != "" {
				if err := s.validEndpoint(meta.RegistrationEndpoint); err != nil {
					lastErr = err
					continue
				}
			}
			if meta.RevocationEndpoint != "" {
				if err := s.validEndpoint(meta.RevocationEndpoint); err != nil {
					meta.RevocationEndpoint = ""
				}
			}
			return prm, meta, nil
		}
		lastErr = err
	}
	return nil, nil, fmt.Errorf("%w: the authorization server's metadata could not be read (%v)", ErrUnsupported, lastErr)
}

const iosOAuthCallbackURL = connections.IOSOAuthCallbackURL

func isIOSOAuthCallback(raw string) bool {
	return connections.IsIOSOAuthCallback(raw)
}

func validReturnTo(returnTo string) bool {
	return connections.ValidReturnTo(returnTo)
}

func safeReturnTo(returnTo string) string {
	return connections.SafeReturnTo(returnTo)
}

func buildReturnRedirect(returnTo string, params url.Values) string {
	return connections.BuildReturnRedirect(returnTo, params)
}

func (s *Service) pruneExpiredAttempts(ctx context.Context) {
	now := s.now()
	after := ""
	for range 10 {
		page, next, err := s.Store.Resources(ctx, CollectionAuthAttempts, nil, after, 100)
		if err != nil || len(page) == 0 {
			return
		}
		for _, r := range page {
			var attempt authAttempt
			if err := r.Decode(&attempt); err != nil || now.After(attempt.ExpiresAt) {
				key := r.ID
				_, _ = s.Store.Mutate(ctx, state.Mutation{
					RequestID:         "mcp-prune-" + uuid.NewString(),
					Method:            "DELETE",
					Target:            key,
					Payload:           key,
					Collection:        CollectionAuthAttempts,
					ID:                key,
					SkipRevisionCheck: true,
					Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
						if current == nil {
							return state.MutationResult{Receipt: state.MutationReceipt{Status: "applied"}}, nil
						}
						var latest authAttempt
						if err := current.Decode(&latest); err == nil && !now.After(latest.ExpiresAt) {
							return state.MutationResult{Receipt: state.MutationReceipt{Status: "applied"}}, nil
						}
						return state.MutationResult{
							Deletes: []state.ResourceKey{{Collection: CollectionAuthAttempts, ID: key}},
							Receipt: state.MutationReceipt{Status: "applied"},
						}, nil
					},
				})
			}
		}
		if next == "" {
			return
		}
		after = next
	}
}

func (s *Service) SignIn(ctx context.Context, id, returnTo string) (AuthAttempt, error) {
	if returnTo == "" {
		returnTo = "/apps"
	} else if !validReturnTo(returnTo) {
		return AuthAttempt{}, fmt.Errorf("%w: invalid return_to path", state.ErrInvalidResource)
	}
	returnTo = safeReturnTo(returnTo)
	server, _, err := s.server(ctx, id)
	if err != nil {
		return AuthAttempt{}, err
	}
	if server.Transport != "http" {
		return AuthAttempt{}, fmt.Errorf("%w: only remote servers sign in with OAuth", ErrUnsupported)
	}
	if s.RedirectURL == "" {
		return AuthAttempt{}, fmt.Errorf("%w: NORI_OAUTH_REDIRECT_URL is not configured", ErrUnsupported)
	}
	if s.Credentials == nil {
		return AuthAttempt{}, secret.ErrCredentialUnavailable
	}
	if _, err := s.Credentials.Get(ctx, "mcp-"+id); errors.Is(err, secret.ErrCredentialUnavailable) {
		return AuthAttempt{}, err
	}
	prm, meta, err := s.discoverAuth(ctx, server.Endpoint)
	if err != nil {
		return AuthAttempt{}, err
	}
	credential := oauthCredential{CredentialEpoch: server.CredentialEpoch, AuthURL: meta.AuthorizationEndpoint, TokenURL: meta.TokenEndpoint, RevokeURL: meta.RevocationEndpoint,
		Resource: prm.Resource, Scopes: prm.ScopesSupported}
	if credential.Resource == "" {
		credential.Resource = server.Endpoint
	}
	if stored, err := s.oauthCredential(ctx, id); err == nil && stored.CredentialEpoch == server.CredentialEpoch && stored.ClientID != "" && stored.TokenURL == meta.TokenEndpoint {
		credential.ClientID, credential.ClientSecret = stored.ClientID, stored.ClientSecret
	}
	if credential.ClientID == "" {
		if meta.RegistrationEndpoint == "" {
			return AuthAttempt{}, ErrNoRegistration
		}
		registered, err := oauthex.RegisterClient(ctx, meta.RegistrationEndpoint, &oauthex.ClientRegistrationMetadata{
			RedirectURIs: []string{s.RedirectURL}, ClientName: "Nori", GrantTypes: []string{"authorization_code", "refresh_token"},
			ResponseTypes: []string{"code"}, TokenEndpointAuthMethod: "none",
		}, s.httpClient())
		if err != nil {
			return AuthAttempt{}, fmt.Errorf("dynamic client registration failed: %w", err)
		}
		credential.ClientID, credential.ClientSecret = registered.ClientID, registered.ClientSecret
	}
	s.pruneExpiredAttempts(ctx)
	stateValue := randomToken()
	verifier := oauth2.GenerateVerifier()
	attempt := authAttempt{ID: stateKey(stateValue), ServerID: id, Verifier: verifier, Credential: credential, ReturnTo: returnTo,
		ExpiresAt: s.now().Add(authAttemptLifetime)}
	r, err := state.NewResource(CollectionAuthAttempts, attempt.ID, 0, attempt, map[string]string{"server_id": id}, s.now())
	if err != nil {
		return AuthAttempt{}, err
	}
	if _, err := s.Store.PutResource(ctx, r, 0); err != nil {
		return AuthAttempt{}, err
	}
	authorize := credential.config(s.RedirectURL).AuthCodeURL(stateValue, oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("resource", credential.Resource))
	return AuthAttempt{ID: attempt.ID, AuthorizeURL: authorize, ExpiresAt: attempt.ExpiresAt}, nil
}

var ErrNotMCPAttempt = errors.New("not an MCP sign-in")

func (s *Service) Callback(ctx context.Context, stateValue, code, providerError string) (string, error) {
	key := stateKey(stateValue)
	var attempt authAttempt
	_, err := s.Store.Mutate(ctx, state.Mutation{
		RequestID:         "mcp-cb-" + uuid.NewString(),
		Method:            "DELETE",
		Target:            key,
		Payload:           key,
		Collection:        CollectionAuthAttempts,
		ID:                key,
		SkipRevisionCheck: true,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, ErrNotMCPAttempt
			}
			var decoded authAttempt
			if err := current.Decode(&decoded); err != nil {
				return state.MutationResult{}, err
			}
			attempt = decoded
			return state.MutationResult{
				Deletes: []state.ResourceKey{{Collection: CollectionAuthAttempts, ID: key}},
				Receipt: state.MutationReceipt{Status: "applied"},
			}, nil
		},
	})
	if err != nil {
		return "", err
	}
	s.pruneExpiredAttempts(ctx)
	redirect := func(params url.Values) string {
		return buildReturnRedirect(attempt.ReturnTo, params)
	}
	fail := func(reason string) (string, error) {
		return redirect(url.Values{"mcp": {attempt.ServerID}, "result": {"failed"}, "reason": {reason}}), nil
	}
	if s.now().After(attempt.ExpiresAt) {
		return fail("expired")
	}
	if providerError != "" {
		return fail(providerError)
	}
	if err := s.checkCredentialEpoch(ctx, attempt.ServerID, attempt.Credential.CredentialEpoch); err != nil {
		return fail("server_changed")
	}
	exchange, cancel := context.WithTimeout(context.WithValue(ctx, oauth2.HTTPClient, s.httpClient()), 30*time.Second)
	defer cancel()
	token, err := attempt.Credential.config(s.RedirectURL).Exchange(exchange, code, oauth2.VerifierOption(attempt.Verifier),
		oauth2.SetAuthURLParam("resource", attempt.Credential.Resource))
	if err != nil {
		return fail("token_exchange")
	}
	server, _, err := s.server(ctx, attempt.ServerID)
	if err != nil || server.Transport != "http" || server.CredentialEpoch != attempt.Credential.CredentialEpoch {
		return fail("server_changed")
	}
	server, err = s.advanceCredentialEpoch(ctx, server)
	if err != nil {
		return fail("server_changed")
	}
	credential := attempt.Credential
	credential.CredentialEpoch = server.CredentialEpoch
	credential.Token = token
	if err := s.storeOAuth(ctx, attempt.ServerID, credential); err != nil {
		if isIOSOAuthCallback(attempt.ReturnTo) {
			return fail("credential_store")
		}
		return "", err
	}
	if err := s.setAuth(ctx, attempt.ServerID, "oauth", credential.CredentialEpoch); err != nil {
		if isIOSOAuthCallback(attempt.ReturnTo) {
			return fail("save_failed")
		}
		return "", err
	}
	if _, err := s.Discover(ctx, attempt.ServerID); err != nil {
		return fail("discovery")
	}
	if s.OnSignedIn != nil {
		s.OnSignedIn(context.WithoutCancel(ctx), attempt.ServerID)
	}
	return redirect(url.Values{"mcp": {attempt.ServerID}, "result": {"connected"}}), nil
}

func (s *Service) oauthCredentialWithVersion(ctx context.Context, id string) (oauthCredential, int64, error) {
	if s.Credentials == nil {
		return oauthCredential{}, 0, secret.ErrCredentialUnavailable
	}
	stored, err := s.Credentials.Get(ctx, "mcp-"+id)
	if err != nil {
		return oauthCredential{}, 0, err
	}
	var c oauthCredential
	if err := json.Unmarshal(stored.Data, &c); err != nil || c.TokenURL == "" {
		return oauthCredential{}, 0, errors.New("the stored credential is not an OAuth credential")
	}
	return c, stored.Version, nil
}

func (s *Service) oauthCredential(ctx context.Context, id string) (oauthCredential, error) {
	c, _, err := s.oauthCredentialWithVersion(ctx, id)
	return c, err
}

func (s *Service) storeOAuth(ctx context.Context, id string, c oauthCredential) error {
	if s.Credentials == nil {
		return secret.ErrCredentialUnavailable
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	for range 3 {
		if err := s.checkCredentialEpoch(ctx, id, c.CredentialEpoch); err != nil {
			return err
		}
		version := int64(0)
		if current, err := s.Credentials.Get(ctx, "mcp-"+id); err == nil {
			version = current.Version
		} else if !errors.Is(err, secret.ErrCredentialNotFound) {
			return err
		}
		if err := s.checkCredentialEpoch(ctx, id, c.CredentialEpoch); err != nil {
			return err
		}
		_, err = s.Credentials.Replace(ctx, "mcp-"+id, version, data)
		if !errors.Is(err, secret.ErrCredentialConflict) {
			return err
		}
	}
	return secret.ErrCredentialConflict
}

func (s *Service) checkCredentialEpoch(ctx context.Context, id, epoch string) error {
	server, _, err := s.server(ctx, id)
	if err != nil {
		return err
	}
	if server.Transport != "http" || server.CredentialEpoch != epoch {
		return errors.New("the MCP server configuration changed")
	}
	return nil
}

func (s *Service) setAuth(ctx context.Context, id, auth, epoch string) error {
	_, err := s.Store.Mutate(ctx, state.Mutation{RequestID: "mcp-auth-" + id + "-" + uuid.NewString(), Method: "POST", Target: "/v1/mcp/" + id,
		Payload: auth, Collection: CollectionServers, ID: id, SkipRevisionCheck: true,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			server, err := decode(*current)
			if err != nil {
				return state.MutationResult{}, err
			}
			if server.Transport != "http" || server.CredentialEpoch != epoch {
				return state.MutationResult{}, state.ErrRevisionConflict
			}
			server.Auth = auth
			return s.write(server, current.Revision+1, 200)
		}})
	return err
}

func (s *Service) oauthTransport(ctx context.Context, server stored, base http.RoundTripper) (http.RoundTripper, error) {
	c, version, err := s.oauthCredentialWithVersion(ctx, server.ID)
	if err != nil {
		return nil, fmt.Errorf("the server needs sign-in: %w", err)
	}
	if c.Token == nil {
		return nil, errors.New("the server needs sign-in: stored OAuth token is missing")
	}
	if c.CredentialEpoch != server.CredentialEpoch {
		return nil, errors.New("the server's credential does not match its current configuration")
	}
	refreshClient := &http.Client{
		Transport:     base,
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	refreshCtx := context.WithValue(context.WithoutCancel(ctx), oauth2.HTTPClient, refreshClient)
	source := &savingSource{
		service:    s,
		serverID:   server.ID,
		refreshCtx: refreshCtx,
		cred:       c,
		version:    version,
		inner:      c.config(s.RedirectURL).TokenSource(refreshCtx, c.Token),
		last:       c.Token.AccessToken,
	}
	return &oauth2.Transport{Source: source, Base: base}, nil
}

type savingSource struct {
	mu         sync.Mutex
	service    *Service
	serverID   string
	refreshCtx context.Context
	cred       oauthCredential
	version    int64
	inner      oauth2.TokenSource
	last       string
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.inner.Token()
	checkCtx, cancel := context.WithTimeout(s.refreshCtx, 5*time.Second)
	checkErr := s.service.checkCredentialEpoch(checkCtx, s.serverID, s.cred.CredentialEpoch)
	cancel()
	if checkErr != nil {
		return nil, checkErr
	}
	if err != nil {
		latest, latestVersion, reloadErr := s.service.oauthCredentialWithVersion(s.refreshCtx, s.serverID)
		if reloadErr == nil && latest.CredentialEpoch == s.cred.CredentialEpoch && latestVersion != s.version && latest.Token != nil && latest.Token.Valid() {
			s.cred = latest
			s.version = latestVersion
			s.inner = latest.config(s.service.RedirectURL).TokenSource(s.refreshCtx, latest.Token)
			s.last = latest.Token.AccessToken
			return latest.Token, nil
		}
		return nil, err
	}
	if t == nil {
		return nil, errors.New("OAuth token source returned nil token")
	}
	if t.AccessToken != s.last {
		updated := s.cred
		updated.Token = t
		data, marshalErr := json.Marshal(updated)
		if marshalErr == nil {
			nextVersion, replaceErr := s.service.Credentials.Replace(s.refreshCtx, "mcp-"+s.serverID, s.version, data)
			if replaceErr == nil {
				s.cred = updated
				s.version = nextVersion
				s.last = t.AccessToken
				return t, nil
			}
			if errors.Is(replaceErr, secret.ErrCredentialConflict) {
				latest, latestVersion, reloadErr := s.service.oauthCredentialWithVersion(s.refreshCtx, s.serverID)
				if reloadErr == nil && latest.CredentialEpoch == s.cred.CredentialEpoch && latest.Token != nil {
					s.cred = latest
					s.version = latestVersion
					s.inner = latest.config(s.service.RedirectURL).TokenSource(s.refreshCtx, latest.Token)
					s.last = latest.Token.AccessToken
					if latest.Token.Valid() {
						return latest.Token, nil
					}
					if t.RefreshToken == "" && latest.Token.RefreshToken != "" {
						copied := *t
						copied.RefreshToken = latest.Token.RefreshToken
						t = &copied
					}
					updated = latest
					updated.Token = t
					if retryData, err := json.Marshal(updated); err == nil {
						if retryVersion, err := s.service.Credentials.Replace(s.refreshCtx, "mcp-"+s.serverID, latestVersion, retryData); err == nil {
							s.cred = updated
							s.version = retryVersion
							s.inner = updated.config(s.service.RedirectURL).TokenSource(s.refreshCtx, t)
							s.last = t.AccessToken
						}
					}
				}
			}
		}
		s.last = t.AccessToken
	}
	return t, nil
}

func (s *Service) SignOut(ctx context.Context, id string) error {
	server, _, err := s.server(ctx, id)
	if err != nil {
		return err
	}
	if s.Credentials == nil {
		return secret.ErrCredentialUnavailable
	}
	credential, credentialErr := s.Credentials.Get(ctx, "mcp-"+id)
	if credentialErr != nil && !errors.Is(credentialErr, secret.ErrCredentialNotFound) {
		return credentialErr
	}
	nextEpoch := uuid.NewString()
	_, err = s.Store.Mutate(ctx, state.Mutation{
		RequestID: "mcp-signout-" + id + "-" + uuid.NewString(), Method: "POST", Target: "/v1/mcp/" + id,
		Payload: "sign_out", Collection: CollectionServers, ID: id, SkipRevisionCheck: true,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			latest, err := decode(*current)
			if err != nil {
				return state.MutationResult{}, err
			}
			if latest.CredentialEpoch != server.CredentialEpoch {
				return state.MutationResult{}, state.ErrRevisionConflict
			}
			latest.CredentialEpoch = nextEpoch
			if latest.Auth == "oauth" && latest.Enabled {
				latest.Observed.Status = "needs_sign_in"
			} else if latest.Auth == "configured" && latest.Enabled {
				latest.Observed.Status = "disconnected"
			}
			return s.write(latest, current.Revision+1, 200)
		},
	})
	if err != nil {
		return err
	}
	var c oauthCredential
	if credentialErr == nil && json.Unmarshal(credential.Data, &c) == nil && c.CredentialEpoch == server.CredentialEpoch && c.RevokeURL != "" && c.Token != nil {
		if s.validEndpoint(c.RevokeURL) == nil {
			value := c.Token.RefreshToken
			if value == "" {
				value = c.Token.AccessToken
			}
			if value != "" {
				form := url.Values{"token": {value}, "client_id": {c.ClientID}}
				if req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.RevokeURL, strings.NewReader(form.Encode())); err == nil {
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					if resp, err := s.httpClient().Do(req); err == nil {
						resp.Body.Close()
					}
				}
			}
		}
	}
	if credentialErr == nil {
		return s.deleteCredentialVersion(ctx, id, credential)
	}
	return nil
}

func randomToken() string {
	data := make([]byte, 32)
	_, _ = rand.Read(data)
	return base64.RawURLEncoding.EncodeToString(data)
}
