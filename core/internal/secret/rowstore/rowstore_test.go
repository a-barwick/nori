package rowstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"nori/internal/secret"
	"nori/internal/state"
)

// memoryResources is a minimal state.ResourceStore holding one user's resources in memory. Production scopes each
// user's store by owner in the database; here each user simply gets their own instance.
type memoryResources struct {
	mu   sync.Mutex
	rows map[string]state.Resource
}

func newMemoryResources() *memoryResources {
	return &memoryResources{rows: map[string]state.Resource{}}
}

func key(collection, id string) string { return collection + "/" + id }

func (m *memoryResources) Resource(_ context.Context, collection, id string) (state.Resource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[key(collection, id)]
	if !ok {
		return state.Resource{}, state.ErrNotFound
	}
	return r, nil
}

func (m *memoryResources) Resources(context.Context, string, map[string]string, string, int) ([]state.Resource, string, error) {
	return nil, "", errors.New("not used")
}

func (m *memoryResources) Receipt(context.Context, string) (state.MutationReceipt, error) {
	return state.MutationReceipt{}, state.ErrNotFound
}

func (m *memoryResources) PutResource(_ context.Context, r state.Resource, expected int64) (state.Resource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.rows[key(r.Collection, r.ID)]
	if ok && current.Revision != expected || !ok && expected != 0 {
		return state.Resource{}, state.ErrRevisionConflict
	}
	r.Revision = expected + 1
	m.rows[key(r.Collection, r.ID)] = r
	return r, nil
}

func (m *memoryResources) Mutate(_ context.Context, mutation state.Mutation) (state.MutationReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var current *state.Resource
	if r, ok := m.rows[key(mutation.Collection, mutation.ID)]; ok {
		current = &r
	}
	result, err := mutation.Apply(current, func(collection, id string) (*state.Resource, error) {
		if r, ok := m.rows[key(collection, id)]; ok {
			return &r, nil
		}
		return nil, nil
	})
	if err != nil {
		return state.MutationReceipt{}, err
	}
	for _, w := range result.Writes {
		m.rows[key(w.Collection, w.ID)] = w
	}
	for _, d := range result.Deletes {
		delete(m.rows, key(d.Collection, d.ID))
	}
	return result.Receipt, nil
}

// rawRow returns the stored row exactly as the database would hold it.
func (m *memoryResources) rawRow(t *testing.T, id string) state.Resource {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[key(Collection, id)]
	if !ok {
		t.Fatalf("no stored row for %s", id)
	}
	return r
}

// fakeKMS stands in for Cloud KMS: AES-GCM under a master key per key name that never leaves it, checking the
// associated data on decrypt as KMS does.
type fakeKMS struct {
	keys  map[string][]byte
	calls int
}

func newFakeKMS(t *testing.T, names ...string) *fakeKMS {
	t.Helper()
	f := &fakeKMS{keys: map[string][]byte{}}
	for _, name := range names {
		master := make([]byte, 32)
		if _, err := rand.Read(master); err != nil {
			t.Fatal(err)
		}
		f.keys[name] = master
	}
	return f
}

func (f *fakeKMS) Encrypt(_ context.Context, name string, plaintext, aad []byte) ([]byte, error) {
	f.calls++
	master, ok := f.keys[name]
	if !ok {
		return nil, errors.New("fake kms: no such key")
	}
	gcm, err := newGCM(master)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, gcm.Seal(nil, nonce, plaintext, aad)...), nil
}

func (f *fakeKMS) Decrypt(_ context.Context, name string, ciphertext, aad []byte) ([]byte, error) {
	f.calls++
	master, ok := f.keys[name]
	if !ok {
		return nil, errors.New("fake kms: no such key")
	}
	gcm, err := newGCM(master)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("fake kms: ciphertext too short")
	}
	return gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], aad)
}

const (
	userA   = "11111111-1111-4111-8111-111111111111"
	userB   = "22222222-2222-4222-8222-222222222222"
	connID  = "conn_0123456789abcdef0123"
	kmsName = "projects/p/locations/us-central1/keyRings/nori-credentials/cryptoKeys/connected-accounts"
)

// token is credential material the way Connections stores it: an OAuth token as JSON.
var token = []byte(`{"access_token":"ya29.test-access-token-value","refresh_token":"1//test-refresh-token-value","token_type":"Bearer"}`)

func localKeys(t *testing.T) KeyWrapper {
	t.Helper()
	keys, err := OpenLocalKey(filepath.Join(t.TempDir(), "keys", "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func roundTrip(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	version, err := store.Replace(ctx, connID, 0, token)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	got, err := store.Get(ctx, connID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !bytes.Equal(got.Data, token) || got.Version != version {
		t.Fatalf("load returned %q at version %d, want the stored token at version %d", got.Data, got.Version, version)
	}
	// A stale writer is refused; the current version replaces.
	if _, err := store.Replace(ctx, connID, 0, []byte(`{"access_token":"other"}`)); !errors.Is(err, secret.ErrCredentialConflict) {
		t.Fatalf("stale replace: got %v, want ErrCredentialConflict", err)
	}
	next, err := store.Replace(ctx, connID, version, []byte(`{"access_token":"rotated"}`))
	if err != nil || next != version+1 {
		t.Fatalf("replace at the current version: version %d, err %v", next, err)
	}
	if got, err = store.Get(ctx, connID); err != nil || string(got.Data) != `{"access_token":"rotated"}` {
		t.Fatalf("load after replace: %q, %v", got.Data, err)
	}
	if err := store.Delete(ctx, connID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Get(ctx, connID); !errors.Is(err, secret.ErrCredentialNotFound) {
		t.Fatalf("load after delete: got %v, want ErrCredentialNotFound", err)
	}
	if err := store.Delete(ctx, connID); err != nil {
		t.Fatalf("deleting a missing credential is not an error: %v", err)
	}
}

func TestRoundTripLocalKeyFile(t *testing.T) {
	roundTrip(t, &Store{Resources: newMemoryResources(), Keys: localKeys(t), Owner: userA})
}

func TestRoundTripKMS(t *testing.T) {
	kms := newFakeKMS(t, kmsName)
	roundTrip(t, &Store{Resources: newMemoryResources(), Keys: NewCloudKMS(kmsName, kms), Owner: userA})
	if kms.calls == 0 {
		t.Fatal("the data keys were not wrapped through the KMS client")
	}
}

func TestLocalKeyFileReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	first, err := OpenLocalKey(path)
	if err != nil {
		t.Fatal(err)
	}
	resources := newMemoryResources()
	if _, err := (&Store{Resources: resources, Keys: first, Owner: userA}).Replace(context.Background(), connID, 0, token); err != nil {
		t.Fatal(err)
	}
	again, err := OpenLocalKey(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := (&Store{Resources: resources, Keys: again, Owner: userA}).Get(context.Background(), connID)
	if err != nil || !bytes.Equal(got.Data, token) {
		t.Fatalf("a reopened key file must read earlier rows: %q, %v", got.Data, err)
	}
}

func TestKMSWrapsWithItsKey(t *testing.T) {
	ctx := context.Background()
	kms := NewCloudKMS(kmsName, newFakeKMS(t, kmsName))
	dataKey := bytes.Repeat([]byte{7}, 32)
	wrapped, keyID, err := kms.Wrap(ctx, dataKey)
	if err != nil {
		t.Fatal(err)
	}
	if keyID != "kms:"+kmsName || bytes.Contains(wrapped, dataKey) {
		t.Fatalf("wrapped key id %q; wrapped bytes must not contain the data key", keyID)
	}
	if got, err := kms.Unwrap(ctx, wrapped, keyID); err != nil || !bytes.Equal(got, dataKey) {
		t.Fatalf("unwrap: %v", err)
	}
}

func TestKMSWrongKeyFails(t *testing.T) {
	ctx := context.Background()
	resources := newMemoryResources()
	if _, err := (&Store{Resources: resources, Keys: NewCloudKMS(kmsName, newFakeKMS(t, kmsName)), Owner: userA}).Replace(ctx, connID, 0, token); err != nil {
		t.Fatal(err)
	}
	// Another deployment's key name: refused before any key service call.
	other := NewCloudKMS("projects/p/locations/us-central1/keyRings/other/cryptoKeys/connected-accounts", newFakeKMS(t, "projects/p/locations/us-central1/keyRings/other/cryptoKeys/connected-accounts"))
	if _, err := (&Store{Resources: resources, Keys: other, Owner: userA}).Get(ctx, connID); err == nil {
		t.Fatal("a row wrapped by one key must not load with another key name")
	}
	// The same key name with different key material (a re-created key): the key service refuses to unwrap.
	if _, err := (&Store{Resources: resources, Keys: NewCloudKMS(kmsName, newFakeKMS(t, kmsName)), Owner: userA}).Get(ctx, connID); err == nil {
		t.Fatal("a row must not load when the key service holds different key material")
	}
	// A local key file cannot read a KMS row either.
	if _, err := (&Store{Resources: resources, Keys: localKeys(t), Owner: userA}).Get(ctx, connID); err == nil {
		t.Fatal("a KMS-wrapped row must not load with a local key")
	}
}

// tamper flips one byte of a stored row field and writes the row back.
func tamper(t *testing.T, resources *memoryResources, field string) {
	t.Helper()
	r := resources.rawRow(t, connID)
	var stored row
	if err := r.Decode(&stored); err != nil {
		t.Fatal(err)
	}
	switch field {
	case "ciphertext":
		stored.Ciphertext[len(stored.Ciphertext)/2] ^= 0x01
	case "wrapped_key":
		stored.WrappedKey[len(stored.WrappedKey)-1] ^= 0x01
	case "nonce":
		stored.Nonce[0] ^= 0x01
	}
	next, err := state.NewResource(Collection, connID, r.Revision, stored, nil, r.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	resources.mu.Lock()
	resources.rows[key(Collection, connID)] = next
	resources.mu.Unlock()
}

func TestTamperedRowFails(t *testing.T) {
	for _, field := range []string{"ciphertext", "wrapped_key", "nonce"} {
		t.Run(field, func(t *testing.T) {
			ctx := context.Background()
			resources := newMemoryResources()
			store := &Store{Resources: resources, Keys: NewCloudKMS(kmsName, newFakeKMS(t, kmsName)), Owner: userA}
			if _, err := store.Replace(ctx, connID, 0, token); err != nil {
				t.Fatal(err)
			}
			tamper(t, resources, field)
			if got, err := store.Get(ctx, connID); err == nil {
				t.Fatalf("a tampered %s must not decrypt, got %q", field, got.Data)
			}
		})
	}
}

func TestStoredRowHoldsNoPlaintext(t *testing.T) {
	for name, keys := range map[string]func(t *testing.T) KeyWrapper{
		"local": localKeys,
		"kms":   func(t *testing.T) KeyWrapper { return NewCloudKMS(kmsName, newFakeKMS(t, kmsName)) },
	} {
		t.Run(name, func(t *testing.T) {
			resources := newMemoryResources()
			store := &Store{Resources: resources, Keys: keys(t), Owner: userA}
			if _, err := store.Replace(context.Background(), connID, 0, token); err != nil {
				t.Fatal(err)
			}
			r := resources.rawRow(t, connID)
			stored, _ := json.Marshal(r)
			for _, secretText := range []string{"ya29.test-access-token-value", "1//test-refresh-token-value", "access_token", "refresh_token"} {
				for _, form := range []string{secretText, base64.StdEncoding.EncodeToString([]byte(secretText)), base64.RawURLEncoding.EncodeToString([]byte(secretText))} {
					if bytes.Contains(stored, []byte(form)) {
						t.Fatalf("the stored row contains %q in the clear", secretText)
					}
				}
			}
			var fields map[string]any
			if err := json.Unmarshal(r.Body, &fields); err != nil {
				t.Fatal(err)
			}
			for name := range fields {
				switch name {
				case "wrapped_key", "key_id", "nonce", "ciphertext", "owner":
				default:
					t.Fatalf("the stored row has an unexpected field %q", name)
				}
			}
		})
	}
}

func TestPerUserIsolation(t *testing.T) {
	ctx := context.Background()
	kms := newFakeKMS(t, kmsName)
	resourcesA, resourcesB := newMemoryResources(), newMemoryResources()
	storeA := &Store{Resources: resourcesA, Keys: NewCloudKMS(kmsName, kms), Owner: userA}
	storeB := &Store{Resources: resourcesB, Keys: NewCloudKMS(kmsName, kms), Owner: userB}
	if _, err := storeA.Replace(ctx, connID, 0, token); err != nil {
		t.Fatal(err)
	}
	// User B's store does not see user A's row.
	if _, err := storeB.Get(ctx, connID); !errors.Is(err, secret.ErrCredentialNotFound) {
		t.Fatalf("user B read user A's credential: %v", err)
	}
	// Deleting from user B's store leaves user A's row.
	if err := storeB.Delete(ctx, connID); err != nil {
		t.Fatal(err)
	}
	if got, err := storeA.Get(ctx, connID); err != nil || !bytes.Equal(got.Data, token) {
		t.Fatalf("user A's credential after user B's delete: %v", err)
	}
	// A row copied into user B's store under the same connection ID does not decrypt there, although both users'
	// data keys are wrapped by the same deployment key: the ciphertext is bound to its owner.
	copied := resourcesA.rawRow(t, connID)
	resourcesB.mu.Lock()
	resourcesB.rows[key(Collection, connID)] = copied
	resourcesB.mu.Unlock()
	if got, err := storeB.Get(ctx, connID); err == nil {
		t.Fatalf("a row copied from user A decrypted in user B's store: %q", got.Data)
	}
	// And within one user, a row moved to another connection ID does not decrypt either.
	moved := resourcesA.rawRow(t, connID)
	moved.ID = "conn_ffffffffffffffffffff"
	resourcesA.mu.Lock()
	resourcesA.rows[key(Collection, moved.ID)] = moved
	resourcesA.mu.Unlock()
	if _, err := storeA.Get(ctx, moved.ID); err == nil {
		t.Fatal("a row moved to another connection decrypted")
	}
}

func TestStoreNeedsOwner(t *testing.T) {
	store := &Store{Resources: newMemoryResources(), Keys: localKeys(t)}
	if _, err := store.Replace(context.Background(), connID, 0, token); err == nil {
		t.Fatal("a store without an owner must not write")
	}
}

// Rows written before owner binding (no owner flag) still load.
func TestRowWithoutOwnerBindingLoads(t *testing.T) {
	ctx := context.Background()
	keys := localKeys(t)
	resources := newMemoryResources()
	dataKey := bytes.Repeat([]byte{9}, 32)
	gcm, err := newGCM(dataKey)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	wrapped, keyID, err := keys.Wrap(ctx, dataKey)
	if err != nil {
		t.Fatal(err)
	}
	old, err := state.NewResource(Collection, connID, 1, row{WrappedKey: wrapped, KeyID: keyID, Nonce: nonce,
		Ciphertext: gcm.Seal(nil, nonce, token, aad(connID))}, nil, (&Store{}).now())
	if err != nil {
		t.Fatal(err)
	}
	resources.rows[key(Collection, connID)] = old
	got, err := (&Store{Resources: resources, Keys: keys, Owner: userA}).Get(ctx, connID)
	if err != nil || !bytes.Equal(got.Data, token) {
		t.Fatalf("an earlier row must still load: %v", err)
	}
}
