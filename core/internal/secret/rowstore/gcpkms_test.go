package rowstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func jsonResponse(status int, body any) *http.Response {
	raw, _ := json.Marshal(body)
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(raw)),
	}
}

func TestGCPKMSKeyWrapUnwrapRoundTrip(t *testing.T) {
	ctx := context.Background()
	keyName := "projects/nori-prod/locations/us-central1/keyRings/nori/cryptoKeys/master"
	simKey := mustParseHexKey(t, 0x99)
	expectedAAD := base64.StdEncoding.EncodeToString(dataKeyAAD)

	var calls atomic.Int32
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", req.Method)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer test-token-123" {
			t.Errorf("Authorization = %q, want Bearer test-token-123", got)
		}
		if got := req.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		rawBody, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}

		switch req.URL.String() {
		case "https://cloudkms.googleapis.com/v1/" + keyName + ":encrypt":
			var er kmsEncryptRequest
			if err := json.Unmarshal(rawBody, &er); err != nil {
				return nil, err
			}
			if er.AdditionalAuthenticatedData != expectedAAD {
				t.Errorf("encrypt AAD = %q, want %q", er.AdditionalAuthenticatedData, expectedAAD)
			}
			pt, err := base64.StdEncoding.DecodeString(er.Plaintext)
			if err != nil {
				return nil, err
			}
			ct, _, err := simKey.Wrap(ctx, pt)
			if err != nil {
				return nil, err
			}
			return jsonResponse(http.StatusOK, kmsEncryptResponse{
				Ciphertext: base64.StdEncoding.EncodeToString(ct),
				Name:       keyName + "/cryptoKeyVersions/1",
			}), nil

		case "https://cloudkms.googleapis.com/v1/" + keyName + ":decrypt":
			var dr kmsDecryptRequest
			if err := json.Unmarshal(rawBody, &dr); err != nil {
				return nil, err
			}
			if dr.AdditionalAuthenticatedData != expectedAAD {
				t.Errorf("decrypt AAD = %q, want %q", dr.AdditionalAuthenticatedData, expectedAAD)
			}
			ct, err := base64.StdEncoding.DecodeString(dr.Ciphertext)
			if err != nil {
				return nil, err
			}
			pt, err := simKey.Unwrap(ctx, ct, simKey.id)
			if err != nil {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": "decrypt failed"}), nil
			}
			return jsonResponse(http.StatusOK, kmsDecryptResponse{
				Plaintext: base64.StdEncoding.EncodeToString(pt),
			}), nil

		default:
			return jsonResponse(http.StatusNotFound, map[string]string{"error": "unexpected url " + req.URL.String()}), nil
		}
	})

	kms, err := NewGCPKMSKey(GCPKMSConfig{
		KeyName:    keyName,
		HTTPClient: &http.Client{Transport: rt},
		TokenSource: func(context.Context) (string, error) {
			return "test-token-123", nil
		},
	})
	if err != nil {
		t.Fatalf("NewGCPKMSKey: %v", err)
	}

	dek := bytes.Repeat([]byte{0x42}, 32)
	wrapped, keyID, err := kms.Wrap(ctx, dek)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if keyID != "gcpkms:"+keyName {
		t.Fatalf("keyID = %q, want %q", keyID, "gcpkms:"+keyName)
	}
	if bytes.Equal(wrapped, dek) {
		t.Fatalf("wrapped key equals plaintext DEK")
	}

	got, err := kms.Unwrap(ctx, wrapped, keyID)
	if err != nil {
		t.Fatalf("Unwrap: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatalf("Unwrap returned %x, want %x", got, dek)
	}
	if calls.Load() != 2 {
		t.Fatalf("HTTP calls = %d, want 2", calls.Load())
	}

	// Mismatched keyID must fail fast without calling KMS.
	if _, err := kms.Unwrap(ctx, wrapped, "key:abcdef012345"); err == nil {
		t.Fatalf("Unwrap accepted mismatched keyID")
	}
	if calls.Load() != 2 {
		t.Fatalf("Unwrap with mismatched keyID made an HTTP call (total=%d)", calls.Load())
	}

	// Heterogeneous KeyRing: GCPKMSKey (primary) + MasterKey (fallback) for zero-downtime migration.
	legacyKey := mustParseHexKey(t, 0x12)
	legacyWrapped, legacyID, err := legacyKey.Wrap(ctx, dek)
	if err != nil {
		t.Fatalf("legacyKey.Wrap: %v", err)
	}
	ring, err := NewKeyRing(kms, legacyKey)
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}
	if unwrapped, err := ring.Unwrap(ctx, legacyWrapped, legacyID); err != nil || !bytes.Equal(unwrapped, dek) {
		t.Fatalf("ring.Unwrap(legacy) = (%x, %v)", unwrapped, err)
	}
	if _, newID, err := ring.Wrap(ctx, dek); err != nil || newID != kms.KeyID() {
		t.Fatalf("ring.Wrap = (%q, %v), want %q", newID, err, kms.KeyID())
	}
}

func TestGCPKMSKeyWrapWithAAD(t *testing.T) {
	ctx := context.Background()
	keyName := "projects/nori-prod/locations/us-central1/keyRings/nori/cryptoKeys/master"
	simKey := mustParseHexKey(t, 0x88)

	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		rawBody, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		switch req.URL.String() {
		case "https://cloudkms.googleapis.com/v1/" + keyName + ":encrypt":
			var er kmsEncryptRequest
			if err := json.Unmarshal(rawBody, &er); err != nil {
				return nil, err
			}
			pt, err := base64.StdEncoding.DecodeString(er.Plaintext)
			if err != nil {
				return nil, err
			}
			aad, err := base64.StdEncoding.DecodeString(er.AdditionalAuthenticatedData)
			if err != nil {
				return nil, err
			}
			ct, _, err := simKey.WrapWithAAD(ctx, pt, aad)
			if err != nil {
				return nil, err
			}
			return jsonResponse(http.StatusOK, kmsEncryptResponse{
				Ciphertext: base64.StdEncoding.EncodeToString(ct),
			}), nil
		case "https://cloudkms.googleapis.com/v1/" + keyName + ":decrypt":
			var dr kmsDecryptRequest
			if err := json.Unmarshal(rawBody, &dr); err != nil {
				return nil, err
			}
			ct, err := base64.StdEncoding.DecodeString(dr.Ciphertext)
			if err != nil {
				return nil, err
			}
			aad, err := base64.StdEncoding.DecodeString(dr.AdditionalAuthenticatedData)
			if err != nil {
				return nil, err
			}
			pt, err := simKey.UnwrapWithAAD(ctx, ct, simKey.id, aad)
			if err != nil {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": "aad mismatch"}), nil
			}
			return jsonResponse(http.StatusOK, kmsDecryptResponse{
				Plaintext: base64.StdEncoding.EncodeToString(pt),
			}), nil
		default:
			return jsonResponse(http.StatusNotFound, map[string]string{"error": "unexpected url"}), nil
		}
	})

	kms, err := NewGCPKMSKey(GCPKMSConfig{
		KeyName:     keyName,
		HTTPClient:  &http.Client{Transport: rt},
		TokenSource: func(context.Context) (string, error) { return "tok", nil },
	})
	if err != nil {
		t.Fatalf("NewGCPKMSKey: %v", err)
	}

	dek := bytes.Repeat([]byte{0x5a}, 32)
	aadAlice := []byte("nori-tenant-dek:v1:5:alice")
	aadBob := []byte("nori-tenant-dek:v1:3:bob")

	wrapped, keyID, err := kms.WrapWithAAD(ctx, dek, aadAlice)
	if err != nil {
		t.Fatalf("WrapWithAAD: %v", err)
	}
	got, err := kms.UnwrapWithAAD(ctx, wrapped, keyID, aadAlice)
	if err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("UnwrapWithAAD(matching) = (%x, %v)", got, err)
	}
	if _, err := kms.UnwrapWithAAD(ctx, wrapped, keyID, aadBob); err == nil {
		t.Fatalf("UnwrapWithAAD succeeded with wrong AAD")
	}
	if _, err := kms.Unwrap(ctx, wrapped, keyID); err == nil {
		t.Fatalf("Unwrap succeeded with default AAD")
	}
}

func TestGCPKMSKeyErrorsAndValidation(t *testing.T) {
	ctx := context.Background()

	if _, err := NewGCPKMSKey(GCPKMSConfig{KeyName: ""}); err == nil {
		t.Fatalf("expected error for empty KeyName")
	}
	if _, err := NewGCPKMSKey(GCPKMSConfig{KeyName: "projects/a/cryptoKeys/b?foo=bar"}); err == nil {
		t.Fatalf("expected error for invalid KeyName characters")
	}

	newWithRT := func(rt roundTripFunc, ts func(context.Context) (string, error)) *GCPKMSKey {
		if ts == nil {
			ts = func(context.Context) (string, error) { return "tok", nil }
		}
		k, err := NewGCPKMSKey(GCPKMSConfig{
			KeyName:     "projects/p/locations/l/keyRings/r/cryptoKeys/k",
			HTTPClient:  &http.Client{Transport: rt},
			TokenSource: ts,
		})
		if err != nil {
			t.Fatalf("NewGCPKMSKey: %v", err)
		}
		return k
	}

	// Empty inputs.
	k := newWithRT(func(*http.Request) (*http.Response, error) {
		t.Fatal("unexpected HTTP call")
		return nil, nil
	}, nil)
	if _, _, err := k.Wrap(ctx, nil); err == nil {
		t.Fatalf("Wrap(nil) succeeded")
	}
	if _, err := k.Unwrap(ctx, nil, k.KeyID()); err == nil {
		t.Fatalf("Unwrap(nil) succeeded")
	}

	// Token source failure.
	kTokErr := newWithRT(nil, func(context.Context) (string, error) {
		return "", errors.New("no token")
	})
	if _, _, err := kTokErr.Wrap(ctx, []byte("dek")); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Fatalf("Wrap token err = %v, want no token", err)
	}

	// HTTP 403 error.
	k403 := newWithRT(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusForbidden, map[string]string{"error": "permission denied"}), nil
	}, nil)
	if _, _, err := k403.Wrap(ctx, []byte("dek")); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("Wrap 403 err = %v, want 403", err)
	}
	if _, err := k403.Unwrap(ctx, []byte("ct"), k403.KeyID()); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("Unwrap 403 err = %v, want 403", err)
	}

	// Invalid base64 in response.
	kBadB64 := newWithRT(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, map[string]string{
			"ciphertext": "!!!not-base64!!!",
			"plaintext":  "!!!not-base64!!!",
		}), nil
	}, nil)
	if _, _, err := kBadB64.Wrap(ctx, []byte("dek")); err == nil {
		t.Fatalf("Wrap accepted invalid base64")
	}
	if _, err := kBadB64.Unwrap(ctx, []byte("ct"), kBadB64.KeyID()); err == nil {
		t.Fatalf("Unwrap accepted invalid base64")
	}

	// Empty ciphertext / plaintext in response.
	kEmpty := newWithRT(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, map[string]string{}), nil
	}, nil)
	if _, _, err := kEmpty.Wrap(ctx, []byte("dek")); err == nil {
		t.Fatalf("Wrap accepted empty ciphertext")
	}
	if _, err := kEmpty.Unwrap(ctx, []byte("ct"), kEmpty.KeyID()); err == nil {
		t.Fatalf("Unwrap accepted empty plaintext")
	}
}

func TestDefaultGCPTokenSourceEnvAndMetadata(t *testing.T) {
	ctx := context.Background()

	t.Run("env_fallback", func(t *testing.T) {
		t.Setenv(gcpAccessTokenEnv, "env-access-token")
		ts := newDefaultGCPTokenSource(&http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("metadata server should not be called when NORI_GCP_ACCESS_TOKEN is set")
				return nil, nil
			}),
		}, defaultMetadataURL, time.Now)
		tok, err := ts.Token(ctx)
		if err != nil || tok != "env-access-token" {
			t.Fatalf("Token() = (%q, %v), want env-access-token", tok, err)
		}
	})

	t.Run("metadata_fetch_and_cache", func(t *testing.T) {
		t.Setenv(gcpAccessTokenEnv, "")
		now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
		var fetches atomic.Int32

		ts := newDefaultGCPTokenSource(&http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != defaultMetadataURL {
					t.Errorf("url = %q, want %q", req.URL.String(), defaultMetadataURL)
				}
				if req.Header.Get("Metadata-Flavor") != "Google" {
					t.Errorf("Metadata-Flavor = %q, want Google", req.Header.Get("Metadata-Flavor"))
				}
				n := fetches.Add(1)
				tok := "meta-tok-1"
				if n > 1 {
					tok = "meta-tok-2"
				}
				return jsonResponse(http.StatusOK, metadataTokenResponse{
					AccessToken: tok,
					ExpiresIn:   300,
					TokenType:   "Bearer",
				}), nil
			}),
		}, defaultMetadataURL, func() time.Time { return now })

		tok1, err := ts.Token(ctx)
		if err != nil || tok1 != "meta-tok-1" {
			t.Fatalf("Token #1 = (%q, %v)", tok1, err)
		}
		// Second call within TTL uses cache.
		now = now.Add(100 * time.Second)
		tok2, err := ts.Token(ctx)
		if err != nil || tok2 != "meta-tok-1" || fetches.Load() != 1 {
			t.Fatalf("Token #2 = (%q, %v, fetches=%d), want cached meta-tok-1", tok2, err, fetches.Load())
		}
		// After expiry minus skew, refreshes from metadata server.
		now = now.Add(200 * time.Second)
		tok3, err := ts.Token(ctx)
		if err != nil || tok3 != "meta-tok-2" || fetches.Load() != 2 {
			t.Fatalf("Token #3 = (%q, %v, fetches=%d), want refreshed meta-tok-2", tok3, err, fetches.Load())
		}
	})
}
