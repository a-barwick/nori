package rowstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"
)

func TestParseKeyRingAndRotation(t *testing.T) {
	ctx := context.Background()
	raw1 := bytes.Repeat([]byte{0x11}, 32)
	raw2 := bytes.Repeat([]byte{0x22}, 32)
	raw3 := bytes.Repeat([]byte{0x33}, 32)

	k1, err := ParseMasterKey(hex.EncodeToString(raw1))
	if err != nil {
		t.Fatalf("ParseMasterKey k1: %v", err)
	}
	k2, err := ParseMasterKey(base64.StdEncoding.EncodeToString(raw2))
	if err != nil {
		t.Fatalf("ParseMasterKey k2: %v", err)
	}
	k3, err := ParseMasterKey(base64.RawURLEncoding.EncodeToString(raw3))
	if err != nil {
		t.Fatalf("ParseMasterKey k3: %v", err)
	}

	// Single-key ring works cleanly.
	singleRing, err := ParseKeyRing(hex.EncodeToString(raw1))
	if err != nil {
		t.Fatalf("ParseKeyRing single: %v", err)
	}
	dek := bytes.Repeat([]byte{0xab}, 32)
	w1, id1, err := singleRing.Wrap(ctx, dek)
	if err != nil {
		t.Fatalf("singleRing.Wrap: %v", err)
	}
	if id1 != k1.id {
		t.Fatalf("singleRing keyID = %q, want %q", id1, k1.id)
	}

	// Multi-key ring: k3 (primary), k2 (fallback 1), k1 (fallback 2), mixed hex/base64 + whitespace.
	encoded := " " + base64.RawURLEncoding.EncodeToString(raw3) + " , " +
		base64.StdEncoding.EncodeToString(raw2) + " , " +
		hex.EncodeToString(raw1) + " "
	ring, err := ParseKeyRing(encoded)
	if err != nil {
		t.Fatalf("ParseKeyRing multi: %v", err)
	}

	// Wrap always uses primary (k3).
	w3, id3, err := ring.Wrap(ctx, dek)
	if err != nil {
		t.Fatalf("ring.Wrap: %v", err)
	}
	if id3 != k3.id {
		t.Fatalf("ring.Wrap keyID = %q, want primary %q", id3, k3.id)
	}

	// Wrap with k2 directly, then unwrap all three through ring.
	w2, id2, err := k2.Wrap(ctx, dek)
	if err != nil {
		t.Fatalf("k2.Wrap: %v", err)
	}

	for name, tc := range map[string]struct {
		wrapped []byte
		keyID   string
	}{
		"primary_k3":  {w3, id3},
		"fallback_k2": {w2, id2},
		"fallback_k1": {w1, id1},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ring.Unwrap(ctx, tc.wrapped, tc.keyID)
			if err != nil {
				t.Fatalf("ring.Unwrap(%s): %v", name, err)
			}
			if !bytes.Equal(got, dek) {
				t.Fatalf("ring.Unwrap(%s) mismatch", name)
			}
		})
	}

	// Unknown key ID fails.
	if _, err := ring.Unwrap(ctx, w1, "key:000000000000"); err == nil {
		t.Fatalf("ring.Unwrap with unknown keyID succeeded, want error")
	}

	// Tampered ciphertext fails even with valid key ID.
	tampered := append([]byte(nil), w2...)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := ring.Unwrap(ctx, tampered, id2); err == nil {
		t.Fatalf("ring.Unwrap with tampered ciphertext succeeded, want error")
	}

	// Retiring k1 from the ring prevents unwrapping w1 while k2 and k3 still succeed.
	retiredRing, err := NewKeyRing(k3, k2)
	if err != nil {
		t.Fatalf("NewKeyRing retired: %v", err)
	}
	if _, err := retiredRing.Unwrap(ctx, w1, id1); err == nil {
		t.Fatalf("retiredRing unwrapped retired key k1")
	}
	if got, err := retiredRing.Unwrap(ctx, w2, id2); err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("retiredRing.Unwrap(k2) = (%x, %v)", got, err)
	}
}

func TestKeyRingValidationAndStoreIntegration(t *testing.T) {
	ctx := context.Background()

	if _, err := NewKeyRing(nil); err == nil {
		t.Fatalf("NewKeyRing(nil) succeeded, want error")
	}
	k1 := mustParseHexKey(t, 0x61)
	k2 := mustParseHexKey(t, 0x62)
	if _, err := NewKeyRing(k1, nil); err == nil {
		t.Fatalf("NewKeyRing(k1, nil) succeeded, want error")
	}

	for _, bad := range []string{"", "   ", ",", hex.EncodeToString(bytes.Repeat([]byte{1}, 32)) + ",", "not-a-key"} {
		if _, err := ParseKeyRing(bad); err == nil {
			t.Errorf("ParseKeyRing(%q) succeeded, want error", bad)
		}
	}

	// Context cancellation stops fallback scan.
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()
	ring, err := NewKeyRing(k2, k1)
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}
	w1, id1, err := k1.Wrap(ctx, bytes.Repeat([]byte{0x77}, 32))
	if err != nil {
		t.Fatalf("k1.Wrap: %v", err)
	}
	if _, err := ring.Unwrap(cancelledCtx, w1, id1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Unwrap with cancelled ctx = %v, want context.Canceled", err)
	}

	// End-to-end with rowstore.Store.
	res := newTestResourceStore(t, "keyring-store")
	storeV1 := &Store{Resources: res, Keys: k1, Owner: "alice"}
	if _, err := storeV1.Replace(ctx, "conn-1", 0, []byte("token-v1")); err != nil {
		t.Fatalf("storeV1.Replace: %v", err)
	}

	storeV2 := &Store{Resources: res, Keys: ring, Owner: "alice"}
	got, err := storeV2.Get(ctx, "conn-1")
	if err != nil || string(got.Data) != "token-v1" {
		t.Fatalf("storeV2.Get(conn-1) = (%+v, %v), want token-v1", got, err)
	}
	if _, err := storeV2.Replace(ctx, "conn-2", 0, []byte("token-v2")); err != nil {
		t.Fatalf("storeV2.Replace(conn-2): %v", err)
	}

	// k2 alone reads conn-2 (written with ring primary) but not conn-1 (still wrapped with k1).
	storeK2Only := &Store{Resources: res, Keys: k2, Owner: "alice"}
	if got2, err := storeK2Only.Get(ctx, "conn-2"); err != nil || string(got2.Data) != "token-v2" {
		t.Fatalf("storeK2Only.Get(conn-2) = (%+v, %v)", got2, err)
	}
	if _, err := storeK2Only.Get(ctx, "conn-1"); err == nil {
		t.Fatalf("storeK2Only unexpectedly decrypted k1 row")
	}
}

func TestKeyRingAndMasterKeyWrapWithAAD(t *testing.T) {
	ctx := context.Background()
	k1 := mustParseHexKey(t, 0x71)
	k2 := mustParseHexKey(t, 0x72)
	ring, err := NewKeyRing(k2, k1)
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}

	dek := bytes.Repeat([]byte{0xde}, 32)
	aadAlice := []byte("nori-tenant-dek:v1:5:alice")
	aadBob := []byte("nori-tenant-dek:v1:3:bob")

	w1, id1, err := k1.WrapWithAAD(ctx, dek, aadAlice)
	if err != nil {
		t.Fatalf("k1.WrapWithAAD: %v", err)
	}
	// Unwrap through ring fallback succeeds with matching AAD.
	got, err := ring.UnwrapWithAAD(ctx, w1, id1, aadAlice)
	if err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("ring.UnwrapWithAAD(matching) = (%x, %v)", got, err)
	}
	// Wrong AAD or default AAD must fail.
	if _, err := ring.UnwrapWithAAD(ctx, w1, id1, aadBob); err == nil {
		t.Fatalf("ring.UnwrapWithAAD succeeded with wrong AAD")
	}
	if _, err := ring.Unwrap(ctx, w1, id1); err == nil {
		t.Fatalf("ring.Unwrap succeeded without owner AAD")
	}

	// Primary WrapWithAAD round-trip.
	w2, id2, err := ring.WrapWithAAD(ctx, dek, aadBob)
	if err != nil {
		t.Fatalf("ring.WrapWithAAD: %v", err)
	}
	if got2, err := ring.UnwrapWithAAD(ctx, w2, id2, aadBob); err != nil || !bytes.Equal(got2, dek) {
		t.Fatalf("ring.UnwrapWithAAD(primary) = (%x, %v)", got2, err)
	}
	if _, err := ring.UnwrapWithAAD(ctx, w2, id2, aadAlice); err == nil {
		t.Fatalf("ring.UnwrapWithAAD(primary) succeeded with wrong AAD")
	}
}
