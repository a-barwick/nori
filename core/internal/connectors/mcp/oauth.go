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

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
	"nori/internal/secret"
	"nori/internal/state"
)

// OAuth sign-in for remote MCP servers, per the MCP authorization specification: the server's protected-resource
// metadata names its authorization server; that server's metadata gives the endpoints; Nori registers itself
// dynamically when the server offers registration, then runs the authorization code flow with PKCE and the resource
// parameter. The token, its refresh token and the client registration are the server's credential in the credential
// store; refreshed tokens are written back, and sign-out revokes when the server offers revocation.

const (
	CollectionAuthAttempts = "mcp_auth_attempts"
	authAttemptLifetime    = 15 * time.Minute
)

// ErrNoRegistration is a server whose authorization server neither offers dynamic registration nor has a client
// configured for Nori.
var ErrNoRegistration = errors.New("this MCP server's sign-in needs an app registered with the provider; it does not offer dynamic client registration")

// oauthCredential is what Nori stores for an OAuth-signed-in server.
type oauthCredential struct {
	Token        *oauth2.Token `json:"token"`
	ClientID     string        `json:"client_id"`
	ClientSecret string        `json:"client_secret,omitempty"`
	AuthURL      string        `json:"auth_url"`
	TokenURL     string        `json:"token_url"`
	RevokeURL    string        `json:"revoke_url,omitempty"`
	Resource     string        `json:"resource"`
	Scopes       []string      `json:"scopes,omitempty"`
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

// AuthAttempt is the sign-in step the client opens, as in the contract AuthAttempt.
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

// discoverAuth finds the authorization server for an MCP endpoint: first from the WWW-Authenticate challenge of an
// unauthenticated request, then from the well-known protected-resource locations.
func (s *Service) discoverAuth(ctx context.Context, endpoint string) (*oauthex.ProtectedResourceMetadata, *oauthex.AuthServerMeta, error) {
	if err := s.validEndpoint(endpoint); err != nil {
		return nil, nil, err
	}
	client := s.httpClient()
	var candidates []string
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
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

// SignIn starts OAuth sign-in for a server and returns the authorization step to open.
func (s *Service) SignIn(ctx context.Context, id, returnTo string) (AuthAttempt, error) {
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
	if _, err := s.Credentials.Get(ctx, "mcp-"+id); errors.Is(err, secret.ErrCredentialUnavailable) {
		return AuthAttempt{}, err
	}
	prm, meta, err := s.discoverAuth(ctx, server.Endpoint)
	if err != nil {
		return AuthAttempt{}, err
	}
	credential := oauthCredential{AuthURL: meta.AuthorizationEndpoint, TokenURL: meta.TokenEndpoint, RevokeURL: meta.RevocationEndpoint,
		Resource: prm.Resource, Scopes: prm.ScopesSupported}
	if credential.Resource == "" {
		credential.Resource = server.Endpoint
	}
	// Reuse the registration from an earlier sign-in to the same authorization server.
	if stored, err := s.oauthCredential(ctx, id); err == nil && stored.ClientID != "" && stored.TokenURL == meta.TokenEndpoint {
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
	if returnTo == "" || !strings.HasPrefix(returnTo, "/") || strings.HasPrefix(returnTo, "//") {
		returnTo = "/apps"
	}
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

// ErrNotMCPAttempt says a callback state belongs to no MCP sign-in, so another flow may own it.
var ErrNotMCPAttempt = errors.New("not an MCP sign-in")

// Callback completes a sign-in: it exchanges the code with the PKCE verifier and resource, stores the credential,
// marks the server OAuth-signed-in and discovers its tools. It returns the path to send the browser to.
func (s *Service) Callback(ctx context.Context, stateValue, code, providerError string) (string, error) {
	key := stateKey(stateValue)
	r, err := s.Store.Resource(ctx, CollectionAuthAttempts, key)
	if errors.Is(err, state.ErrNotFound) {
		return "", ErrNotMCPAttempt
	}
	if err != nil {
		return "", err
	}
	var attempt authAttempt
	if err := r.Decode(&attempt); err != nil {
		return "", err
	}
	// An attempt is used once, whatever happens next.
	_, _ = s.Store.Mutate(ctx, state.Mutation{RequestID: "mcp-callback-" + key[4:40], Method: "DELETE", Target: key, Payload: key,
		Collection: CollectionAuthAttempts, ID: key, SkipRevisionCheck: true,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			return state.MutationResult{Deletes: []state.ResourceKey{{Collection: CollectionAuthAttempts, ID: key}}, Receipt: state.MutationReceipt{Status: "applied"}}, nil
		}})
	fail := func(reason string) (string, error) {
		return attempt.ReturnTo + "?mcp=" + url.QueryEscape(attempt.ServerID) + "&result=failed&reason=" + url.QueryEscape(reason), nil
	}
	if s.now().After(attempt.ExpiresAt) {
		return fail("expired")
	}
	if providerError != "" {
		return fail(providerError)
	}
	exchange, cancel := context.WithTimeout(context.WithValue(ctx, oauth2.HTTPClient, s.httpClient()), 30*time.Second)
	defer cancel()
	token, err := attempt.Credential.config(s.RedirectURL).Exchange(exchange, code, oauth2.VerifierOption(attempt.Verifier),
		oauth2.SetAuthURLParam("resource", attempt.Credential.Resource))
	if err != nil {
		return fail("token_exchange")
	}
	credential := attempt.Credential
	credential.Token = token
	if err := s.storeOAuth(ctx, attempt.ServerID, credential); err != nil {
		return "", err
	}
	if err := s.setAuth(ctx, attempt.ServerID, "oauth"); err != nil {
		return "", err
	}
	if _, err := s.Discover(ctx, attempt.ServerID); err != nil {
		return fail("discovery")
	}
	if s.OnSignedIn != nil {
		s.OnSignedIn(context.WithoutCancel(ctx), attempt.ServerID)
	}
	return attempt.ReturnTo + "?mcp=" + url.QueryEscape(attempt.ServerID) + "&result=connected", nil
}

func (s *Service) oauthCredential(ctx context.Context, id string) (oauthCredential, error) {
	stored, err := s.Credentials.Get(ctx, "mcp-"+id)
	if err != nil {
		return oauthCredential{}, err
	}
	var c oauthCredential
	if err := json.Unmarshal(stored.Data, &c); err != nil || c.TokenURL == "" {
		return oauthCredential{}, errors.New("the stored credential is not an OAuth credential")
	}
	return c, nil
}

func (s *Service) storeOAuth(ctx context.Context, id string, c oauthCredential) error {
	data, _ := json.Marshal(c)
	version := int64(0)
	if current, err := s.Credentials.Get(ctx, "mcp-"+id); err == nil {
		version = current.Version
	} else if !errors.Is(err, secret.ErrCredentialNotFound) {
		return err
	}
	_, err := s.Credentials.Replace(ctx, "mcp-"+id, version, data)
	return err
}

func (s *Service) setAuth(ctx context.Context, id, auth string) error {
	_, err := s.Store.Mutate(ctx, state.Mutation{RequestID: "mcp-auth-" + id + "-" + randomToken()[:12], Method: "POST", Target: "/v1/mcp/" + id,
		Payload: auth, Collection: CollectionServers, ID: id, SkipRevisionCheck: true,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			server, err := decode(*current)
			if err != nil {
				return state.MutationResult{}, err
			}
			server.Auth = auth
			return s.write(server, current.Revision+1, 200)
		}})
	return err
}

// oauthTransport authenticates with the stored token, refreshing it when it expires and writing the refreshed token
// back so the next session starts from it.
func (s *Service) oauthTransport(ctx context.Context, id string, base http.RoundTripper) (http.RoundTripper, error) {
	c, err := s.oauthCredential(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("the server needs sign-in: %w", err)
	}
	refreshClient := &http.Client{
		Transport:     base,
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	refreshCtx := context.WithValue(context.WithoutCancel(ctx), oauth2.HTTPClient, refreshClient)
	source := &savingSource{inner: c.config(s.RedirectURL).TokenSource(refreshCtx, c.Token), last: c.Token.AccessToken, save: func(t *oauth2.Token) {
		c.Token = t
		_ = s.storeOAuth(context.WithoutCancel(ctx), id, c)
	}}
	return &oauth2.Transport{Source: source, Base: base}, nil
}

type savingSource struct {
	mu    sync.Mutex
	inner oauth2.TokenSource
	last  string
	save  func(*oauth2.Token)
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	t, err := s.inner.Token()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.AccessToken != s.last {
		s.last = t.AccessToken
		s.save(t)
	}
	return t, nil
}

// SignOut revokes the token when the authorization server offers revocation (RFC 7009) and deletes Nori's copy.
func (s *Service) SignOut(ctx context.Context, id string) error {
	if c, err := s.oauthCredential(ctx, id); err == nil && c.RevokeURL != "" && c.Token != nil {
		value := c.Token.RefreshToken
		if value == "" {
			value = c.Token.AccessToken
		}
		form := url.Values{"token": {value}, "client_id": {c.ClientID}}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.RevokeURL, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if resp, err := s.httpClient().Do(req); err == nil {
			resp.Body.Close()
		}
	}
	if err := s.Credentials.Delete(ctx, "mcp-"+id); err != nil {
		return err
	}
	return s.setAuth(ctx, id, "oauth")
}

func randomToken() string {
	data := make([]byte, 32)
	_, _ = rand.Read(data)
	return base64.RawURLEncoding.EncodeToString(data)
}
