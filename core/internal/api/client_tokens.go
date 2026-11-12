package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"nori/internal/clientauth"
	"nori/internal/state"
	"nori/internal/users"
)

const (
	collectionClients   = "mcp_clients"
	collectionClientUse = "client_token_usage"
)

type mcpClient struct {
	ID         string     `json:"id"`
	Revision   int64      `json:"revision"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind,omitempty"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type mcpClientEditable struct {
	Name           string     `json:"name"`
	Kind           string     `json:"kind"`
	Scopes         []string   `json:"scopes"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	CredentialHash string     `json:"credential_hash"`
}

func validClientName(name string) bool { return strings.TrimSpace(name) != "" && len(name) <= 100 }

func validScopes(scopes []string) bool {
	return len(scopes) == 1 && (scopes[0] == "read" || scopes[0] == "mutate") ||
		len(scopes) == 2 && slices.Contains(scopes, "read") && slices.Contains(scopes, "mutate")
}

func validClientExpiry(at *time.Time) bool {
	return at == nil || !at.IsZero() && at.Before(time.Now().Add(366*24*time.Hour))
}

func (v mcpClientEditable) valid() bool {
	return validClientName(v.Name) && slices.Contains([]string{"mcp", "api", "cli"}, v.Kind) &&
		validScopes(v.Scopes) && validClientExpiry(v.ExpiresAt) && clientauth.ValidHash(v.CredentialHash)
}

type mcpClientActionValue struct {
	Action         string     `json:"action"`
	CredentialHash string     `json:"credential_hash,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}

func (v mcpClientActionValue) valid() bool {
	switch v.Action {
	case "revoke":
		return v.CredentialHash == "" && v.ExpiresAt == nil
	case "rotate":
		return clientauth.ValidHash(v.CredentialHash) && validClientExpiry(v.ExpiresAt)
	default:
		return false
	}
}

type mcpClientCreated struct {
	state.MutationReceipt
	OwnerID string `json:"owner_id"`
}

type mcpClientDoc struct {
	mcpClient
	TokenHash string `json:"token_hash"`
	Version   int    `json:"token_version,omitempty"`
}

func decodeClient(res state.Resource) (mcpClientDoc, error) {
	var doc mcpClientDoc
	err := res.Decode(&doc)
	doc.Revision = res.Revision
	if doc.Kind == "" {
		doc.Kind = "mcp"
	}
	return doc, err
}

func (a *Assistant) mcpClients(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, next, err := a.Store.Resources(ctx, collectionClients, nil, r.URL.Query().Get("cursor"), 100)
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	items := make([]mcpClient, 0, len(page))
	for _, res := range page {
		doc, err := decodeClient(res)
		if err != nil {
			continue
		}
		if use, err := a.Store.Resource(ctx, collectionClientUse, doc.ID); err == nil {
			doc.LastUsedAt = &use.UpdatedAt
		}
		items = append(items, doc.mcpClient)
	}
	writeJSON(w, 200, list[mcpClient]{Items: items, NextCursor: next})
}

func (a *Assistant) createMCPClient(w http.ResponseWriter, r *http.Request) {
	var value mcpClientEditable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if expected != 0 || !value.valid() {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	ctx := r.Context()
	receipt, err := createClient(ctx, a.Store, requestID, value)
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	writeJSON(w, 201, mcpClientCreated{MutationReceipt: receipt, OwnerID: users.ID(ctx)})
}

func clientID(requestID string) string { return "mcpc_" + strings.ReplaceAll(requestID, "-", "") }

func createClient(ctx context.Context, store state.ResourceStore, requestID string, value mcpClientEditable) (state.MutationReceipt, error) {
	id := clientID(requestID)
	return store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "POST", Target: "/v1/mcp-clients", Payload: value, Collection: collectionClients, ID: id,
		Apply: func(_ *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			doc := mcpClientDoc{mcpClient: mcpClient{
				ID: id, Revision: 1, Name: value.Name, Kind: value.Kind, Scopes: value.Scopes,
				ExpiresAt: value.ExpiresAt, CreatedAt: time.Now().UTC(),
			}, TokenHash: value.CredentialHash, Version: 2}
			return clientMutation(doc, 201)
		},
	})
}

func clientMutation(doc mcpClientDoc, status int) (state.MutationResult, error) {
	res, err := state.NewResource(collectionClients, doc.ID, doc.Revision, doc, nil, time.Now())
	if err != nil {
		return state.MutationResult{}, err
	}
	data, err := json.Marshal(doc.mcpClient)
	return state.MutationResult{
		Writes:  []state.Resource{res},
		Receipt: state.MutationReceipt{Status: "applied", Resource: data, HTTPStatus: status},
	}, err
}

func (a *Assistant) mcpClientAction(w http.ResponseWriter, r *http.Request) {
	var value mcpClientActionValue
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if !value.valid() {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	ctx, id := r.Context(), r.PathValue("id")
	receipt, err := changeClient(ctx, a.Store, id, requestID, expected, value)
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	writeJSON(w, 200, mcpClientCreated{MutationReceipt: receipt, OwnerID: users.ID(ctx)})
}

func changeClient(ctx context.Context, store state.ResourceStore, id, requestID string, expected int64, value mcpClientActionValue) (state.MutationReceipt, error) {
	return store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "POST", Target: "/v1/mcp-clients/" + id + "/actions", ExpectedRevision: expected, Payload: value,
		Collection: collectionClients, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			doc, err := decodeClient(*current)
			if err != nil {
				return state.MutationResult{}, err
			}
			doc.Revision++
			if value.Action == "revoke" {
				now := time.Now().UTC()
				doc.RevokedAt = &now
			} else {
				if doc.RevokedAt != nil || doc.TokenHash == value.CredentialHash {
					return state.MutationResult{}, state.ErrInvalidResource
				}
				doc.TokenHash, doc.Version = value.CredentialHash, 2
				if value.ExpiresAt != nil {
					doc.ExpiresAt = value.ExpiresAt
				}
			}
			return clientMutation(doc, 200)
		},
	})
}

var errClientCredential = errors.New("invalid client credential")

func (a *Assistant) authenticate(ctx context.Context, credential clientauth.Credential) (mcpClient, error) {
	client, err := lookupClient(ctx, a.Store, credential)
	if err == nil {
		recordClientUse(ctx, a.Store, client.ID)
	}
	return client, err
}

func lookupClient(ctx context.Context, store state.ResourceStore, credential clientauth.Credential) (mcpClient, error) {
	if credential.ID != "" {
		res, err := store.Resource(ctx, collectionClients, credential.ID)
		if err != nil {
			return mcpClient{}, err
		}
		doc, err := decodeClient(res)
		if err == nil && doc.matches(credential, 2) {
			return doc.mcpClient, nil
		}
		return mcpClient{}, errClientCredential
	}
	for cursor := ""; ; {
		page, next, err := store.Resources(ctx, collectionClients, nil, cursor, 100)
		if err != nil {
			return mcpClient{}, err
		}
		for _, res := range page {
			doc, err := decodeClient(res)
			if err == nil && doc.matches(credential, 0) {
				return doc.mcpClient, nil
			}
		}
		if next == "" {
			return mcpClient{}, errClientCredential
		}
		cursor = next
	}
}

func (d mcpClientDoc) matches(credential clientauth.Credential, version int) bool {
	return d.Version == version && d.RevokedAt == nil && (d.ExpiresAt == nil || time.Now().Before(*d.ExpiresAt)) &&
		subtle.ConstantTimeCompare([]byte(d.TokenHash), []byte(credential.Hash)) == 1
}

func recordClientUse(ctx context.Context, store state.ResourceStore, id string) {
	old, err := store.Resource(ctx, collectionClientUse, id)
	if err == nil && time.Since(old.UpdatedAt) < time.Minute || err != nil && !errors.Is(err, state.ErrNotFound) {
		return
	}
	use, err := state.NewResource(collectionClientUse, id, old.Revision+1, struct{}{}, nil, time.Now())
	if err == nil {
		_, _ = store.PutResource(ctx, use, old.Revision)
	}
}

func clientToken(r *http.Request) string {
	if value := r.Header.Get(clientauth.Header); value != "" {
		return value
	}
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return token
}

func clientUnauthorized(w http.ResponseWriter) {
	writeJSON(w, 401, apiError{Error: "A valid Nori client token is required.", Code: "invalid_client_token"})
}
