package connections

import (
	"net/url"
	"strings"
)

const IOSOAuthCallbackURL = "nori://oauth/callback"

func IsIOSOAuthCallback(raw string) bool {
	return raw == IOSOAuthCallbackURL
}

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
