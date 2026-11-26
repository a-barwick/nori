package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"nori/internal/secret"
	"nori/internal/state"
)

const configuredCredentialPrefix = "\x00nori-mcp-credential-v1\x00"

type configuredCredential struct {
	Epoch string `json:"epoch"`
	Token string `json:"token"`
}

func encodeConfiguredCredential(epoch, token string) ([]byte, error) {
	data, err := json.Marshal(configuredCredential{Epoch: epoch, Token: token})
	if err != nil {
		return nil, err
	}
	return append([]byte(configuredCredentialPrefix), data...), nil
}

func configuredToken(server stored, data []byte) (string, error) {
	value := string(data)
	if strings.HasPrefix(value, configuredCredentialPrefix) {
		var credential configuredCredential
		if json.Unmarshal(data[len(configuredCredentialPrefix):], &credential) != nil || credential.Epoch != server.CredentialEpoch || credential.Token == "" {
			return "", errors.New("the server's credential does not match its current configuration")
		}
		return credential.Token, nil
	}
	if server.CredentialEpoch != "" || value == "" {
		return "", errors.New("the server's credential does not match its current configuration")
	}
	return value, nil
}

func credentialMatches(server stored, data []byte) bool {
	if server.Auth == "configured" {
		_, err := configuredToken(server, data)
		return err == nil
	}
	var credential oauthCredential
	return json.Unmarshal(data, &credential) == nil && credential.TokenURL != "" && credential.CredentialEpoch == server.CredentialEpoch
}

func (s *Service) deleteCredentialVersion(ctx context.Context, id string, credential secret.Credential) error {
	store, ok := s.Credentials.(secret.VersionedCredentialDeleter)
	if !ok {
		return secret.ErrCredentialUnavailable
	}
	err := store.DeleteVersion(ctx, "mcp-"+id, credential)
	if errors.Is(err, secret.ErrCredentialConflict) || errors.Is(err, secret.ErrCredentialNotFound) || errors.Is(err, secret.ErrCredentialUnavailable) {
		return nil
	}
	return err
}

func (s *Service) advanceCredentialEpoch(ctx context.Context, server stored) (stored, error) {
	nextEpoch := uuid.NewString()
	var updated stored
	_, err := s.Store.Mutate(ctx, state.Mutation{
		RequestID: "mcp-credential-epoch-" + uuid.NewString(), Method: "POST", Target: "/v1/mcp/" + server.ID,
		Payload: nextEpoch, Collection: CollectionServers, ID: server.ID, SkipRevisionCheck: true,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			latest, err := decode(*current)
			if err != nil {
				return state.MutationResult{}, err
			}
			if latest.CredentialEpoch != server.CredentialEpoch || latest.Endpoint != server.Endpoint ||
				latest.Transport != server.Transport || latest.Auth != server.Auth {
				return state.MutationResult{}, state.ErrRevisionConflict
			}
			latest.CredentialEpoch = nextEpoch
			updated = latest
			return s.write(latest, current.Revision+1, 200)
		},
	})
	return updated, err
}
