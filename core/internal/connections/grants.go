package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"nori/internal/extensions"
	"nori/internal/secret"
	"nori/internal/state"
)

func (s *Service) disconnect(ctx context.Context, id, requestID string, expected int64, action Action) (state.MutationReceipt, error) {
	prefs, _, err := s.Preferences(ctx)
	if err != nil {
		return state.MutationReceipt{}, err
	}
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
			if record.Status == StatusDisconnected {
				return state.MutationResult{}, ErrInvalidAction
			}
			record.Status = StatusDisconnecting
			record.AuthorizationEpoch++
			return s.write(record, current.Revision+1, "queued", prefs)
		},
	})
	if err != nil {
		return receipt, err
	}
	s.finishDisconnect(context.WithoutCancel(ctx), id, action)
	return receipt, nil
}

func (s *Service) finishDisconnect(ctx context.Context, id string, action Action) {
	record, r, err := s.record(ctx, id)
	if err != nil || record.Status != StatusDisconnecting {
		return
	}
	var problems []string
	_, shared := s.sameAccount(ctx, record.IntegrationID, record.Account.ProviderID, id)
	if integration := s.Integrations[record.IntegrationID]; integration != nil && !shared {
		if credential, err := s.Credentials.Get(ctx, id); err == nil {
			var token oauth2.Token
			if json.Unmarshal(credential.Data, &token) == nil {
				revoke, cancel := context.WithTimeout(s.providerContext(ctx), 15*time.Second)
				if err := integration.Revoke(revoke, &token); err != nil {
					problems = append(problems, "The provider did not confirm revocation; remove Nori's access in the account's security settings.")
				}
				cancel()
			}
		}
	}
	if err := s.Credentials.Delete(ctx, id); err != nil && !errors.Is(err, secret.ErrCredentialUnavailable) {
		problems = append(problems, "The stored credential could not be deleted.")
	}
	for _, cleanup := range s.Cleanup {
		if err := cleanup(ctx, id, action.PurgeCache, action.DeleteImportedContent); err != nil {
			problems = append(problems, "Some cached connector data could not be removed.")
		}
	}
	now := s.now()
	record.Status, record.GrantedScopes, record.Capabilities = StatusDisconnected, []string{}, []string{}
	if s.GoogleWorkspace != nil && record.ID == googleConnectionID {
		record.Account = Account{}
		record.DesiredScopes, record.SelectedResourceIDs = nil, []string{}
	}
	record.Health = Health{CheckedAt: &now, Code: HealthNeedsSignIn, Recovery: "connect", Message: strings.Join(problems, " ")}
	next, err := state.NewResource(CollectionConnections, id, 0, record, recordIndex(record), now)
	if err == nil {
		_, _ = s.Store.PutResource(ctx, next, r.Revision)
	}
}

func (s *Service) Resources(ctx context.Context, id string) ([]Resource, error) {
	grant, err := s.Grant(ctx, id)
	if err != nil {
		return nil, err
	}
	integration := s.Integrations[grant.Connection.IntegrationID]
	var list []Resource
	if s.GoogleWorkspace != nil && grant.Connection.ID == googleConnectionID {
		list, err = s.googleResources(ctx, grant, integration)
	} else {
		list, err = integration.Resources(ctx, grant.Client)
	}
	if err != nil {
		s.ReportFailure(ctx, id, err)
		return nil, err
	}
	for i := range list {
		list[i].Ref.ConnectionID = id
		list[i].Selected = list[i].Selected || slices.Contains(grant.Connection.SelectedResourceIDs, list[i].Ref.ProviderID)
	}
	return list, nil
}

type Grant struct {
	Connection Record
	Revision   int64
	Client     *http.Client
	Token      *oauth2.Token
}

var refreshLocks sync.Map

func (s *Service) Grant(ctx context.Context, id string) (Grant, error) {
	record, r, err := s.record(ctx, id)
	if err != nil {
		return Grant{}, err
	}
	if record.Status != StatusConnected && record.Status != StatusAttention {
		return Grant{}, fmt.Errorf("%w: the account is %s", ErrNotConnected, record.Status)
	}
	integration := s.Integrations[record.IntegrationID]
	if integration == nil {
		return Grant{}, ErrUnknownIntegration
	}
	if password, ok := integration.(PasswordIntegration); ok && integration.Info().Auth == "app_password" {
		credential, err := s.Credentials.Get(ctx, id)
		if err != nil {
			return Grant{}, err
		}
		var pc passwordCredential
		if err := json.Unmarshal(credential.Data, &pc); err != nil {
			return Grant{}, errors.New("stored credential is malformed")
		}
		return Grant{Connection: record, Revision: r.Revision, Client: password.Client(s.baseClient(), pc.Username, pc.Password)}, nil
	}
	config := s.oauthConfig(integration)
	if config == nil {
		return Grant{}, ErrNotConfigured
	}
	credential, err := s.Credentials.Get(ctx, id)
	if err != nil {
		return Grant{}, err
	}
	var token oauth2.Token
	if err = json.Unmarshal(credential.Data, &token); err != nil {
		return Grant{}, errors.New("stored credential is malformed")
	}
	if !token.Valid() {
		lockID := id
		if s.GoogleWorkspace != nil && id == googleConnectionID {
			lockID = s.GoogleWorkspace.UserID + ":" + id
		}
		lock, _ := refreshLocks.LoadOrStore(lockID, &sync.Mutex{})
		mu := lock.(*sync.Mutex)
		mu.Lock()
		defer mu.Unlock()
		if latest, err := s.Credentials.Get(ctx, id); err == nil && latest.Version != credential.Version {
			credential = latest
			_ = json.Unmarshal(credential.Data, &token)
		}
		if !token.Valid() {
			refresh, cancel := context.WithTimeout(s.providerContext(ctx), 20*time.Second)
			fresh, err := config.TokenSource(refresh, &token).Token()
			cancel()
			if err != nil {
				s.ReportFailure(ctx, id, err)
				return Grant{}, fmt.Errorf("%w: %s", ErrNotConnected, redactOAuth(err))
			}
			if s.GoogleWorkspace != nil && id == googleConnectionID {
				if _, allowed := googleGrantedScopes(integration, grantedScopes(fresh, record.GrantedScopes)); !allowed {
					return Grant{}, fmt.Errorf("%w: Google returned access outside the permissions this connection supports", ErrNotConnected)
				}
			}
			if fresh.RefreshToken == "" {
				fresh.RefreshToken = token.RefreshToken
			}
			data, _ := json.Marshal(fresh)
			if _, err = s.Credentials.Replace(ctx, id, credential.Version, data); errors.Is(err, secret.ErrCredentialConflict) {
				latest, readErr := s.Credentials.Get(ctx, id)
				if readErr != nil {
					return Grant{}, readErr
				}
				_ = json.Unmarshal(latest.Data, fresh)
			} else if err != nil {
				return Grant{}, err
			}
			token = *fresh
		}
	}
	if s.GoogleWorkspace != nil && id == googleConnectionID {
		latest, _, err := s.record(ctx, id)
		if err != nil {
			return Grant{}, err
		}
		if usable, _ := usableRecord(latest); !usable || latest.AuthorizationEpoch != record.AuthorizationEpoch {
			return Grant{}, fmt.Errorf("%w: the Google account changed while getting access", ErrNotConnected)
		}
	}
	client := config.Client(s.providerContext(context.WithoutCancel(ctx)), &token)
	client.Timeout = 30 * time.Second
	return Grant{Connection: record, Revision: r.Revision, Client: client, Token: &token}, nil
}

func (s *Service) ReportFailure(ctx context.Context, id string, failure error) {
	code, recovery, message := HealthTemporary, "wait", "The provider is temporarily failing."
	var retrieve *oauth2.RetrieveError
	switch {
	case errors.As(failure, &retrieve) && (retrieve.ErrorCode == "invalid_grant" || retrieve.ErrorCode == "unauthorized_client"):
		code, recovery, message = HealthRevoked, "connect", "Access was revoked or expired. Sign in again."
	case errors.Is(failure, ErrInsufficientScope):
		code, recovery, message = HealthNeedsMoreAccess, "request_access", "This needs more access than the account granted."
	case errors.Is(failure, ErrRateLimited):
		code, recovery, message = HealthRateLimited, "wait", "The provider is limiting requests."
	case errors.Is(failure, ErrUnauthorized):
		code, recovery, message = HealthNeedsSignIn, "connect", "The provider rejected the credential. Sign in again."
	case errors.Is(failure, secret.ErrCredentialUnavailable):
		code, recovery, message = HealthStoreUnavailable, "none", secret.ErrCredentialUnavailable.Error()
	}
	record, r, err := s.record(ctx, id)
	if err != nil {
		return
	}
	now := s.now()
	record.Health.CheckedAt, record.Health.Code, record.Health.Recovery, record.Health.Message = &now, code, recovery, message
	if code == HealthRevoked || code == HealthNeedsSignIn {
		record.Status = StatusAttention
	}
	next, err := state.NewResource(CollectionConnections, id, 0, record, recordIndex(record), now)
	if err == nil {
		_, _ = s.Store.PutResource(ctx, next, r.Revision)
	}
}

func (s *Service) ReportSuccess(ctx context.Context, id string) {
	record, r, err := s.record(ctx, id)
	if err != nil {
		return
	}
	if record.Health.Code == HealthReady && record.Status == StatusConnected && record.Health.LastSuccessAt != nil && s.now().Sub(*record.Health.LastSuccessAt) < 10*time.Minute {
		return
	}
	now := s.now()
	if missing := missingScopes(record.DesiredScopes, record.GrantedScopes); record.Status == StatusAttention && len(missing) > 0 {
		if record.Health.Code == HealthNeedsMoreAccess && record.Health.LastSuccessAt != nil && now.Sub(*record.Health.LastSuccessAt) < 10*time.Minute {
			return
		}
		record.Health = Health{CheckedAt: &now, Code: HealthNeedsMoreAccess, Recovery: "request_access", Message: "Some requested access was not granted.", LastSuccessAt: &now}
	} else {
		record.Health = Health{CheckedAt: &now, Code: HealthReady, Recovery: "none", LastSuccessAt: &now}
		if record.Status == StatusAttention {
			record.Status = StatusConnected
		}
	}
	next, err := state.NewResource(CollectionConnections, id, 0, record, recordIndex(record), now)
	if err == nil {
		_, _ = s.Store.PutResource(ctx, next, r.Revision)
	}
}

var (
	ErrInsufficientScope = errors.New("insufficient scope")
	ErrRateLimited       = errors.New("rate limited")
	ErrUnauthorized      = errors.New("unauthorized")
)

func (s *Service) Select(ctx context.Context, integrationID, purpose, requested string) (Record, int64, error) {
	records, resources, err := s.Records(ctx)
	if err != nil {
		return Record{}, 0, err
	}
	var eligible []int
	connected := false
	for i, record := range records {
		usable, _ := usableRecord(record)
		if record.IntegrationID != integrationID || !usable {
			continue
		}
		connected = true
		if purpose == "" || slices.Contains(record.Capabilities, purpose) {
			eligible = append(eligible, i)
		}
	}
	if requested != "" {
		for _, i := range eligible {
			if records[i].ID == requested || strings.EqualFold(records[i].Account.Label, requested) || strings.EqualFold(records[i].Title, requested) {
				return records[i], resources[i].Revision, nil
			}
		}
		return Record{}, 0, ErrUnknownAccount
	}
	if len(eligible) == 0 {
		if connected {
			return Record{}, 0, fmt.Errorf("%w: %w", ErrNotConnected, ErrInsufficientScope)
		}
		return Record{}, 0, ErrNotConnected
	}
	prefs, _, err := s.Preferences(ctx)
	if err != nil {
		return Record{}, 0, err
	}
	if id := prefs.AccountDefaults[purpose]; id != "" {
		for _, i := range eligible {
			if records[i].ID == id {
				return records[i], resources[i].Revision, nil
			}
		}
	}
	purposes := make([]string, 0, len(prefs.AccountDefaults))
	for p := range prefs.AccountDefaults {
		purposes = append(purposes, p)
	}
	sort.Strings(purposes)
	for _, p := range purposes {
		id := prefs.AccountDefaults[p]
		for _, i := range eligible {
			if records[i].ID == id {
				return records[i], resources[i].Revision, nil
			}
		}
	}
	if len(eligible) == 1 {
		return records[eligible[0]], resources[eligible[0]].Revision, nil
	}
	same := records[eligible[0]].Account.ProviderID != ""
	for _, i := range eligible[1:] {
		same = same && records[i].Account.ProviderID == records[eligible[0]].Account.ProviderID
	}
	if same {
		best := eligible[0]
		for _, i := range eligible[1:] {
			if len(records[i].Capabilities) > len(records[best].Capabilities) ||
				len(records[i].Capabilities) == len(records[best].Capabilities) && resources[i].UpdatedAt.After(resources[best].UpdatedAt) {
				best = i
			}
		}
		return records[best], resources[best].Revision, nil
	}
	choice := &ChooseAccount{Purpose: purpose}
	for _, i := range eligible {
		choice.Eligible = append(choice.Eligible, records[i])
	}
	return Record{}, 0, choice
}

func usableRecord(record Record) (usable, partial bool) {
	if record.Status == StatusConnected {
		return true, false
	}
	if record.Status == StatusAttention && (record.Health.Code == HealthNeedsMoreAccess || record.Health.Code == HealthReady) {
		return true, record.Health.Code == HealthNeedsMoreAccess || len(missingScopes(record.DesiredScopes, record.GrantedScopes)) > 0
	}
	return false, false
}

func (s *Service) Inventory(ctx context.Context) (extensions.Inventory, error) {
	records, _, err := s.Records(ctx)
	if err != nil {
		return extensions.Inventory{}, err
	}
	var inventory extensions.Inventory
	for _, id := range s.order {
		integration := s.Integrations[id]
		granted := map[string]bool{}
		var labels, attention []string
		for _, record := range records {
			if record.IntegrationID != id {
				continue
			}
			usable, partial := usableRecord(record)
			switch {
			case usable:
				labels = append(labels, record.Account.Label)
				if partial {
					attention = append(attention, record.Account.Label+" (some access not granted)")
				}
				for _, capability := range record.Capabilities {
					granted[capability] = true
				}
			case record.Status == StatusAttention || record.Status == StatusConnecting || record.Status == StatusDisabled:
				attention = append(attention, fmt.Sprintf("%s (%s)", record.Account.Label, record.Status))
			}
		}
		groups := map[string]string{}
		usable := map[string]bool{}
		askable := integration.Info().Auth == "oauth" && s.oauthConfig(integration) != nil
		var groupOrder []string
		for _, d := range integration.Definitions() {
			if _, seen := groups[d.Capability]; !seen {
				groups[d.Capability] = d.CapabilityTitle
				groupOrder = append(groupOrder, d.Capability)
			}
			if d.RequiredCapability == "" || granted[d.RequiredCapability] {
				inventory.Definitions = append(inventory.Definitions, d)
				usable[d.Capability] = true
			} else if askable {
				if len(labels) == 0 {
					d.Exposure = extensions.ExposureDiscoverable
				}
				inventory.Definitions = append(inventory.Definitions, d)
			}
		}
		for _, capability := range groupOrder {
			stateText := "Not connected. The user can connect it in Apps."
			if askable {
				stateText = "Not connected. Load it and call the tool the request needs: that asks the user to connect the account from this task, and the task resumes once they have"
			}
			ready := len(labels) > 0 && usable[capability]
			if ready {
				stateText = "Connected"
			} else if len(labels) > 0 {
				stateText = "Connected, but this access was not granted. The user can grant it in Apps."
				if askable {
					stateText = "Connected, but this access was not granted. Load it and call the tool the request needs: that asks the user for the access from this task"
				}
			} else if len(attention) > 0 {
				stateText = "Needs attention in Apps: " + strings.Join(attention, ", ")
				if askable {
					stateText += ". Load it and call the tool the request needs: that asks the user to sign in from this task"
				}
			}
			inventory.Capabilities = append(inventory.Capabilities, extensions.CapabilityState{
				Capability: capability, Title: groups[capability], Account: strings.Join(labels, ", "), Availability: stateText, Ready: ready,
			})
		}
	}
	return inventory, nil
}

func (s *Service) NeedsSignIn(ctx context.Context, integrationID, requested string) (Record, bool) {
	records, _, err := s.Records(ctx)
	if err != nil {
		return Record{}, false
	}
	var found *Record
	for i, r := range records {
		if r.IntegrationID != integrationID || r.Status != StatusAttention || (r.Health.Code != HealthNeedsSignIn && r.Health.Code != HealthRevoked) {
			continue
		}
		if requested != "" && (r.ID == requested || strings.EqualFold(r.Account.Label, requested)) {
			return records[i], true
		}
		if found == nil {
			found = &records[i]
		}
	}
	if found == nil || requested != "" {
		return Record{}, false
	}
	return *found, true
}

func (s *Service) StartRepair(ctx context.Context, id, requestID, returnTo string, capabilities []string) (AuthAttempt, error) {
	record, r, err := s.record(ctx, id)
	if err != nil {
		return AuthAttempt{}, err
	}
	integration := s.Integrations[record.IntegrationID]
	if integration == nil || integration.Info().Auth != "oauth" {
		return AuthAttempt{}, fmt.Errorf("%w: this account signs in with an app password in Apps", ErrInvalidAction)
	}
	action := Action{Action: "repair", ReturnTo: returnTo}
	if len(capabilities) > 0 {
		action.Capabilities = append(append([]string{}, record.Capabilities...), capabilities...)
	}
	receipt, err := s.connect(ctx, id, requestID, r.Revision, action)
	if err != nil {
		return AuthAttempt{}, err
	}
	var attempt AuthAttempt
	if err := json.Unmarshal(receipt.Auth, &attempt); err != nil || attempt.AuthorizeURL == "" {
		return AuthAttempt{}, errors.New("the sign-in attempt could not be started")
	}
	return attempt, nil
}

func (s *Service) LackingCapability(ctx context.Context, integrationID, capability, requested string) (Record, bool) {
	records, _, err := s.Records(ctx)
	if err != nil {
		return Record{}, false
	}
	for _, r := range records {
		usable, _ := usableRecord(r)
		if r.IntegrationID != integrationID || !usable || slices.Contains(r.Capabilities, capability) {
			continue
		}
		if requested == "" || r.ID == requested || strings.EqualFold(r.Account.Label, requested) {
			return r, true
		}
	}
	return Record{}, false
}

func (s *Service) ScopesFor(integrationID, capability string) []string {
	integration := s.Integrations[integrationID]
	if integration == nil {
		return nil
	}
	for _, c := range integration.Info().Capabilities {
		if c.ID == capability {
			return c.Scopes
		}
	}
	return nil
}
