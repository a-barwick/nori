package tenantkey

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/secret/rowstore"
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
		ID:          uuid.NewSHA1(uuid.NameSpaceOID, []byte("tenantkey-test:"+label)).String(),
		Email:       label + "@example.com",
		DisplayName: label,
		TimeZone:    "UTC",
	}
	db := records.NewShared(backend, u, 5.0)
	db.Now = func() time.Time { return now }
	return db
}

func mustKey(t *testing.T, fill byte) *rowstore.MasterKey {
	t.Helper()
	k, err := rowstore.ParseMasterKey(hex.EncodeToString(bytes.Repeat([]byte{fill}, 32)))
	if err != nil {
		t.Fatalf("ParseMasterKey: %v", err)
	}
	return k
}

func TestSealOpenRoundTripAndAAD(t *testing.T) {
	ctx := context.Background()
	res := newTestResourceStore(t, "alice")
	key := mustKey(t, 0x11)

	mgr, err := NewManager(res, key, "alice")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	plaintext := []byte("sensitive user payload")
	ct1, err := mgr.Seal(ctx, "message", "msg-1", plaintext)
	if err != nil {
		t.Fatalf("Seal #1: %v", err)
	}
	ct2, err := mgr.Seal(ctx, "message", "msg-1", plaintext)
	if err != nil {
		t.Fatalf("Seal #2: %v", err)
	}
	if bytes.Equal(ct1, ct2) {
		t.Fatalf("two Seal calls produced identical ciphertext (nonce reuse)")
	}
	if bytes.Contains(ct1, plaintext) {
		t.Fatalf("ciphertext contains plaintext")
	}

	// Stored resource row exists under tenant_keys/primary.
	rawRow, err := res.Resource(ctx, Collection, PrimaryKeyID)
	if err != nil {
		t.Fatalf("Resource(tenant_keys/primary): %v", err)
	}
	var stored wrappedDEKRow
	if err := rawRow.Decode(&stored); err != nil {
		t.Fatalf("Decode wrappedDEKRow: %v", err)
	}
	if stored.Owner != "alice" || len(stored.WrappedKey) == 0 {
		t.Fatalf("unexpected wrappedDEKRow: %+v", stored)
	}

	// Open succeeds with matching owner, scope, recordID (both cached and cold manager).
	got, err := mgr.Open(ctx, "message", "msg-1", ct1)
	if err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("Open cached = (%q, %v)", got, err)
	}
	coldMgr, err := NewManager(res, key, "alice")
	if err != nil {
		t.Fatalf("NewManager cold: %v", err)
	}
	gotCold, err := coldMgr.Open(ctx, "message", "msg-1", ct1)
	if err != nil || !bytes.Equal(gotCold, plaintext) {
		t.Fatalf("Open cold = (%q, %v)", gotCold, err)
	}

	// Empty plaintext round-trips cleanly.
	emptyCT, err := mgr.Seal(ctx, "message", "msg-empty", nil)
	if err != nil {
		t.Fatalf("Seal empty: %v", err)
	}
	emptyPT, err := mgr.Open(ctx, "message", "msg-empty", emptyCT)
	if err != nil || len(emptyPT) != 0 {
		t.Fatalf("Open empty = (%q, %v)", emptyPT, err)
	}

	// Tamper detection.
	tampered := append([]byte(nil), ct1...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := mgr.Open(ctx, "message", "msg-1", tampered); err == nil {
		t.Fatalf("Open succeeded on tampered ciphertext")
	}
	if _, err := mgr.Open(ctx, "message", "msg-1", ct1[:5]); err == nil {
		t.Fatalf("Open succeeded on short ciphertext")
	}

	// Wrong scope or recordID fails AAD.
	if _, err := mgr.Open(ctx, "other-scope", "msg-1", ct1); err == nil {
		t.Fatalf("Open succeeded with wrong scope")
	}
	if _, err := mgr.Open(ctx, "message", "msg-2", ct1); err == nil {
		t.Fatalf("Open succeeded with wrong recordID")
	}

	// Wrong owner fails (both at stored row owner check and AAD).
	bobMgr, err := NewManager(res, key, "bob")
	if err != nil {
		t.Fatalf("NewManager bob: %v", err)
	}
	if _, err := bobMgr.Open(ctx, "message", "msg-1", ct1); err == nil {
		t.Fatalf("Open succeeded with wrong owner")
	}

	// Delimiter-ambiguity safety on AAD.
	if bytes.Equal(dataAAD("a", "b\x00c", "d"), dataAAD("a", "b", "c\x00d")) {
		t.Fatalf("dataAAD collided across NUL boundary")
	}
}

func TestShredCryptoShredding(t *testing.T) {
	ctx := context.Background()
	res := newTestResourceStore(t, "shred")
	key := mustKey(t, 0x22)

	mgr, err := NewManager(res, key, "shred-user")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ct, err := mgr.Seal(ctx, "artifact:file", "uploads/1", []byte("top-secret"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if pt, err := mgr.Open(ctx, "artifact:file", "uploads/1", ct); err != nil || string(pt) != "top-secret" {
		t.Fatalf("Open before Shred = (%q, %v)", pt, err)
	}

	// Shred overwrites the DEK row with a tombstone and evicts the cache.
	if err := mgr.Shred(ctx); err != nil {
		t.Fatalf("Shred: %v", err)
	}
	// Shred is idempotent.
	if err := mgr.Shred(ctx); err != nil {
		t.Fatalf("Shred #2: %v", err)
	}

	// Stored resource row is now a tombstone with Shredded=true and no wrapped key material.
	rawRow, err := res.Resource(ctx, Collection, PrimaryKeyID)
	if err != nil {
		t.Fatalf("Resource after Shred: %v", err)
	}
	var tombstone wrappedDEKRow
	if err := rawRow.Decode(&tombstone); err != nil {
		t.Fatalf("Decode tombstone: %v", err)
	}
	if !tombstone.Shredded || tombstone.Owner != "shred-user" || len(tombstone.WrappedKey) != 0 {
		t.Fatalf("unexpected tombstone row: %+v", tombstone)
	}

	// Open fails closed with ErrKeyShredded (which also matches ErrKeyNotFound).
	if _, err := mgr.Open(ctx, "artifact:file", "uploads/1", ct); !errors.Is(err, ErrKeyShredded) || !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("Open after Shred = %v, want ErrKeyShredded", err)
	}

	// Seal after Shred MUST fail closed with ErrKeyShredded (never silently create a new DEK).
	if _, err := mgr.Seal(ctx, "artifact:file", "uploads/2", []byte("post-shred")); !errors.Is(err, ErrKeyShredded) {
		t.Fatalf("Seal after Shred = %v, want ErrKeyShredded", err)
	}

	// Cold manager also fails closed on both Open and Seal.
	coldMgr, err := NewManager(res, key, "shred-user")
	if err != nil {
		t.Fatalf("NewManager cold: %v", err)
	}
	if _, err := coldMgr.Open(ctx, "artifact:file", "uploads/1", ct); !errors.Is(err, ErrKeyShredded) {
		t.Fatalf("cold Open after Shred = %v, want ErrKeyShredded", err)
	}
	if _, err := coldMgr.Seal(ctx, "artifact:file", "uploads/3", []byte("post-shred-cold")); !errors.Is(err, ErrKeyShredded) {
		t.Fatalf("cold Seal after Shred = %v, want ErrKeyShredded", err)
	}
}

func TestWrappedDEKRowSwapAcrossOwnersFailsUnwrapWithAAD(t *testing.T) {
	ctx := context.Background()
	key := mustKey(t, 0x2a)
	resAlice := newTestResourceStore(t, "swap-alice")
	resBob := newTestResourceStore(t, "swap-bob")

	aliceMgr, err := NewManager(resAlice, key, "alice")
	if err != nil {
		t.Fatalf("NewManager alice: %v", err)
	}
	if _, err := aliceMgr.Seal(ctx, "doc", "1", []byte("alice-secret")); err != nil {
		t.Fatalf("alice Seal: %v", err)
	}

	// Read Alice's wrapped DEK row and transplant it into Bob's store, even forging
	// the plaintext JSON Owner field to "bob". Because the wrapped DEK is bound to
	// Alice via WrapWithAAD("nori-tenant-dek:v1:5:alice"), Bob's UnwrapWithAAD MUST fail.
	aliceRowRes, err := resAlice.Resource(ctx, Collection, PrimaryKeyID)
	if err != nil {
		t.Fatalf("Resource(alice): %v", err)
	}
	var swapped wrappedDEKRow
	if err := aliceRowRes.Decode(&swapped); err != nil {
		t.Fatalf("Decode alice row: %v", err)
	}
	swapped.Owner = "bob"
	forgedRes, err := state.NewResource(Collection, PrimaryKeyID, 1, swapped, nil, time.Now().UTC())
	if err != nil {
		t.Fatalf("NewResource forged: %v", err)
	}
	if _, err := resBob.PutResource(ctx, forgedRes, 0); err != nil {
		t.Fatalf("PutResource forged into bob: %v", err)
	}

	bobMgr, err := NewManager(resBob, key, "bob")
	if err != nil {
		t.Fatalf("NewManager bob: %v", err)
	}
	if _, err := bobMgr.Seal(ctx, "doc", "1", []byte("bob-attempt")); err == nil {
		t.Fatalf("bobMgr.Seal succeeded on swapped wrapped DEK row; expected UnwrapWithAAD failure")
	}
	if _, err := bobMgr.Open(ctx, "doc", "1", []byte("0123456789abcdef0123456789abcdef")); err == nil {
		t.Fatalf("bobMgr.Open succeeded on swapped wrapped DEK row; expected UnwrapWithAAD failure")
	}
}

type countingAADKeyWrapper struct {
	inner       rowstore.AADKeyWrapper
	wrapCalls   atomic.Int32
	unwrapCalls atomic.Int32
}

func (c *countingAADKeyWrapper) Wrap(ctx context.Context, dataKey []byte) ([]byte, string, error) {
	c.wrapCalls.Add(1)
	return c.inner.Wrap(ctx, dataKey)
}

func (c *countingAADKeyWrapper) Unwrap(ctx context.Context, wrapped []byte, keyID string) ([]byte, error) {
	c.unwrapCalls.Add(1)
	return c.inner.Unwrap(ctx, wrapped, keyID)
}

func (c *countingAADKeyWrapper) WrapWithAAD(ctx context.Context, dataKey, aad []byte) ([]byte, string, error) {
	c.wrapCalls.Add(1)
	return c.inner.WrapWithAAD(ctx, dataKey, aad)
}

func (c *countingAADKeyWrapper) UnwrapWithAAD(ctx context.Context, wrapped []byte, keyID string, aad []byte) ([]byte, error) {
	c.unwrapCalls.Add(1)
	return c.inner.UnwrapWithAAD(ctx, wrapped, keyID, aad)
}

func TestSharedDEKCacheTTLAndShredEviction(t *testing.T) {
	ctx := context.Background()
	res := newTestResourceStore(t, "shared-cache")
	wrapper := &countingAADKeyWrapper{inner: mustKey(t, 0x5c)}
	cache := NewCache(5 * time.Second)
	defer cache.Close()

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	newMgr := func() *Manager {
		m, err := NewManager(res, wrapper, "alice")
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}
		m.Cache = cache
		m.Now = clock
		return m
	}

	mgr1 := newMgr()
	ct, err := mgr1.Seal(ctx, "msg", "1", []byte("cached-secret"))
	if err != nil {
		t.Fatalf("mgr1.Seal: %v", err)
	}
	if wrapper.wrapCalls.Load() != 1 || wrapper.unwrapCalls.Load() != 0 {
		t.Fatalf("after Seal: wraps=%d unwraps=%d, want 1/0", wrapper.wrapCalls.Load(), wrapper.unwrapCalls.Load())
	}

	// Second fresh Manager sharing the same Cache reuses the cached DEK with zero Unwrap calls.
	mgr2 := newMgr()
	pt, err := mgr2.Open(ctx, "msg", "1", ct)
	if err != nil || string(pt) != "cached-secret" {
		t.Fatalf("mgr2.Open = (%q, %v)", pt, err)
	}
	if wrapper.unwrapCalls.Load() != 0 {
		t.Fatalf("mgr2.Open triggered %d Unwrap calls despite warm shared cache", wrapper.unwrapCalls.Load())
	}

	// Advance past TTL: next Manager must unwrap from KMS once and repopulate cache.
	now = now.Add(6 * time.Second)
	mgr3 := newMgr()
	pt, err = mgr3.Open(ctx, "msg", "1", ct)
	if err != nil || string(pt) != "cached-secret" {
		t.Fatalf("mgr3.Open after TTL = (%q, %v)", pt, err)
	}
	if wrapper.unwrapCalls.Load() != 1 {
		t.Fatalf("unwrapCalls after TTL = %d, want 1", wrapper.unwrapCalls.Load())
	}

	// Shred immediately evicts the shared cache so mgr2 sees ErrKeyShredded without waiting for TTL.
	if err := mgr3.Shred(ctx); err != nil {
		t.Fatalf("Shred: %v", err)
	}
	if _, err := mgr2.Open(ctx, "msg", "1", ct); !errors.Is(err, ErrKeyShredded) {
		t.Fatalf("mgr2.Open after Shred = %v, want ErrKeyShredded", err)
	}
}

// conflictOnceResourceStore injects a single ErrRevisionConflict on PutResource
// after pre-populating the winning row from a peer manager, deterministically
// exercising the concurrent first-time creation conflict path.
type conflictOnceResourceStore struct {
	state.ResourceStore
	peer     *Manager
	injected atomic.Bool
}

func (c *conflictOnceResourceStore) PutResource(ctx context.Context, r state.Resource, expected int64) (state.Resource, error) {
	if r.Collection == Collection && r.ID == PrimaryKeyID && expected == 0 && c.injected.CompareAndSwap(false, true) {
		// Peer wins the race and writes the DEK first.
		if _, err := c.peer.Seal(ctx, "peer", "init", []byte("winner")); err != nil {
			return state.Resource{}, err
		}
		return state.Resource{}, state.ErrRevisionConflict
	}
	return c.ResourceStore.PutResource(ctx, r, expected)
}

func TestConcurrentFirstTimeCreation(t *testing.T) {
	ctx := context.Background()
	key := mustKey(t, 0x33)

	t.Run("deterministic_revision_conflict_recovery", func(t *testing.T) {
		res := newTestResourceStore(t, "conflict-det")
		peer, err := NewManager(res, key, "alice")
		if err != nil {
			t.Fatalf("NewManager peer: %v", err)
		}
		wrappedRes := &conflictOnceResourceStore{ResourceStore: res, peer: peer}
		loser, err := NewManager(wrappedRes, key, "alice")
		if err != nil {
			t.Fatalf("NewManager loser: %v", err)
		}

		ct, err := loser.Seal(ctx, "doc", "1", []byte("hello"))
		if err != nil {
			t.Fatalf("loser.Seal after conflict: %v", err)
		}
		// Peer must be able to decrypt loser's ciphertext because loser adopted peer's DEK.
		pt, err := peer.Open(ctx, "doc", "1", ct)
		if err != nil || string(pt) != "hello" {
			t.Fatalf("peer.Open(loser ct) = (%q, %v)", pt, err)
		}
	})

	t.Run("parallel_managers_converge", func(t *testing.T) {
		res := newTestResourceStore(t, "conflict-race")
		const workers = 8
		mgrs := make([]*Manager, workers)
		cts := make([][]byte, workers)
		errs := make([]error, workers)
		for i := range workers {
			var err error
			mgrs[i], err = NewManager(res, key, "alice")
			if err != nil {
				t.Fatalf("NewManager[%d]: %v", i, err)
			}
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range workers {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-start
				cts[idx], errs[idx] = mgrs[idx].Seal(ctx, "doc", "shared", []byte("payload"))
			}(i)
		}
		close(start)
		wg.Wait()

		for i := range workers {
			if errs[i] != nil {
				t.Fatalf("worker %d Seal: %v", i, errs[i])
			}
			// Manager 0 can decrypt every other manager's ciphertext.
			pt, err := mgrs[0].Open(ctx, "doc", "shared", cts[i])
			if err != nil || string(pt) != "payload" {
				t.Fatalf("mgr[0].Open(worker %d) = (%q, %v)", i, pt, err)
			}
		}
	})
}

func TestKeyRingRotationWithTenantKey(t *testing.T) {
	ctx := context.Background()
	res := newTestResourceStore(t, "rotate-tenant")
	k1 := mustKey(t, 0x41)
	k2 := mustKey(t, 0x42)

	mgrV1, err := NewManager(res, k1, "alice")
	if err != nil {
		t.Fatalf("NewManager v1: %v", err)
	}
	ct, err := mgrV1.Seal(ctx, "note", "n1", []byte("remember this"))
	if err != nil {
		t.Fatalf("Seal v1: %v", err)
	}

	// Rotate master key to KeyRing(k2, k1); cold manager unwraps existing DEK via fallback k1.
	ring, err := rowstore.NewKeyRing(k2, k1)
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}
	mgrV2, err := NewManager(res, ring, "alice")
	if err != nil {
		t.Fatalf("NewManager v2: %v", err)
	}
	pt, err := mgrV2.Open(ctx, "note", "n1", ct)
	if err != nil || !strings.EqualFold(string(pt), "remember this") {
		t.Fatalf("Open after master key rotation = (%q, %v)", pt, err)
	}
}

func TestDefaultTTL10mAndEvictAll(t *testing.T) {
	if DefaultCacheTTL != 10*time.Minute {
		t.Fatalf("DefaultCacheTTL = %v, want 10m", DefaultCacheTTL)
	}
	ctx := context.Background()
	res := newTestResourceStore(t, "evict-all")
	wrapper := &countingAADKeyWrapper{inner: mustKey(t, 0x61)}
	cache := NewCache(0)
	defer cache.Close()

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	newMgr := func(scope string) *Manager {
		m, err := NewManager(res, wrapper, "alice")
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}
		m.Cache = cache
		m.CacheScope = scope
		m.Now = func() time.Time { return now }
		return m
	}

	m1 := newMgr("alice/2026-06-01")
	ct, err := m1.Seal(ctx, "msg", "1", []byte("secret"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Still cached after 9 minutes (10m default TTL).
	now = now.Add(9 * time.Minute)
	if _, err := newMgr("alice/2026-06-01").Open(ctx, "msg", "1", ct); err != nil {
		t.Fatalf("Open at 9m: %v", err)
	}
	if wrapper.unwrapCalls.Load() != 0 {
		t.Fatalf("unwrapCalls at 9m = %d, want 0", wrapper.unwrapCalls.Load())
	}

	// Evict("alice") evicts reset-scoped "alice/2026-06-01" immediately.
	cache.Evict("alice")
	if _, err := newMgr("alice/2026-06-01").Open(ctx, "msg", "1", ct); err != nil {
		t.Fatalf("Open after Evict(alice): %v", err)
	}
	if wrapper.unwrapCalls.Load() != 1 {
		t.Fatalf("unwrapCalls after Evict(alice) = %d, want 1", wrapper.unwrapCalls.Load())
	}

	// EvictAll drops everything.
	cache.EvictAll()
	if _, err := newMgr("alice/2026-06-01").Open(ctx, "msg", "1", ct); err != nil {
		t.Fatalf("Open after EvictAll: %v", err)
	}
	if wrapper.unwrapCalls.Load() != 2 {
		t.Fatalf("unwrapCalls after EvictAll = %d, want 2", wrapper.unwrapCalls.Load())
	}
}
