// Package tenantkey manages per-user 256-bit data encryption keys (DEKs)
// stored as wrapped rows in a user's ResourceStore. Shredding a user's DEK via
// Shred writes a permanent tombstone and evicts cached key material, rendering
// all ciphertext encrypted under that key unrecoverable (crypto-shredding) and
// failing closed on subsequent Seal/Open calls.
package tenantkey

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"nori/internal/secret/rowstore"
	"nori/internal/state"
)

const (
	// Collection is the ResourceStore collection holding per-user wrapped DEKs.
	Collection = "tenant_keys"
	// PrimaryKeyID is the resource ID of the user's active DEK row.
	PrimaryKeyID = "primary"
	// DefaultCacheTTL is the default TTL for process-wide shared DEK caching.
	DefaultCacheTTL = 5 * time.Second

	dekBytes = 32
)

// ErrKeyNotFound is returned by Open when the tenant's DEK does not exist.
var ErrKeyNotFound = errors.New("tenant data key not found")

// ErrKeyShredded is returned by Seal and Open when the tenant's DEK has been
// permanently shredded via Shred. It wraps ErrKeyNotFound so callers checking
// either sentinel match.
var ErrKeyShredded = fmt.Errorf("tenant data key shredded: %w", ErrKeyNotFound)

type wrappedDEKRow struct {
	WrappedKey []byte `json:"wrapped_key,omitempty"`
	KeyID      string `json:"key_id,omitempty"`
	Owner      string `json:"owner"`
	Version    int    `json:"version,omitempty"`
	Shredded   bool   `json:"shredded,omitempty"`
}

type cacheEntry struct {
	dek       []byte
	aead      cipher.AEAD
	expiresAt time.Time
}

// Cache caches unwrapped per-owner AEAD ciphers across Manager instances for a
// short TTL so repeated OpenStore calls avoid redundant KMS round-trips.
type Cache struct {
	ttl time.Duration

	mu      sync.RWMutex
	entries map[string]cacheEntry
}

// NewCache creates a process-wide DEK cache with the given TTL (defaulting to
// DefaultCacheTTL when ttl <= 0).
func NewCache(ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &Cache{
		ttl:     ttl,
		entries: make(map[string]cacheEntry),
	}
}

func (c *Cache) get(owner string, now time.Time) (cipher.AEAD, bool) {
	if c == nil || owner == "" {
		return nil, false
	}
	c.mu.RLock()
	entry, ok := c.entries[owner]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if !now.Before(entry.expiresAt) {
		c.evictIfExpired(owner, now)
		return nil, false
	}
	return entry.aead, true
}

func (c *Cache) evictIfExpired(owner string, now time.Time) {
	c.mu.Lock()
	if entry, ok := c.entries[owner]; ok && !now.Before(entry.expiresAt) {
		clear(entry.dek)
		delete(c.entries, owner)
	}
	c.mu.Unlock()
}

func (c *Cache) put(owner string, dek []byte, aead cipher.AEAD, now time.Time) {
	if c == nil || owner == "" || aead == nil {
		return
	}
	copied := append([]byte(nil), dek...)
	c.mu.Lock()
	if prev, ok := c.entries[owner]; ok {
		clear(prev.dek)
	}
	c.entries[owner] = cacheEntry{
		dek:       copied,
		aead:      aead,
		expiresAt: now.Add(c.ttl),
	}
	c.mu.Unlock()
}

// Evict immediately zeroes and removes the cached DEK for owner.
func (c *Cache) Evict(owner string) {
	if c == nil || owner == "" {
		return
	}
	c.mu.Lock()
	if entry, ok := c.entries[owner]; ok {
		clear(entry.dek)
		delete(c.entries, owner)
	}
	c.mu.Unlock()
}

// Close zeroes and removes all cached DEKs.
func (c *Cache) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	for owner, entry := range c.entries {
		clear(entry.dek)
		delete(c.entries, owner)
	}
	c.mu.Unlock()
}

// Manager seals and opens tenant-scoped payloads using a per-user 256-bit DEK
// wrapped by a master KeyWrapper.
type Manager struct {
	Resources state.ResourceStore
	Keys      rowstore.KeyWrapper
	Owner     string
	Now       func() time.Time
	// Cache, when non-nil, shares unwrapped DEKs across Manager instances for
	// the same owner with a short TTL and immediate eviction on Shred.
	Cache *Cache
	// CacheScope separates cached keys across a data reset while keeping the
	// account ID used to authenticate encrypted payloads stable.
	CacheScope string

	mu   sync.RWMutex
	dek  []byte
	aead cipher.AEAD
}

// NewManager constructs a validated tenantkey Manager.
func NewManager(resources state.ResourceStore, keys rowstore.KeyWrapper, owner string) (*Manager, error) {
	if resources == nil {
		return nil, errors.New("tenantkey manager requires a resource store")
	}
	if keys == nil {
		return nil, errors.New("tenantkey manager requires a key wrapper")
	}
	if owner == "" {
		return nil, errors.New("tenantkey manager requires an owner")
	}
	return &Manager{
		Resources: resources,
		Keys:      keys,
		Owner:     owner,
	}, nil
}

func (m *Manager) validateConfig() error {
	if m == nil {
		return errors.New("tenantkey manager is nil")
	}
	if m.Resources == nil {
		return errors.New("tenantkey manager has no resource store")
	}
	if m.Keys == nil {
		return errors.New("tenantkey manager has no key wrapper")
	}
	if m.Owner == "" {
		return errors.New("tenantkey manager has no owner")
	}
	return nil
}

func (m *Manager) now() time.Time {
	if m.Now == nil {
		return time.Now().UTC()
	}
	return m.Now().UTC()
}

func wrapAAD(owner string) []byte {
	return []byte(fmt.Sprintf("nori-tenant-dek:v1:%d:%s", len(owner), owner))
}

func dataAAD(owner, scope, recordID string) []byte {
	return []byte(fmt.Sprintf(
		"nori-data:v1:%d:%s\x00%d:%s\x00%d:%s",
		len(owner), owner,
		len(scope), scope,
		len(recordID), recordID,
	))
}

func (m *Manager) wrapDEK(ctx context.Context, dek []byte) ([]byte, string, error) {
	if aw, ok := m.Keys.(rowstore.AADKeyWrapper); ok {
		return aw.WrapWithAAD(ctx, dek, wrapAAD(m.Owner))
	}
	return m.Keys.Wrap(ctx, dek)
}

func (m *Manager) unwrapDEK(ctx context.Context, wrapped []byte, keyID string) ([]byte, error) {
	if aw, ok := m.Keys.(rowstore.AADKeyWrapper); ok {
		return aw.UnwrapWithAAD(ctx, wrapped, keyID, wrapAAD(m.Owner))
	}
	return m.Keys.Unwrap(ctx, wrapped, keyID)
}

// Seal encrypts plaintext with the tenant's 256-bit DEK using AES-256-GCM,
// prepending a fresh 12-byte nonce and authenticating owner, scope, and recordID.
// If the tenant does not yet have a DEK, one is generated and persisted safely.
// If the tenant's DEK has been shredded, Seal fails closed with ErrKeyShredded.
func (m *Manager) Seal(ctx context.Context, scope, recordID string, plaintext []byte) ([]byte, error) {
	if err := m.validateConfig(); err != nil {
		return nil, err
	}
	if scope == "" || recordID == "" {
		return nil, errors.New("scope and recordID must not be empty")
	}
	gcm, err := m.ensureAEAD(ctx)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	aad := dataAAD(m.Owner, scope, recordID)
	out := make([]byte, len(nonce), len(nonce)+len(plaintext)+gcm.Overhead())
	copy(out, nonce)
	return gcm.Seal(out, nonce, plaintext, aad), nil
}

// Open decrypts ciphertext produced by Seal, verifying the prepended nonce and
// length-prefixed AAD bound to owner, scope, and recordID.
func (m *Manager) Open(ctx context.Context, scope, recordID string, ciphertext []byte) ([]byte, error) {
	if err := m.validateConfig(); err != nil {
		return nil, err
	}
	if scope == "" || recordID == "" {
		return nil, errors.New("scope and recordID must not be empty")
	}
	gcm, err := m.loadAEAD(ctx)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext is too short")
	}
	aad := dataAAD(m.Owner, scope, recordID)
	plaintext, err := gcm.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], aad)
	if err != nil {
		return nil, errors.New("ciphertext could not be decrypted")
	}
	return plaintext, nil
}

// Shred overwrites the tenant's DEK resource row with a permanent tombstone
// ({Shredded: true, Owner: m.Owner}) and evicts all cached key material,
// rendering all ciphertext encrypted under it permanently unrecoverable and
// causing future Seal and Open calls to fail closed with ErrKeyShredded.
func (m *Manager) Shred(ctx context.Context) error {
	if err := m.validateConfig(); err != nil {
		return err
	}
	_, err := m.Resources.Mutate(ctx, state.Mutation{
		RequestID:         uuid.NewString(),
		Method:            "DELETE",
		Target:            "tenant_key:" + PrimaryKeyID,
		Payload:           PrimaryKeyID,
		Collection:        Collection,
		ID:                PrimaryKeyID,
		SkipRevisionCheck: true,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			nextRev := int64(1)
			if current != nil {
				nextRev = current.Revision + 1
			}
			tombstone, err := state.NewResource(Collection, PrimaryKeyID, nextRev, wrappedDEKRow{
				Owner:    m.Owner,
				Shredded: true,
			}, nil, m.now())
			if err != nil {
				return state.MutationResult{}, err
			}
			return state.MutationResult{
				Writes:  []state.Resource{tombstone},
				Receipt: state.MutationReceipt{Status: "applied"},
			}, nil
		},
	})
	m.evictCache()
	return err
}

func (m *Manager) evictCache() {
	if m.Cache != nil {
		m.Cache.Evict(m.cacheScope())
	}
	m.mu.Lock()
	clear(m.dek)
	m.dek = nil
	m.aead = nil
	m.mu.Unlock()
}

func (m *Manager) cacheScope() string {
	if m.CacheScope != "" {
		return m.CacheScope
	}
	return m.Owner
}

func (m *Manager) cachedAEAD() (cipher.AEAD, bool) {
	if m.Cache != nil {
		return m.Cache.get(m.cacheScope(), m.now())
	}
	m.mu.RLock()
	gcm := m.aead
	m.mu.RUnlock()
	return gcm, gcm != nil
}

func (m *Manager) loadAEAD(ctx context.Context) (cipher.AEAD, error) {
	if gcm, ok := m.cachedAEAD(); ok {
		return gcm, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Cache != nil {
		if gcm, ok := m.Cache.get(m.cacheScope(), m.now()); ok {
			return gcm, nil
		}
	} else if m.aead != nil {
		return m.aead, nil
	}
	return m.readAndCacheLocked(ctx)
}

func (m *Manager) ensureAEAD(ctx context.Context) (cipher.AEAD, error) {
	if gcm, ok := m.cachedAEAD(); ok {
		return gcm, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Cache != nil {
		if gcm, ok := m.Cache.get(m.cacheScope(), m.now()); ok {
			return gcm, nil
		}
	} else if m.aead != nil {
		return m.aead, nil
	}

	gcm, err := m.readAndCacheLocked(ctx)
	if err == nil {
		return gcm, nil
	}
	if errors.Is(err, ErrKeyShredded) {
		return nil, err
	}
	if !errors.Is(err, ErrKeyNotFound) {
		return nil, err
	}

	dek := make([]byte, dekBytes)
	if _, err := rand.Read(dek); err != nil {
		return nil, err
	}
	wrapped, keyID, err := m.wrapDEK(ctx, dek)
	if err != nil {
		clear(dek)
		return nil, fmt.Errorf("wrap tenant data key: %w", err)
	}
	r, err := state.NewResource(Collection, PrimaryKeyID, 1, wrappedDEKRow{
		WrappedKey: wrapped,
		KeyID:      keyID,
		Owner:      m.Owner,
		Version:    1,
	}, nil, m.now())
	if err != nil {
		clear(dek)
		return nil, err
	}
	if _, err := m.Resources.PutResource(ctx, r, 0); err != nil {
		clear(dek)
		if errors.Is(err, state.ErrRevisionConflict) {
			return m.readAndCacheLocked(ctx)
		}
		return nil, err
	}
	return m.cacheLocked(dek)
}

func (m *Manager) readAndCacheLocked(ctx context.Context) (cipher.AEAD, error) {
	r, err := m.Resources.Resource(ctx, Collection, PrimaryKeyID)
	if errors.Is(err, state.ErrNotFound) {
		return nil, ErrKeyNotFound
	}
	if err != nil {
		return nil, err
	}
	var stored wrappedDEKRow
	if err := r.Decode(&stored); err != nil {
		return nil, fmt.Errorf("decode tenant key row: %w", err)
	}
	if stored.Shredded {
		return nil, ErrKeyShredded
	}
	if stored.Owner != m.Owner {
		return nil, errors.New("tenant key owner mismatch")
	}
	dek, err := m.unwrapDEK(ctx, stored.WrappedKey, stored.KeyID)
	if err != nil {
		return nil, fmt.Errorf("unwrap tenant data key: %w", err)
	}
	if len(dek) != dekBytes {
		clear(dek)
		return nil, fmt.Errorf("unwrapped tenant data key has length %d, want %d", len(dek), dekBytes)
	}
	return m.cacheLocked(dek)
}

func (m *Manager) cacheLocked(dek []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(dek)
	if err != nil {
		clear(dek)
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		clear(dek)
		return nil, err
	}
	if m.Cache != nil {
		m.Cache.put(m.cacheScope(), dek, gcm, m.now())
		clear(dek)
		return gcm, nil
	}
	clear(m.dek)
	m.dek = dek
	m.aead = gcm
	return gcm, nil
}
