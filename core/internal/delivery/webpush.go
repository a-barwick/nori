// Package delivery holds the push and email transports behind the Scheduler's delivery channels.
//
// Receipts are honest about what the provider said: a push service's 201 or an SMTP relay's 250 means the message
// was accepted for delivery, never that the user saw it. Neither provider has been verified against a real account
// yet: Austin has no push or email credentials, so both are built from the specifications cited below.
package delivery

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nori/internal/scheduler"
)

// WebPush sends Web Push messages (RFC 8030) with VAPID authentication (RFC 8292) and aes128gcm message encryption
// (RFC 8291, RFC 8188). The browser subscribes with the VAPID public key and enrolls the subscription's endpoint and
// keys as the destination's target.
type WebPush struct {
	PublicKey  string // base64url uncompressed P-256 point, shared with the browser
	PrivateKey string // base64url 32-byte P-256 scalar
	Subject    string // mailto: or https: contact for the push service
	Client     *http.Client
}

// NewWebPush returns nil when VAPID keys are not configured, so the channel reports no_transport.
func NewWebPush(public, private, subject string) *WebPush {
	if public == "" || private == "" || subject == "" {
		return nil
	}
	return &WebPush{PublicKey: public, PrivateKey: private, Subject: subject, Client: &http.Client{Timeout: 20 * time.Second}}
}

func (p *WebPush) Kind() string { return "push" }

func (p *WebPush) Send(ctx context.Context, target scheduler.Target, msg scheduler.Message) (scheduler.Receipt, error) {
	if target.Endpoint == "" || target.P256dh == "" || target.Auth == "" {
		return scheduler.Receipt{}, fmt.Errorf("%w: the push subscription is incomplete", scheduler.ErrTargetInvalid)
	}
	endpoint, err := url.Parse(target.Endpoint)
	if err != nil || endpoint.Scheme != "https" {
		return scheduler.Receipt{}, fmt.Errorf("%w: the push endpoint must be https", scheduler.ErrTargetInvalid)
	}
	payload, _ := json.Marshal(map[string]string{"title": msg.Title, "body": msg.Body, "url": msg.URL, "tag": msg.Topic})
	body, err := encryptPayload(payload, target.P256dh, target.Auth)
	if err != nil {
		return scheduler.Receipt{}, fmt.Errorf("%w: %v", scheduler.ErrTargetInvalid, err)
	}
	token, err := p.vapidToken(endpoint.Scheme + "://" + endpoint.Host)
	if err != nil {
		return scheduler.Receipt{}, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, target.Endpoint, bytes.NewReader(body))
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", "86400")
	req.Header.Set("Urgency", "normal")
	if msg.Topic != "" {
		// A topic replaces an undelivered earlier message with the same topic, so a resend cannot stack duplicates.
		req.Header.Set("Topic", msg.Topic)
	}
	req.Header.Set("Authorization", "vapid t="+token+", k="+p.PublicKey)
	resp, err := p.Client.Do(req)
	if err != nil {
		// The request may have reached the push service.
		return scheduler.Receipt{}, fmt.Errorf("%w: %v", scheduler.ErrOutcomeUnknown, err)
	}
	defer resp.Body.Close()
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted:
		return scheduler.Receipt{ID: resp.Header.Get("Location"), State: "accepted"}, nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return scheduler.Receipt{}, fmt.Errorf("%w: the push service no longer accepts this subscription (%d)", scheduler.ErrTargetExpired, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return scheduler.Receipt{}, fmt.Errorf("%w: push service returned %d", scheduler.ErrRetryLater, resp.StatusCode)
	default:
		return scheduler.Receipt{}, fmt.Errorf("push service refused the message (%d): %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
}

// vapidToken signs the RFC 8292 JWT for one push service origin.
func (p *WebPush) vapidToken(audience string) (string, error) {
	d, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(p.PrivateKey, "="))
	if err != nil || len(d) != 32 {
		return "", errors.New("the VAPID private key must be a base64url 32-byte P-256 scalar")
	}
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), d)
	if err != nil {
		return "", errors.New("the VAPID private key is not a valid P-256 scalar")
	}
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": audience, "exp": time.Now().Add(12 * time.Hour).Unix(), "sub": p.Subject})
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig), nil
}

// encryptPayload applies RFC 8291 with a single aes128gcm record (RFC 8188).
func encryptPayload(plaintext []byte, p256dh, authSecret string) ([]byte, error) {
	decode := func(s string) ([]byte, error) {
		return base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.NewReplacer("+", "-", "/", "_").Replace(s), "="))
	}
	uaPublic, err := decode(p256dh)
	if err != nil {
		return nil, errors.New("invalid p256dh key")
	}
	auth, err := decode(authSecret)
	if err != nil || len(auth) != 16 {
		return nil, errors.New("invalid auth secret")
	}
	curve := ecdh.P256()
	uaKey, err := curve.NewPublicKey(uaPublic)
	if err != nil {
		return nil, errors.New("invalid p256dh key")
	}
	asKey, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := asKey.ECDH(uaKey)
	if err != nil {
		return nil, err
	}
	asPublic := asKey.PublicKey().Bytes()
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPublic...), asPublic...)
	ikm, err := hkdf.Key(sha256.New, shared, auth, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// One record: the plaintext followed by the 0x02 last-record delimiter.
	sealed := gcm.Seal(nil, nonce, append(append([]byte{}, plaintext...), 0x02), nil)
	header := make([]byte, 0, 16+4+1+len(asPublic))
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, 4096)
	header = append(header, byte(len(asPublic)))
	header = append(header, asPublic...)
	return append(header, sealed...), nil
}
