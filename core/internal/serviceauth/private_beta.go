package serviceauth

import (
	"encoding/json"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"nori/internal/users"
)

// Cloud Run refreshes a mounted latest secret on read. Cache one snapshot per
// instance so chat and event requests don't each need a Secret Manager read.
type PrivateBeta struct {
	path    string
	mu      sync.Mutex
	expires time.Time
	emails  []string
}

func NewPrivateBeta(path string) *PrivateBeta {
	if path == "" {
		return nil
	}
	return &PrivateBeta{path: path}
}

func (b *PrivateBeta) Invited(identity users.Identity, email string) bool {
	if b == nil || identity.Issuer != "https://accounts.google.com" || !identity.EmailAuthoritative {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !time.Now().Before(b.expires) {
		data, err := os.ReadFile(b.path)
		var emails []string
		if err == nil {
			err = json.Unmarshal(data, &emails)
		}
		if err != nil {
			slog.Warn("Private beta roster unavailable")
			b.emails = nil
			b.expires = time.Now().Add(5 * time.Second)
			return false
		}
		b.emails = emails
		b.expires = time.Now().Add(30 * time.Second)
	}
	return slices.Contains(b.emails, users.Email(email))
}
