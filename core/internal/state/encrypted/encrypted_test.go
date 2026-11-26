package encrypted

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/secret/rowstore"
	"nori/internal/secret/tenantkey"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

type memArtifactStore struct {
	mu     sync.Mutex
	pngs   map[string][]byte
	files  map[string][]byte
	closed bool
}

func newMemArtifactStore() *memArtifactStore {
	return &memArtifactStore{
		pngs:  make(map[string][]byte),
		files: make(map[string][]byte),
	}
}

func (m *memArtifactStore) PutPNG(_ context.Context, run, id string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := run + "/" + id
	if _, exists := m.pngs[k]; exists {
		return state.ErrFileExists
	}
	m.pngs[k] = append([]byte(nil), data...)
	return nil
}

func (m *memArtifactStore) GetPNG(_ context.Context, run, id string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.pngs[run+"/"+id]
	if !ok {
		return nil, state.ErrNotFound
	}
	return append([]byte(nil), b...), nil
}

func (m *memArtifactStore) PutFile(_ context.Context, key, _ string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.files[key]; exists {
		return state.ErrFileExists
	}
	m.files[key] = append([]byte(nil), data...)
	return nil
}

func (m *memArtifactStore) GetFile(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[key]
	if !ok {
		return nil, state.ErrNotFound
	}
	return append([]byte(nil), b...), nil
}

func (m *memArtifactStore) DeleteFile(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, key)
	return nil
}

func (m *memArtifactStore) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func newTestManager(t *testing.T, owner string, keyFill byte) (*tenantkey.Manager, state.ResourceStore, *rowstore.MasterKey) {
	t.Helper()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	backend := memstore.New(memstore.WithClock(func() time.Time { return now }))
	t.Cleanup(func() { _ = backend.Close() })
	u := users.User{
		ID:          uuid.NewSHA1(uuid.NameSpaceOID, []byte("encrypted-test:"+owner)).String(),
		Email:       owner + "@example.com",
		DisplayName: owner,
		TimeZone:    "UTC",
	}
	db := records.NewShared(backend, u, 5.0)
	db.Now = func() time.Time { return now }
	k, err := rowstore.ParseMasterKey(hex.EncodeToString(bytes.Repeat([]byte{keyFill}, 32)))
	if err != nil {
		t.Fatalf("ParseMasterKey: %v", err)
	}
	mgr, err := tenantkey.NewManager(db, k, owner)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr, db, k
}

func TestArtifactStoreRoundTripTamperLegacyAndShred(t *testing.T) {
	ctx := context.Background()
	inner := newMemArtifactStore()
	aliceMgr, _, key := newTestManager(t, "alice", 0x51)
	store, err := NewArtifactStore(inner, aliceMgr)
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}

	runID := uuid.NewString()
	artID := uuid.NewString()
	pngData := []byte("\x89PNG\r\n\x1a\nfake-screenshot-bytes")
	if err := store.PutPNG(ctx, runID, artID, pngData); err != nil {
		t.Fatalf("PutPNG: %v", err)
	}

	// Stored bytes are framed with NORIENC1 and do not contain plaintext.
	rawPNG, err := inner.GetPNG(ctx, runID, artID)
	if err != nil {
		t.Fatalf("inner.GetPNG: %v", err)
	}
	if !bytes.HasPrefix(rawPNG, []byte(ArtifactMagic)) {
		t.Fatalf("stored PNG missing %q magic header", ArtifactMagic)
	}
	if bytes.Contains(rawPNG, []byte("fake-screenshot-bytes")) {
		t.Fatalf("stored PNG leaked plaintext")
	}

	// Round-trip PNG read.
	gotPNG, err := store.GetPNG(ctx, runID, artID)
	if err != nil || !bytes.Equal(gotPNG, pngData) {
		t.Fatalf("GetPNG = (%q, %v), want %q", gotPNG, err, pngData)
	}

	// File upload/result round-trip.
	fileKey := "uploads/" + uuid.NewString()
	fileData := []byte("confidential quarterly report")
	if err := store.PutFile(ctx, fileKey, "text/plain", fileData); err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	rawFile, err := inner.GetFile(ctx, fileKey)
	if err != nil {
		t.Fatalf("inner.GetFile: %v", err)
	}
	if !bytes.HasPrefix(rawFile, []byte(ArtifactMagic)) || bytes.Contains(rawFile, fileData) {
		t.Fatalf("stored file not properly encrypted")
	}
	gotFile, err := store.GetFile(ctx, fileKey)
	if err != nil || !bytes.Equal(gotFile, fileData) {
		t.Fatalf("GetFile = (%q, %v), want %q", gotFile, err, fileData)
	}

	// Legacy plaintext passthrough for both PNGs and files.
	legacyArtID := uuid.NewString()
	legacyPNG := []byte("\x89PNG\r\n\x1a\nlegacy-plaintext-png")
	if err := inner.PutPNG(ctx, runID, legacyArtID, legacyPNG); err != nil {
		t.Fatalf("inner.PutPNG legacy: %v", err)
	}
	gotLegacyPNG, err := store.GetPNG(ctx, runID, legacyArtID)
	if err != nil || !bytes.Equal(gotLegacyPNG, legacyPNG) {
		t.Fatalf("GetPNG legacy = (%q, %v)", gotLegacyPNG, err)
	}

	legacyFileKey := "results/" + uuid.NewString()
	legacyFile := []byte("legacy plaintext result")
	if err := inner.PutFile(ctx, legacyFileKey, "text/plain", legacyFile); err != nil {
		t.Fatalf("inner.PutFile legacy: %v", err)
	}
	gotLegacyFile, err := store.GetFile(ctx, legacyFileKey)
	if err != nil || !bytes.Equal(gotLegacyFile, legacyFile) {
		t.Fatalf("GetFile legacy = (%q, %v)", gotLegacyFile, err)
	}

	// Tamper detection.
	tampered := append([]byte(nil), rawFile...)
	tampered[len(tampered)-1] ^= 0xff
	tamperedKey := "uploads/" + uuid.NewString()
	if err := inner.PutFile(ctx, tamperedKey, "text/plain", tampered); err != nil {
		t.Fatalf("inner.PutFile tampered: %v", err)
	}
	if _, err := store.GetFile(ctx, tamperedKey); err == nil {
		t.Fatalf("GetFile succeeded on tampered blob")
	}

	// Cross-ID transplant fails AAD.
	otherArtID := uuid.NewString()
	if err := inner.PutPNG(ctx, runID, otherArtID, rawPNG); err != nil {
		t.Fatalf("inner.PutPNG transplant: %v", err)
	}
	if _, err := store.GetPNG(ctx, runID, otherArtID); err == nil {
		t.Fatalf("GetPNG succeeded on transplanted blob with different ID")
	}

	// Wrong-owner AAD rejection (different tenant manager over same inner store).
	bobMgr, _, _ := newTestManager(t, "bob", 0x51)
	_ = key
	bobStore, err := NewArtifactStore(inner, bobMgr)
	if err != nil {
		t.Fatalf("NewArtifactStore bob: %v", err)
	}
	// Ensure bob has a DEK so decryption reaches AES-GCM check as well.
	if err := bobStore.PutFile(ctx, "uploads/"+uuid.NewString(), "text/plain", []byte("bob")); err != nil {
		t.Fatalf("bob PutFile: %v", err)
	}
	if _, err := bobStore.GetFile(ctx, fileKey); err == nil {
		t.Fatalf("bobStore decrypted alice's file")
	}

	// DeleteFile and Close delegate to inner.
	delKey := "uploads/" + uuid.NewString()
	if err := store.PutFile(ctx, delKey, "text/plain", []byte("bye")); err != nil {
		t.Fatalf("PutFile del: %v", err)
	}
	if err := store.DeleteFile(ctx, delKey); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if _, err := store.GetFile(ctx, delKey); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("GetFile after delete = %v, want ErrNotFound", err)
	}

	// Crypto-shredding: Shred causes GetPNG and GetFile on encrypted blobs to fail.
	if err := aliceMgr.Shred(ctx); err != nil {
		t.Fatalf("Shred: %v", err)
	}
	if _, err := store.GetPNG(ctx, runID, artID); !errors.Is(err, tenantkey.ErrKeyNotFound) {
		t.Fatalf("GetPNG after Shred = %v, want ErrKeyNotFound", err)
	}
	if _, err := store.GetFile(ctx, fileKey); !errors.Is(err, tenantkey.ErrKeyNotFound) {
		t.Fatalf("GetFile after Shred = %v, want ErrKeyNotFound", err)
	}
	// Legacy plaintext remains readable even after shred.
	if got, err := store.GetFile(ctx, legacyFileKey); err != nil || !bytes.Equal(got, legacyFile) {
		t.Fatalf("GetFile legacy after Shred = (%q, %v)", got, err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !inner.closed {
		t.Fatalf("inner store was not closed")
	}
}

func TestPayloadCodecRoundTripTamperAADLegacyAndShred(t *testing.T) {
	ctx := context.Background()
	aliceMgr, _, _ := newTestManager(t, "alice", 0x61)
	codec, err := NewPayloadCodec(aliceMgr)
	if err != nil {
		t.Fatalf("NewPayloadCodec: %v", err)
	}

	secret := "Buy milk & call Dr. Smith about 🍣"
	enc, err := codec.EncryptField(ctx, "message.text", "msg-42", secret)
	if err != nil {
		t.Fatalf("EncryptField: %v", err)
	}
	if !IsEncrypted(enc) || !strings.HasPrefix(enc, PayloadPrefix) {
		t.Fatalf("encrypted field %q missing prefix %q", enc, PayloadPrefix)
	}
	if strings.Contains(enc, "Buy milk") {
		t.Fatalf("encrypted field leaked plaintext")
	}

	dec, err := codec.DecryptField(ctx, "message.text", "msg-42", enc)
	if err != nil || dec != secret {
		t.Fatalf("DecryptField = (%q, %v), want %q", dec, err, secret)
	}

	// Empty string round-trips cleanly.
	encEmpty, err := codec.EncryptField(ctx, "message.text", "msg-empty", "")
	if err != nil {
		t.Fatalf("EncryptField empty: %v", err)
	}
	decEmpty, err := codec.DecryptField(ctx, "message.text", "msg-empty", encEmpty)
	if err != nil || decEmpty != "" {
		t.Fatalf("DecryptField empty = (%q, %v)", decEmpty, err)
	}

	// Legacy plaintext passthrough.
	for _, legacy := range []string{"plain message", "", "nori:other:prefix"} {
		if IsEncrypted(legacy) {
			t.Errorf("IsEncrypted(%q) = true, want false", legacy)
		}
		got, err := codec.DecryptField(ctx, "message.text", "msg-legacy", legacy)
		if err != nil || got != legacy {
			t.Fatalf("DecryptField legacy %q = (%q, %v)", legacy, got, err)
		}
	}

	// Tamper detection (invalid base64 and flipped byte).
	if _, err := codec.DecryptField(ctx, "message.text", "msg-42", PayloadPrefix+"!!!not-base64!!!"); err == nil {
		t.Fatalf("DecryptField accepted invalid base64")
	}
	corrupted := enc[:len(enc)-2] + "AA"
	if corrupted == enc {
		corrupted = enc[:len(enc)-2] + "BB"
	}
	if _, err := codec.DecryptField(ctx, "message.text", "msg-42", corrupted); err == nil {
		t.Fatalf("DecryptField accepted corrupted ciphertext")
	}

	// Wrong scope / wrong ID / wrong owner AAD rejection.
	if _, err := codec.DecryptField(ctx, "memory.body", "msg-42", enc); err == nil {
		t.Fatalf("DecryptField succeeded with wrong scope")
	}
	if _, err := codec.DecryptField(ctx, "message.text", "msg-99", enc); err == nil {
		t.Fatalf("DecryptField succeeded with wrong ID")
	}

	bobMgr, _, _ := newTestManager(t, "bob", 0x61)
	bobCodec, err := NewPayloadCodec(bobMgr)
	if err != nil {
		t.Fatalf("NewPayloadCodec bob: %v", err)
	}
	if _, err := bobCodec.EncryptField(ctx, "init", "1", "bob"); err != nil {
		t.Fatalf("bob EncryptField: %v", err)
	}
	if _, err := bobCodec.DecryptField(ctx, "message.text", "msg-42", enc); err == nil {
		t.Fatalf("bobCodec decrypted alice's payload")
	}

	// Crypto-shredding causes DecryptField to fail.
	if err := aliceMgr.Shred(ctx); err != nil {
		t.Fatalf("Shred: %v", err)
	}
	if _, err := codec.DecryptField(ctx, "message.text", "msg-42", enc); !errors.Is(err, tenantkey.ErrKeyNotFound) {
		t.Fatalf("DecryptField after Shred = %v, want ErrKeyNotFound", err)
	}
}
