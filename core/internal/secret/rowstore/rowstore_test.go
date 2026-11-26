package rowstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/secret"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

func newTestResourceStore(t *testing.T, label string) state.ResourceStore {
	t.Helper()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	backend := memstore.New(memstore.WithClock(func() time.Time { return now }))
	t.Cleanup(func() { _ = backend.Close() })
	u := users.User{
		ID:          uuid.NewSHA1(uuid.NameSpaceOID, []byte("rowstore-test:"+label)).String(),
		Email:       label + "@example.com",
		DisplayName: label,
		TimeZone:    "UTC",
	}
	db := records.NewShared(backend, u, 5.0)
	db.Now = func() time.Time { return now }
	return db
}

func mustParseHexKey(t *testing.T, fill byte) *MasterKey {
	t.Helper()
	raw := bytes.Repeat([]byte{fill}, 32)
	k, err := ParseMasterKey(hex.EncodeToString(raw))
	if err != nil {
		t.Fatalf("ParseMasterKey: %v", err)
	}
	return k
}

type rotatingKeyWrapper struct {
	primary *MasterKey
	all     []*MasterKey
}

func (r *rotatingKeyWrapper) Wrap(ctx context.Context, dataKey []byte) ([]byte, string, error) {
	return r.primary.Wrap(ctx, dataKey)
}

func (r *rotatingKeyWrapper) Unwrap(ctx context.Context, wrapped []byte, keyID string) ([]byte, error) {
	for _, k := range r.all {
		if k.id == keyID {
			return k.Unwrap(ctx, wrapped, keyID)
		}
	}
	return nil, fmt.Errorf("unknown key id %q", keyID)
}

func TestRoundTripConflictAndDelete(t *testing.T) {
	ctx := context.Background()
	res := newTestResourceStore(t, "alice")
	key := mustParseHexKey(t, 0x11)
	store := &Store{Resources: res, Keys: key, Owner: "alice"}

	if _, err := store.Get(ctx, "conn-1"); !errors.Is(err, secret.ErrCredentialNotFound) {
		t.Fatalf("Get missing = %v, want ErrCredentialNotFound", err)
	}

	plaintext := []byte(`{"access_token":"super-secret-oauth-token","refresh_token":"rt-123"}`)
	ver, err := store.Replace(ctx, "conn-1", 0, plaintext)
	if err != nil || ver != 1 {
		t.Fatalf("Replace create = (%d, %v), want (1, nil)", ver, err)
	}

	rawRes, err := res.Resource(ctx, Collection, "conn-1")
	if err != nil {
		t.Fatalf("Resource: %v", err)
	}
	if strings.Contains(string(rawRes.Body), "super-secret-oauth-token") {
		t.Fatalf("plaintext token leaked into stored resource body")
	}
	var storedRow row
	if err := rawRes.Decode(&storedRow); err != nil {
		t.Fatalf("Decode stored row: %v", err)
	}
	if !storedRow.Owner || storedRow.AADVersion != 2 {
		t.Fatalf("unexpected row metadata: owner=%v aad_version=%d", storedRow.Owner, storedRow.AADVersion)
	}

	got, err := store.Get(ctx, "conn-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Version != 1 || !bytes.Equal(got.Data, plaintext) {
		t.Fatalf("Get = (ver=%d, data=%q), want (1, %q)", got.Version, got.Data, plaintext)
	}

	if _, err := store.Replace(ctx, "conn-1", 0, []byte("stale")); !errors.Is(err, secret.ErrCredentialConflict) {
		t.Fatalf("Replace stale = %v, want ErrCredentialConflict", err)
	}

	updated := []byte(`{"access_token":"rotated-token"}`)
	ver2, err := store.Replace(ctx, "conn-1", 1, updated)
	if err != nil || ver2 != 2 {
		t.Fatalf("Replace update = (%d, %v), want (2, nil)", ver2, err)
	}
	got2, err := store.Get(ctx, "conn-1")
	if err != nil || got2.Version != 2 || !bytes.Equal(got2.Data, updated) {
		t.Fatalf("Get updated = (%+v, %v)", got2, err)
	}

	if err := store.Delete(ctx, "conn-1"); err != nil {
		t.Fatalf("Delete existing: %v", err)
	}
	if err := store.Delete(ctx, "conn-1"); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
	if _, err := store.Get(ctx, "conn-1"); !errors.Is(err, secret.ErrCredentialNotFound) {
		t.Fatalf("Get after delete = %v, want ErrCredentialNotFound", err)
	}

	if _, err := store.Replace(ctx, "../bad", 0, []byte("x")); err == nil {
		t.Fatalf("Replace accepted invalid connection id")
	}
	if _, err := store.Get(ctx, "../bad"); err == nil {
		t.Fatalf("Get accepted invalid connection id")
	}
	if err := store.Delete(ctx, "../bad"); err == nil {
		t.Fatalf("Delete accepted invalid connection id")
	}
	noOwner := &Store{Resources: res, Keys: key, Owner: ""}
	if _, err := noOwner.Replace(ctx, "conn-2", 0, []byte("x")); err == nil {
		t.Fatalf("Replace accepted empty owner")
	}
}

func TestCrossOwnerAndCrossResourceAADIsolation(t *testing.T) {
	ctx := context.Background()
	key := mustParseHexKey(t, 0x22)
	resAlice := newTestResourceStore(t, "alice")
	aliceStore := &Store{Resources: resAlice, Keys: key, Owner: "alice"}

	if _, err := aliceStore.Replace(ctx, "conn-1", 0, []byte("alice-secret")); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	bobOverAlice := &Store{Resources: resAlice, Keys: key, Owner: "bob"}
	if _, err := bobOverAlice.Get(ctx, "conn-1"); err == nil {
		t.Fatalf("cross-owner Get succeeded, want decryption error")
	}
	emptyOwnerOverAlice := &Store{Resources: resAlice, Keys: key, Owner: ""}
	if _, err := emptyOwnerOverAlice.Get(ctx, "conn-1"); err == nil {
		t.Fatalf("empty-owner Get succeeded, want decryption error")
	}

	rawRow, err := resAlice.Resource(ctx, Collection, "conn-1")
	if err != nil {
		t.Fatalf("Resource: %v", err)
	}
	copied := rawRow
	copied.ID = "conn-2"
	copied.Revision = 0
	if _, err := resAlice.PutResource(ctx, copied, 0); err != nil {
		t.Fatalf("PutResource copied: %v", err)
	}
	if _, err := aliceStore.Get(ctx, "conn-2"); err == nil {
		t.Fatalf("cross-resource Get succeeded, want decryption error")
	}

	if bytes.Equal(ownerAAD("a", "b:c"), ownerAAD("a:b", "c")) {
		t.Fatalf("v2 ownerAAD collided across colon boundary")
	}
	resA := newTestResourceStore(t, "owner-a")
	resAB := newTestResourceStore(t, "owner-ab")
	storeA := &Store{Resources: resA, Keys: key, Owner: "a"}
	storeAB := &Store{Resources: resAB, Keys: key, Owner: "a:b"}

	if _, err := storeA.Replace(ctx, "b:c", 0, []byte("delimiter-secret")); err != nil {
		t.Fatalf("storeA.Replace: %v", err)
	}
	rawColon, err := resA.Resource(ctx, Collection, "b:c")
	if err != nil {
		t.Fatalf("Resource(b:c): %v", err)
	}
	transplanted := rawColon
	transplanted.ID = "c"
	transplanted.Revision = 0
	if _, err := resAB.PutResource(ctx, transplanted, 0); err != nil {
		t.Fatalf("PutResource transplanted: %v", err)
	}
	if _, err := storeAB.Get(ctx, "c"); err == nil {
		t.Fatalf("delimiter-boundary transplant decrypted under (owner=a:b, id=c)")
	}

	dataKey := bytes.Repeat([]byte{0x55}, 32)
	wrapped, keyID, err := key.Wrap(ctx, dataKey)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	gcm, err := newGCM(dataKey)
	if err != nil {
		t.Fatalf("newGCM: %v", err)
	}
	nonce := bytes.Repeat([]byte{0x07}, gcm.NonceSize())
	legacyBody := row{
		WrappedKey: wrapped,
		KeyID:      keyID,
		Nonce:      nonce,
		Ciphertext: gcm.Seal(nil, nonce, []byte("legacy-secret"), legacyOwnerAAD("alice", "conn-legacy")),
		Owner:      true,
		AADVersion: 0,
	}
	legacyRes, err := state.NewResource(Collection, "conn-legacy", 1, legacyBody, nil, time.Now().UTC())
	if err != nil {
		t.Fatalf("NewResource: %v", err)
	}
	if _, err := resAlice.PutResource(ctx, legacyRes, 0); err != nil {
		t.Fatalf("PutResource legacy: %v", err)
	}
	gotLegacy, err := aliceStore.Get(ctx, "conn-legacy")
	if err != nil || !bytes.Equal(gotLegacy.Data, []byte("legacy-secret")) {
		t.Fatalf("legacy AADVersion=0 row failed to decrypt: (%+v, %v)", gotLegacy, err)
	}
}

func TestMalformedAndShortNoncesDoNotPanic(t *testing.T) {
	ctx := context.Background()
	key := mustParseHexKey(t, 0x33)
	res := newTestResourceStore(t, "malformed")
	store := &Store{Resources: res, Keys: key, Owner: "malformed"}

	if _, err := store.Replace(ctx, "conn-base", 0, []byte("payload")); err != nil {
		t.Fatalf("Replace base: %v", err)
	}
	baseRes, err := res.Resource(ctx, Collection, "conn-base")
	if err != nil {
		t.Fatalf("Resource base: %v", err)
	}
	var baseRow row
	if err := json.Unmarshal(baseRes.Body, &baseRow); err != nil {
		t.Fatalf("Unmarshal base row: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(r *row)
	}{
		{name: "empty_nonce", mutate: func(r *row) { r.Nonce = nil }},
		{name: "short_nonce_1_byte", mutate: func(r *row) { r.Nonce = []byte{0x01} }},
		{name: "short_nonce_11_bytes", mutate: func(r *row) { r.Nonce = make([]byte, 11) }},
		{name: "long_nonce_13_bytes", mutate: func(r *row) { r.Nonce = make([]byte, 13) }},
		{name: "corrupted_ciphertext", mutate: func(r *row) {
			cp := append([]byte(nil), r.Ciphertext...)
			cp[0] ^= 0xff
			r.Ciphertext = cp
		}},
		{name: "short_wrapped_key", mutate: func(r *row) { r.WrappedKey = []byte{1, 2, 3} }},
		{name: "empty_wrapped_key", mutate: func(r *row) { r.WrappedKey = nil }},
		{name: "wrong_key_id", mutate: func(r *row) { r.KeyID = "key:000000000000" }},
		{name: "missing_owner_flag", mutate: func(r *row) { r.Owner = false }},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mut := baseRow
			tc.mutate(&mut)
			id := fmt.Sprintf("conn-bad-%d", i)
			badRes, err := state.NewResource(Collection, id, 1, mut, nil, time.Now().UTC())
			if err != nil {
				t.Fatalf("NewResource: %v", err)
			}
			if _, err := res.PutResource(ctx, badRes, 0); err != nil {
				t.Fatalf("PutResource: %v", err)
			}
			if _, err := store.Get(ctx, id); err == nil {
				t.Fatalf("Get(%s) succeeded, want error", tc.name)
			}
		})
	}

	for _, wrapped := range [][]byte{nil, {}, {1, 2, 3, 4, 5}, make([]byte, 11), make([]byte, 12)} {
		if _, err := key.Unwrap(ctx, wrapped, key.id); err == nil {
			t.Errorf("Unwrap(len=%d) succeeded, want error", len(wrapped))
		}
	}
}

func TestMultiKeyRotation(t *testing.T) {
	ctx := context.Background()
	oldKey := mustParseHexKey(t, 0x44)
	newKey := mustParseHexKey(t, 0x55)
	if oldKey.id == newKey.id {
		t.Fatalf("expected distinct key IDs")
	}

	res := newTestResourceStore(t, "rotate")

	oldStore := &Store{Resources: res, Keys: oldKey, Owner: "rotate"}
	if _, err := oldStore.Replace(ctx, "conn-old", 0, []byte("secret-v1")); err != nil {
		t.Fatalf("oldStore.Replace: %v", err)
	}

	rotator := &rotatingKeyWrapper{primary: newKey, all: []*MasterKey{newKey, oldKey}}
	rotatedStore := &Store{Resources: res, Keys: rotator, Owner: "rotate"}

	gotOld, err := rotatedStore.Get(ctx, "conn-old")
	if err != nil || !bytes.Equal(gotOld.Data, []byte("secret-v1")) {
		t.Fatalf("rotatedStore.Get(conn-old) = (%+v, %v), want secret-v1", gotOld, err)
	}

	if _, err := rotatedStore.Replace(ctx, "conn-new", 0, []byte("secret-v2")); err != nil {
		t.Fatalf("rotatedStore.Replace(conn-new): %v", err)
	}
	rawNew, err := res.Resource(ctx, Collection, "conn-new")
	if err != nil {
		t.Fatalf("Resource(conn-new): %v", err)
	}
	var newRow row
	if err := rawNew.Decode(&newRow); err != nil {
		t.Fatalf("Decode(conn-new): %v", err)
	}
	if newRow.KeyID != newKey.id {
		t.Fatalf("conn-new KeyID = %q, want primary %q", newRow.KeyID, newKey.id)
	}

	if _, err := oldStore.Get(ctx, "conn-new"); err == nil {
		t.Fatalf("oldKey decrypted row written by newKey")
	}
	newOnlyStore := &Store{Resources: res, Keys: newKey, Owner: "rotate"}
	gotNew, err := newOnlyStore.Get(ctx, "conn-new")
	if err != nil || !bytes.Equal(gotNew.Data, []byte("secret-v2")) {
		t.Fatalf("newOnlyStore.Get(conn-new) = (%+v, %v), want secret-v2", gotNew, err)
	}

	if _, err := rotatedStore.Replace(ctx, "conn-old", gotOld.Version, []byte("secret-v1-rewrapped")); err != nil {
		t.Fatalf("re-wrap conn-old: %v", err)
	}
	gotRewrapped, err := newOnlyStore.Get(ctx, "conn-old")
	if err != nil || !bytes.Equal(gotRewrapped.Data, []byte("secret-v1-rewrapped")) {
		t.Fatalf("newOnlyStore.Get(rewrapped conn-old) = (%+v, %v)", gotRewrapped, err)
	}
}

func TestOpenLocalKeyAndParseMasterKey(t *testing.T) {
	ctx := context.Background()

	t.Run("create_and_reuse_in_tempdir", func(t *testing.T) {
		keyPath := filepath.Join(t.TempDir(), "nested", "master.key")
		k1, err := OpenLocalKey(keyPath)
		if err != nil {
			t.Fatalf("OpenLocalKey #1: %v", err)
		}
		info, err := os.Stat(keyPath)
		if err != nil {
			t.Fatalf("Stat key file: %v", err)
		}
		if info.Size() != 32 {
			t.Fatalf("key file size = %d, want 32", info.Size())
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("key file mode = %o, want 0600", perm)
		}

		wrapped, keyID, err := k1.Wrap(ctx, bytes.Repeat([]byte{0xaa}, 32))
		if err != nil {
			t.Fatalf("k1.Wrap: %v", err)
		}

		k2, err := OpenLocalKey(keyPath)
		if err != nil {
			t.Fatalf("OpenLocalKey #2: %v", err)
		}
		if k2.id != k1.id {
			t.Fatalf("reopened key id = %q, want %q", k2.id, k1.id)
		}
		unwrapped, err := k2.Unwrap(ctx, wrapped, keyID)
		if err != nil || !bytes.Equal(unwrapped, bytes.Repeat([]byte{0xaa}, 32)) {
			t.Fatalf("k2.Unwrap = (%x, %v)", unwrapped, err)
		}
	})

	t.Run("concurrent_open_converges_to_single_key", func(t *testing.T) {
		keyPath := filepath.Join(t.TempDir(), "race.key")
		const workers = 8
		keys := make([]*LocalKey, workers)
		errs := make([]error, workers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range workers {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-start
				keys[idx], errs[idx] = OpenLocalKey(keyPath)
			}(i)
		}
		close(start)
		wg.Wait()
		for i := range workers {
			if errs[i] != nil {
				t.Fatalf("worker %d OpenLocalKey: %v", i, errs[i])
			}
			if keys[i].id != keys[0].id {
				t.Fatalf("worker %d key id = %q, want %q", i, keys[i].id, keys[0].id)
			}
		}
	})

	t.Run("rejects_corrupt_short_key_file", func(t *testing.T) {
		badPath := filepath.Join(t.TempDir(), "short.key")
		if err := os.WriteFile(badPath, []byte("too-short"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if _, err := OpenLocalKey(badPath); err == nil {
			t.Fatalf("OpenLocalKey accepted 9-byte file")
		}
	})

	t.Run("parse_master_key_encodings", func(t *testing.T) {
		raw := bytes.Repeat([]byte{0x3c}, 32)
		valid := []string{
			hex.EncodeToString(raw),
			"  " + base64.StdEncoding.EncodeToString(raw) + "\n",
			base64.RawStdEncoding.EncodeToString(raw),
			base64.URLEncoding.EncodeToString(raw),
			base64.RawURLEncoding.EncodeToString(raw),
		}
		var firstID string
		for i, encoded := range valid {
			k, err := ParseMasterKey(encoded)
			if err != nil {
				t.Fatalf("ParseMasterKey[%d] failed: %v", i, err)
			}
			if firstID == "" {
				firstID = k.id
			} else if k.id != firstID {
				t.Fatalf("ParseMasterKey[%d] id = %q, want %q", i, k.id, firstID)
			}
		}
		for _, bad := range []string{"", "   ", "not-valid-key", hex.EncodeToString(make([]byte, 16))} {
			if _, err := ParseMasterKey(bad); err == nil {
				t.Errorf("ParseMasterKey(%q) succeeded, want error", bad)
			}
		}
	})
}
