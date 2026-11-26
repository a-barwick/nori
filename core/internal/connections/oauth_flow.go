package connections

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"nori/internal/extensions"
	"nori/internal/secret"
	"nori/internal/state"
)

func (s *Service) connectPassword(ctx context.Context, id, requestID string, expected int64, action Action, integration PasswordIntegration) (state.MutationReceipt, error) {
	username, password := strings.TrimSpace(action.Username), strings.TrimSpace(action.Password)
	if username == "" || password == "" {
		return state.MutationReceipt{}, fmt.Errorf("%w: this app connects with a username and an app-specific password", state.ErrInvalidResource)
	}
	account, err := integration.VerifyPassword(ctx, s.baseClient(), username, password)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	data, _ := json.Marshal(passwordCredential{Username: username, Password: password})
	stored, err := s.Credentials.Get(ctx, id)
	version := int64(0)
	if err == nil {
		version = stored.Version
	} else if !errors.Is(err, secret.ErrCredentialNotFound) {
		return state.MutationReceipt{}, err
	}
	if _, err := s.Credentials.Replace(ctx, id, version, data); err != nil {
		return state.MutationReceipt{}, err
	}
	info := integration.Info()
	capabilities := []string{}
	for _, c := range info.Capabilities {
		if c.Supported {
			capabilities = append(capabilities, c.ID)
		}
	}
	prefs, _, err := s.Preferences(ctx)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	action.Password = ""
	receipt, err := s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "POST", Target: "/v1/connections/" + id + "/actions", ExpectedRevision: expected, Payload: action,
		Collection: CollectionConnections, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			var record Record
			if err := current.Decode(&record); err != nil {
				return state.MutationResult{}, err
			}
			if record.Status == StatusDisconnecting {
				return state.MutationResult{}, ErrInvalidAction
			}
			if record.Account.ProviderID != "" && record.Account.ProviderID != account.ProviderID {
				return state.MutationResult{}, fmt.Errorf("%w: these credentials belong to a different account than this connection", state.ErrInvalidResource)
			}
			now := s.now()
			record.Account, record.Status, record.Capabilities, record.GrantedScopes = account, StatusConnected, capabilities, []string{}
			record.AuthorizationEpoch++
			record.Health = Health{CheckedAt: &now, Code: HealthReady, Recovery: "none", LastSuccessAt: &now}
			return s.write(record, current.Revision+1, "applied", prefs)
		},
	})
	if err == nil && s.OnConnected != nil {
		s.OnConnected(context.WithoutCancel(ctx), id)
	}
	return receipt, err
}

func randomToken() string {
	data := make([]byte, 32)
	_, _ = rand.Read(data)
	return base64.RawURLEncoding.EncodeToString(data)
}

func stateKey(value string) string {
	sum := sha256.Sum256([]byte("nori-oauth-state:" + value))
	return "att_" + hex.EncodeToString(sum[:20])
}

func (s *Service) scopesFor(integration Integration, capabilities []string, record Record) ([]string, []string, error) {
	info := integration.Info()
	if len(capabilities) == 0 {
		capabilities = record.Capabilities
	}
	if len(capabilities) == 0 {
		for _, capability := range info.Capabilities {
			if capability.Supported && capability.Effect == string(extensions.EffectRead) {
				capabilities = append(capabilities, capability.ID)
			}
		}
	}
	scopes := append([]string{}, info.BaseScopes...)
	for _, want := range capabilities {
		found := false
		for _, capability := range info.Capabilities {
			if capability.ID == want && capability.Supported {
				found = true
				for _, scope := range capability.Scopes {
					if !slices.Contains(scopes, scope) {
						scopes = append(scopes, scope)
					}
				}
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("%w: unknown capability %s", state.ErrInvalidResource, want)
		}
	}
	return scopes, capabilities, nil
}

func (s *Service) connect(ctx context.Context, id, requestID string, expected int64, action Action) (state.MutationReceipt, error) {
	if saved, err := s.Store.Receipt(ctx, requestID); err == nil {
		bound := action
		bound.Password = ""
		return s.replay(ctx, saved, state.Mutation{RequestID: requestID, Method: "POST", Target: "/v1/connections/" + id + "/actions", ExpectedRevision: expected, Payload: bound})
	}
	record, current, err := s.record(ctx, id)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	integration := s.Integrations[record.IntegrationID]
	if integration == nil {
		return state.MutationReceipt{}, ErrUnknownIntegration
	}
	if password, ok := integration.(PasswordIntegration); ok && integration.Info().Auth == "app_password" {
		return s.connectPassword(ctx, id, requestID, expected, action, password)
	}
	config := s.oauthConfig(integration)
	if config == nil {
		return state.MutationReceipt{}, ErrNotConfigured
	}
	if _, err := s.Credentials.Get(ctx, id); errors.Is(err, secret.ErrCredentialUnavailable) {
		return state.MutationReceipt{}, err
	}
	scopes, capabilities, err := s.scopesFor(integration, action.Capabilities, record)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	returnTo := action.ReturnTo
	if returnTo == "" {
		returnTo = "/apps"
	} else if !ValidReturnTo(returnTo) {
		return state.MutationReceipt{}, state.ErrInvalidResource
	}
	returnTo = SafeReturnTo(returnTo)
	oauthState, verifier := randomToken(), oauth2.GenerateVerifier()
	if s.GoogleWorkspace != nil && record.IntegrationID == GoogleIntegrationID {
		oauthState = s.GoogleWorkspace.signState(oauthState, s.now().Add(attemptLifetime))
	}
	attemptRecord := attempt{
		ID: stateKey(oauthState), ConnectionID: id, IntegrationID: record.IntegrationID, Scopes: scopes, Capabilities: capabilities,
		Verifier: verifier, ReturnTo: returnTo, ExpiresAt: s.now().Add(attemptLifetime), Status: "pending", AccountEpoch: record.AuthorizationEpoch,
	}
	config.Scopes = scopes
	options := []oauth2.AuthCodeOption{oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("include_granted_scopes", "true"), oauth2.SetAuthURLParam("prompt", "consent select_account")}
	if record.Account.Label != "" {
		options = append(options, oauth2.SetAuthURLParam("login_hint", record.Account.Label))
	}
	auth := AuthAttempt{ID: attemptRecord.ID, AuthorizeURL: config.AuthCodeURL(oauthState, options...), ExpiresAt: attemptRecord.ExpiresAt}
	authJSON, _ := json.Marshal(auth)
	prefs, _, err := s.Preferences(ctx)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	_ = current
	return s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "POST", Target: "/v1/connections/" + id + "/actions", ExpectedRevision: expected, Payload: action,
		Collection: CollectionConnections, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			var record Record
			if err := current.Decode(&record); err != nil {
				return state.MutationResult{}, err
			}
			if record.Status == StatusDisconnecting {
				return state.MutationResult{}, ErrInvalidAction
			}
			if record.Status != StatusConnected && record.Account.ProviderID == "" {
				record.Status = StatusConnecting
			}
			record.DesiredScopes = scopes
			attemptResource, err := state.NewResource(CollectionAttempts, attemptRecord.ID, 1, attemptRecord, map[string]string{"connection_id": id}, s.now())
			if err != nil {
				return state.MutationResult{}, err
			}
			result, err := s.write(record, current.Revision+1, "queued", prefs, attemptResource)
			result.Receipt.Auth = authJSON
			return result, err
		},
	})
}

func (s *Service) replay(_ context.Context, saved state.MutationReceipt, m state.Mutation) (state.MutationReceipt, error) {
	binding, err := state.MutationBinding(m)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	if binding != saved.Binding {
		return state.MutationReceipt{}, state.ErrIdempotencyConflict
	}
	return saved, nil
}

func (s *Service) Callback(ctx context.Context, oauthState, code, providerError string) (string, error) {
	return s.CallbackWithSelection(ctx, oauthState, code, providerError, "")
}

func (s *Service) CallbackWithSelection(ctx context.Context, oauthState, code, providerError, pickedFiles string) (string, error) {
	key := stateKey(oauthState)
	r, err := s.Store.Resource(ctx, CollectionAttempts, key)
	if errors.Is(err, state.ErrNotFound) {
		return "", ErrAttemptInvalid
	}
	if err != nil {
		return "", err
	}
	var a attempt
	if err = r.Decode(&a); err != nil {
		return "", err
	}
	if a.DrivePicker {
		return s.finishDrivePicker(ctx, a, r, code, providerError, pickedFiles)
	}
	redirect := func(result string) string {
		return BuildReturnRedirect(a.ReturnTo, url.Values{"connection": {a.ConnectionID}, "result": {result}})
	}
	if a.Status != "pending" {
		return redirect(a.Status), nil
	}
	finish := func(status, reason string) (string, error) {
		a.Status, a.Reason = status, reason
		next, err := state.NewResource(CollectionAttempts, a.ID, r.Revision+1, a, r.Index, s.now())
		if err != nil {
			if IsIOSOAuthCallback(a.ReturnTo) {
				return redirect(status), nil
			}
			return "", err
		}
		if _, err = s.Store.PutResource(ctx, next, r.Revision); err != nil && !errors.Is(err, state.ErrRevisionConflict) {
			if IsIOSOAuthCallback(a.ReturnTo) {
				return redirect(status), nil
			}
			return "", err
		}
		if status != "connected" {
			s.settleFailedAttempt(ctx, a.ConnectionID, reason)
		}
		return redirect(status), nil
	}
	if providerError != "" {
		if providerError == "access_denied" {
			return finish("cancelled", "The sign-in was cancelled.")
		}
		return finish("failed", "The provider did not grant access.")
	}
	if s.now().After(a.ExpiresAt) {
		return finish("failed", "The sign-in link expired. Start again from Apps.")
	}
	integration := s.Integrations[a.IntegrationID]
	config := s.oauthConfig(integration)
	if config == nil {
		return finish("failed", ErrNotConfigured.Error())
	}
	googleBeta := s.GoogleWorkspace != nil && a.IntegrationID == GoogleIntegrationID
	if googleBeta {
		claimed, acquired, err := s.claimGoogleAttempt(ctx, a, r)
		if err != nil {
			return "", err
		}
		if !acquired {
			return redirect("processing"), nil
		}
		r = claimed
	}
	config.Scopes = a.Scopes
	exchange, cancel := context.WithTimeout(s.providerContext(ctx), 20*time.Second)
	defer cancel()
	token, err := config.Exchange(exchange, code, oauth2.VerifierOption(a.Verifier))
	if err != nil {
		slog.WarnContext(ctx, "connection OAuth code exchange failed", "connection_id", a.ConnectionID, "reason", redactOAuth(err))
		return finish("failed", "The provider refused the sign-in code.")
	}
	account, err := integration.Identify(exchange, config.Client(exchange, token))
	if err != nil {
		return finish("failed", "The account could not be identified.")
	}
	granted := grantedScopes(token, a.Scopes)
	if googleBeta {
		var allowed bool
		granted, allowed = googleGrantedScopes(integration, granted)
		if !allowed {
			return finish("failed", "Google returned access outside the permissions this connection supports.")
		}
	}
	record, current, err := s.record(ctx, a.ConnectionID)
	if err != nil {
		if IsIOSOAuthCallback(a.ReturnTo) {
			return finish("failed", "The connection could not be loaded.")
		}
		return "", err
	}
	if record.Account.ProviderID != "" && record.Account.ProviderID != account.ProviderID {
		return finish("failed", ErrWrongAccount.Error())
	}
	if googleBeta && record.AuthorizationEpoch != a.AccountEpoch {
		return finish("failed", "The Google connection changed. Start a new sign-in from Apps.")
	}
	if !googleBeta && record.Account.ProviderID == "" {
		if existing, ok := s.sameAccount(ctx, a.IntegrationID, account.ProviderID, a.ConnectionID); ok {
			return s.mergeSignIn(ctx, a, r, existing, account, integration, token, granted, finish)
		}
	}
	var credentialWrite state.Resource
	if googleBeta {
		credentialWrite, err = s.prepareGoogleCredential(ctx, record, token)
	} else {
		err = s.storeToken(ctx, a.ConnectionID, token)
	}
	if err != nil {
		reason := "The account's credential could not be saved."
		if errors.Is(err, secret.ErrCredentialUnavailable) {
			reason = secret.ErrCredentialUnavailable.Error()
		}
		return finish("failed", reason)
	}
	prefs, _, err := s.Preferences(ctx)
	if err != nil {
		if IsIOSOAuthCallback(a.ReturnTo) {
			return finish("failed", "Account preferences could not be loaded.")
		}
		return "", err
	}
	_, err = s.Store.Mutate(ctx, state.Mutation{
		RequestID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("callback:"+a.ID)).String(), Method: "CALLBACK", Target: a.ID,
		Payload: a.ID, Collection: CollectionConnections, ID: a.ConnectionID, SkipRevisionCheck: true,
		Apply: func(cur *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if cur == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			var record Record
			if err := cur.Decode(&record); err != nil {
				return state.MutationResult{}, err
			}
			if record.Status == StatusDisconnecting || record.Status == StatusDisconnected && cur.Revision != current.Revision {
				return state.MutationResult{}, ErrInvalidAction
			}
			if googleBeta && record.AuthorizationEpoch != a.AccountEpoch {
				return state.MutationResult{}, ErrInvalidAction
			}
			now := s.now()
			record.Account, record.Status, record.GrantedScopes = account, StatusConnected, granted
			record.Capabilities = s.grantedCapabilities(integration, granted)
			record.AuthorizationEpoch++
			record.Health = Health{CheckedAt: &now, Code: HealthReady, Recovery: "none", LastSuccessAt: &now}
			if missing := missingScopes(record.DesiredScopes, granted); len(missing) > 0 {
				record.Status = StatusAttention
				record.Health.Code, record.Health.Recovery = HealthNeedsMoreAccess, "request_access"
				record.Health.Message = "Some requested access was not granted."
			}
			done := a
			done.Status = "connected"
			attemptResource, err := state.NewResource(CollectionAttempts, a.ID, r.Revision+1, done, r.Index, now)
			if err != nil {
				return state.MutationResult{}, err
			}
			writes := []state.Resource{attemptResource}
			if googleBeta {
				writes = append(writes, credentialWrite)
			}
			return s.write(record, cur.Revision+1, "applied", prefs, writes...)
		},
	})
	if err != nil {
		if googleBeta || IsIOSOAuthCallback(a.ReturnTo) {
			return finish("failed", "The connection could not be saved.")
		}
		return "", err
	}
	if s.OnConnected != nil {
		s.OnConnected(context.WithoutCancel(ctx), a.ConnectionID)
	}
	return redirect("connected"), nil
}

func (s *Service) sameAccount(ctx context.Context, integrationID, providerID, except string) (Record, bool) {
	if providerID == "" {
		return Record{}, false
	}
	records, _, err := s.Records(ctx)
	if err != nil {
		return Record{}, false
	}
	for _, record := range records {
		if record.ID != except && record.IntegrationID == integrationID && record.Account.ProviderID == providerID &&
			record.Status != StatusDisconnected && record.Status != StatusDisconnecting {
			return record, true
		}
	}
	return Record{}, false
}

func (s *Service) CollapseDuplicates(ctx context.Context) (map[string][]string, error) {
	if len(s.Integrations) == 0 {
		return nil, nil
	}
	records, resources, err := s.Records(ctx)
	if err != nil {
		return nil, err
	}
	groups := map[string][]int{}
	var order []string
	for i, record := range records {
		if record.Account.ProviderID == "" || record.Status == StatusDisconnected || record.Status == StatusDisconnecting {
			continue
		}
		key := record.IntegrationID + "\x00" + record.Account.ProviderID
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], i)
	}
	removed := map[string][]string{}
	for _, key := range order {
		members := groups[key]
		if len(members) < 2 {
			continue
		}
		keep := members[0]
		for _, i := range members[1:] {
			a, b := records[i], records[keep]
			if len(a.Capabilities) > len(b.Capabilities) || len(a.Capabilities) == len(b.Capabilities) && resources[i].UpdatedAt.After(resources[keep].UpdatedAt) {
				keep = i
			}
		}
		kept := records[keep]
		integration := s.Integrations[kept.IntegrationID]
		if integration == nil {
			continue
		}
		var drop []string
		var deletes []state.ResourceKey
		for _, i := range members {
			if i != keep {
				drop = append(drop, records[i].ID)
				deletes = append(deletes, state.ResourceKey{Collection: CollectionConnections, ID: records[i].ID})
			}
		}
		prefs, prefsResource, err := s.Preferences(ctx)
		if err != nil {
			return removed, err
		}
		_, err = s.Store.Mutate(ctx, state.Mutation{
			RequestID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("collapse:"+kept.ID+":"+strings.Join(drop, ","))).String(),
			Method:    "REPAIR", Target: kept.ID, Payload: drop, Collection: CollectionConnections, ID: kept.ID, SkipRevisionCheck: true,
			Apply: func(cur *state.Resource, read func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
				if cur == nil {
					return state.MutationResult{}, state.ErrNotFound
				}
				var record Record
				if err := cur.Decode(&record); err != nil {
					return state.MutationResult{}, err
				}
				for _, i := range members {
					for _, scope := range records[i].GrantedScopes {
						if !slices.Contains(record.GrantedScopes, scope) {
							record.GrantedScopes = append(record.GrantedScopes, scope)
						}
					}
					for _, scope := range records[i].DesiredScopes {
						if !slices.Contains(record.DesiredScopes, scope) {
							record.DesiredScopes = append(record.DesiredScopes, scope)
						}
					}
				}
				record.Capabilities = s.grantedCapabilities(integration, record.GrantedScopes)
				record.AuthorizationEpoch++
				var extra []state.Resource
				changed := false
				for purpose, id := range prefs.AccountDefaults {
					if slices.Contains(drop, id) {
						prefs.AccountDefaults[purpose], changed = record.ID, true
					}
				}
				if changed && prefsResource.Revision > 0 {
					next, err := state.NewResource(CollectionPreferences, PreferencesID, prefsResource.Revision+1, prefs, nil, s.now())
					if err != nil {
						return state.MutationResult{}, err
					}
					extra = append(extra, next)
				}
				result, err := s.write(record, cur.Revision+1, "applied", prefs, extra...)
				result.Deletes = append(result.Deletes, deletes...)
				return result, err
			},
		})
		if err != nil {
			return removed, err
		}
		for _, id := range drop {
			if err := s.Credentials.Delete(ctx, id); err != nil && !errors.Is(err, secret.ErrCredentialUnavailable) {
				slog.WarnContext(ctx, "duplicate connection credential delete failed", "connection_id", id, "err", err)
			}
			for _, cleanup := range s.Cleanup {
				_ = cleanup(ctx, id, true, false)
			}
		}
		removed[kept.ID] = drop
	}
	return removed, nil
}

func (s *Service) mergeSignIn(ctx context.Context, a attempt, attemptResource state.Resource, existing Record, account Account,
	integration Integration, token *oauth2.Token, granted []string, finish func(status, reason string) (string, error)) (string, error) {
	if token.RefreshToken == "" {
		if credential, err := s.Credentials.Get(ctx, existing.ID); err == nil {
			var old oauth2.Token
			if json.Unmarshal(credential.Data, &old) == nil {
				token.RefreshToken = old.RefreshToken
			}
		}
	}
	if err := s.storeToken(ctx, existing.ID, token); err != nil {
		reason := "The account's credential could not be saved."
		if errors.Is(err, secret.ErrCredentialUnavailable) {
			reason = secret.ErrCredentialUnavailable.Error()
		}
		return finish("failed", reason)
	}
	prefs, _, err := s.Preferences(ctx)
	if err != nil {
		if IsIOSOAuthCallback(a.ReturnTo) {
			return finish("failed", "Account preferences could not be loaded.")
		}
		return "", err
	}
	var desired []string
	if added, _, err := s.record(ctx, a.ConnectionID); err == nil {
		desired = added.DesiredScopes
	}
	_, err = s.Store.Mutate(ctx, state.Mutation{
		RequestID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("callback:"+a.ID)).String(), Method: "CALLBACK", Target: a.ID,
		Payload: a.ID, Collection: CollectionConnections, ID: existing.ID, SkipRevisionCheck: true,
		Apply: func(cur *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if cur == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			var record Record
			if err := cur.Decode(&record); err != nil {
				return state.MutationResult{}, err
			}
			if record.Status == StatusDisconnecting || record.Status == StatusDisconnected {
				return state.MutationResult{}, ErrInvalidAction
			}
			now := s.now()
			union := func(a, b []string) []string {
				out := append([]string{}, a...)
				for _, v := range b {
					if !slices.Contains(out, v) {
						out = append(out, v)
					}
				}
				return out
			}
			record.Account, record.Status = account, StatusConnected
			record.GrantedScopes = union(record.GrantedScopes, granted)
			record.DesiredScopes = union(record.DesiredScopes, desired)
			record.Capabilities = s.grantedCapabilities(integration, record.GrantedScopes)
			record.AuthorizationEpoch++
			record.Health = Health{CheckedAt: &now, Code: HealthReady, Recovery: "none", LastSuccessAt: &now,
				Message: "This account was already connected; its access was updated."}
			if missing := missingScopes(record.DesiredScopes, record.GrantedScopes); len(missing) > 0 {
				record.Status = StatusAttention
				record.Health.Code, record.Health.Recovery = HealthNeedsMoreAccess, "request_access"
				record.Health.Message = "This account was already connected; some requested access was not granted."
			}
			done := a
			done.Status = "connected"
			attemptNext, err := state.NewResource(CollectionAttempts, a.ID, attemptResource.Revision+1, done, attemptResource.Index, now)
			if err != nil {
				return state.MutationResult{}, err
			}
			result, err := s.write(record, cur.Revision+1, "applied", prefs, attemptNext)
			result.Deletes = append(result.Deletes, state.ResourceKey{Collection: CollectionConnections, ID: a.ConnectionID})
			return result, err
		},
	})
	if err != nil {
		if IsIOSOAuthCallback(a.ReturnTo) {
			return finish("failed", "The connection could not be saved.")
		}
		return "", err
	}
	if s.OnConnected != nil {
		s.OnConnected(context.WithoutCancel(ctx), a.ConnectionID)
		s.OnConnected(context.WithoutCancel(ctx), existing.ID)
	}
	return BuildReturnRedirect(a.ReturnTo, url.Values{"connection": {existing.ID}, "result": {"connected"}}), nil
}

func (s *Service) settleFailedAttempt(ctx context.Context, connectionID, reason string) {
	record, r, err := s.rawRecord(ctx, connectionID)
	if err != nil || record.Status != StatusConnecting {
		return
	}
	now := s.now()
	record.Status = StatusNotConnected
	if record.Account.ProviderID != "" {
		record.Status = StatusAttention
	}
	record.Health = Health{CheckedAt: &now, Code: HealthNeedsSignIn, Recovery: "connect", Message: reason}
	next, err := state.NewResource(CollectionConnections, record.ID, 0, record, recordIndex(record), now)
	if err == nil {
		_, _ = s.Store.PutResource(ctx, next, r.Revision)
	}
}

func missingScopes(desired, granted []string) []string {
	var missing []string
	for _, scope := range desired {
		if !slices.Contains(granted, scope) {
			missing = append(missing, scope)
		}
	}
	return missing
}

func (s *Service) grantedCapabilities(integration Integration, granted []string) []string {
	capabilities := []string{}
	for _, capability := range integration.Info().Capabilities {
		if !capability.Supported {
			continue
		}
		ok := true
		for _, scope := range capability.Scopes {
			ok = ok && slices.Contains(granted, scope)
		}
		if ok {
			capabilities = append(capabilities, capability.ID)
		}
	}
	return capabilities
}

func grantedScopes(token *oauth2.Token, requested []string) []string {
	raw, ok := token.Extra("scope").(string)
	if !ok || raw == "" {
		return requested
	}
	out := []string{}
	for _, scope := range strings.Fields(raw) {
		if !slices.Contains(out, scope) {
			out = append(out, scope)
		}
	}
	return out
}

func redactOAuth(err error) string {
	var retrieve *oauth2.RetrieveError
	if errors.As(err, &retrieve) {
		return "provider error " + retrieve.ErrorCode
	}
	return "transport error"
}

func (s *Service) storeToken(ctx context.Context, connectionID string, token *oauth2.Token) error {
	data, err := json.Marshal(token)
	if err != nil {
		return err
	}
	current, err := s.Credentials.Get(ctx, connectionID)
	version := current.Version
	if errors.Is(err, secret.ErrCredentialNotFound) {
		version = 0
	} else if err != nil {
		return err
	}
	_, err = s.Credentials.Replace(ctx, connectionID, version, data)
	return err
}
