package settings

import (
	"strings"
	"testing"
)

func TestGoogleWorkspaceEnabledFor(t *testing.T) {
	base := GoogleWorkspace{ClientID: "id", ClientSecret: "secret", RedirectURL: "https://nori.test/api/v1/connections/oauth/callback"}

	if !base.EnabledFor("anyone@example.test") {
		t.Fatal("an empty tester list should offer Google to every user")
	}

	limited := base
	limited.TestUsers = []string{"tester@example.test"}
	if !limited.EnabledFor(" Tester@Example.test ") {
		t.Fatal("a listed tester should see Google regardless of case or spacing")
	}
	if limited.EnabledFor("anyone@example.test") {
		t.Fatal("a tester list should hide Google from unlisted users")
	}

	if (GoogleWorkspace{TestUsers: []string{"tester@example.test"}}).EnabledFor("tester@example.test") {
		t.Fatal("Google should stay off without OAuth client settings")
	}
}

func TestGoogleWorkspaceValidate(t *testing.T) {
	callback := "https://nori.test/api/v1/connections/oauth/callback"
	cases := []struct {
		name    string
		g       GoogleWorkspace
		wantErr string
	}{
		{name: "unset", g: GoogleWorkspace{}},
		{name: "open to everyone", g: GoogleWorkspace{ClientID: "id", ClientSecret: "secret", RedirectURL: callback}},
		{name: "tester list", g: GoogleWorkspace{ClientID: "id", ClientSecret: "secret", RedirectURL: callback, TestUsers: []string{"a@example.test"}}},
		{name: "testers without client", g: GoogleWorkspace{TestUsers: []string{"a@example.test"}}, wantErr: "NORI_GOOGLE_WORKSPACE_CLIENT_ID"},
		{name: "missing secret", g: GoogleWorkspace{ClientID: "id", RedirectURL: callback}, wantErr: "NORI_GOOGLE_WORKSPACE_CLIENT_SECRET"},
		{name: "wrong callback path", g: GoogleWorkspace{ClientID: "id", ClientSecret: "secret", RedirectURL: "https://nori.test/callback"}, wantErr: "NORI_GOOGLE_WORKSPACE_REDIRECT_URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.g.validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validate() = %v, want error mentioning %s", err, tc.wantErr)
			}
		})
	}
}
