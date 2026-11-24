package agent

import (
	"context"
	"net/http"
	"time"

	"nori/internal/extensions"
	"nori/internal/state"
	"nori/internal/transcript"
	"nori/internal/users"
)

type Service struct {
	UserFacts string
	// Profile, when set, supplies the user facts at the time of each request instead of UserFacts.
	Profile      users.Profile
	Store        state.Store
	Model        Model
	Capabilities Capabilities
	HTTP         *http.Client
	// Extensions supplies the user's current tools beyond the engine's own. Nil offers built-ins only.
	Extensions extensions.Source
	// Hidden reports results the user deleted from the Library; result tools treat them as absent.
	Hidden func(ctx context.Context, resultID string) bool
	// Now returns the current time for prompts, saved results, and step metrics. Nil uses time.Now.
	Now func() time.Time
}

func New(store state.Store, model Model) *Service {
	return &Service{
		Store: store, Model: model,
		HTTP: &http.Client{
			Timeout:       2 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func params(names ...string) *transcript.Schema {
	properties := map[string]*transcript.Schema{}
	for _, name := range names {
		properties[name] = &transcript.Schema{Type: transcript.TypeString}
	}
	return &transcript.Schema{Type: transcript.TypeObject, Properties: properties, Required: names}
}
