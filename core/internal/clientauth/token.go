// Package clientauth defines the credential format shared by Core and clients.
package clientauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"

	"nori/internal/users"
)

const Header = "X-Nori-Client-Token"

func NewSecret() string {
	var data [32]byte
	_, _ = rand.Read(data[:])
	return base64.RawURLEncoding.EncodeToString(data[:])
}

func ValidSecret(secret string) bool {
	_, err := base64.RawURLEncoding.Strict().DecodeString(secret)
	return len(secret) == 43 && err == nil
}

func ValidHash(hash string) bool {
	_, err := hex.DecodeString(hash)
	return len(hash) == 64 && err == nil && strings.ToLower(hash) == hash
}

func Hash(secret string) string { return digest("nori-client-v2:" + secret) }

func ProofHash(proof string) string { return digest("nori-device-proof:" + proof) }

func LegacyHash(token string) string { return digest("nori-mcp-client:" + token) }

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func Token(owner, id, secret string) string { return "nori2_" + owner + "_" + id + "." + secret }

type Credential struct {
	Owner string
	ID    string
	Hash  string
}

// Parse extracts a routing hint and the verifier. Core must check the verifier against storage.
func Parse(token string) (Credential, bool) {
	version, rest, _ := strings.Cut(token, "_")
	owner, payload, ok := strings.Cut(rest, "_")
	if !ok || !users.ValidID(owner) {
		return Credential{}, false
	}
	switch version {
	case "nori":
		if ValidSecret(payload) {
			return Credential{Owner: owner, Hash: LegacyHash(token)}, true
		}
	case "nori2":
		id, secret, found := strings.Cut(payload, ".")
		suffix, prefixed := strings.CutPrefix(id, "mcpc_")
		_, err := hex.DecodeString(suffix)
		if found && prefixed && (len(suffix) == 16 || len(suffix) == 32) && err == nil && ValidSecret(secret) {
			return Credential{Owner: owner, ID: id, Hash: Hash(secret)}, true
		}
	}
	return Credential{}, false
}
