package connections

import (
	"net/url"
	"strings"
)

// IOSOAuthCallbackURL is the single non-relative return_to target allowed for
// native iOS ASWebAuthenticationSession OAuth completion.
const IOSOAuthCallbackURL = "nori://oauth/callback"

// IsIOSOAuthCallback reports whether raw is the exact canonical iOS OAuth callback URL.
func IsIOSOAuthCallback(raw string) bool {
	if raw != IOSOAuthCallbackURL {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "nori" &&
		u.Opaque == "" &&
		u.User == nil &&
		u.Host == "oauth" &&
		u.Port() == "" &&
		u.Path == "/callback" &&
		u.RawPath == "" &&
		u.RawQuery == "" &&
		!u.ForceQuery &&
		u.Fragment == "" &&
		u.RawFragment == "" &&
		u.String() == IOSOAuthCallbackURL
}

// ValidReturnTo reports whether raw is a safe OAuth return target: either a
// relative path inside the web app or the exact iOS callback target
// nori://oauth/callback.
func ValidReturnTo(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, `\#?`) {
		return false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < 0x20 || raw[i] == 0x7f {
			return false
		}
	}
	if IsIOSOAuthCallback(raw) {
		return true
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return false
	}
	return strings.HasPrefix(u.Path, "/") && !strings.HasPrefix(u.Path, "//") && !strings.Contains(u.Path, "\\")
}

// SafeReturnTo returns a normalized valid return target, or "/apps" when raw is invalid.
func SafeReturnTo(raw string) string {
	if !ValidReturnTo(raw) {
		return "/apps"
	}
	if IsIOSOAuthCallback(raw) {
		return IOSOAuthCallbackURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" {
		return "/apps"
	}
	return u.Path
}

// BuildReturnRedirect appends query parameters cleanly to a validated return target.
func BuildReturnRedirect(returnTo string, params url.Values) string {
	target := SafeReturnTo(returnTo)
	if IsIOSOAuthCallback(target) {
		return (&url.URL{
			Scheme:   "nori",
			Host:     "oauth",
			Path:     "/callback",
			RawQuery: params.Encode(),
		}).String()
	}
	return (&url.URL{
		Path:     target,
		RawQuery: params.Encode(),
	}).String()
}
