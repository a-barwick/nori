package settings

import "testing"

func TestParseUserProvisioningMode(t *testing.T) {
	cases := []struct {
		raw     string
		want    UserProvisioningMode
		wantErr bool
	}{
		{raw: "", want: UserProvisioningManual},
		{raw: "   ", want: UserProvisioningManual},
		{raw: "manual", want: UserProvisioningManual},
		{raw: "MANUAL", want: UserProvisioningManual},
		{raw: "invite", want: UserProvisioningInvite},
		{raw: " Invite ", want: UserProvisioningInvite},
		{raw: "open", want: UserProvisioningOpen},
		{raw: "OPEN", want: UserProvisioningOpen},
		{raw: "auto", wantErr: true},
		{raw: "disabled", wantErr: true},
	}

	for _, tc := range cases {
		got, err := ParseUserProvisioningMode(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseUserProvisioningMode(%q) = %q, want error", tc.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseUserProvisioningMode(%q) error = %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseUserProvisioningMode(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestLoadUserProvisioningModeFromEnv(t *testing.T) {
	t.Setenv("NORI_USER_PROVISIONING", "")
	mode, err := LoadUserProvisioningMode()
	if err != nil || mode != UserProvisioningManual {
		t.Fatalf("default LoadUserProvisioningMode() = (%q, %v), want (manual, nil)", mode, err)
	}

	t.Setenv("NORI_USER_PROVISIONING", "invite")
	mode, err = LoadUserProvisioningMode()
	if err != nil || mode != UserProvisioningInvite {
		t.Fatalf("invite LoadUserProvisioningMode() = (%q, %v), want (invite, nil)", mode, err)
	}

	t.Setenv("NORI_USER_PROVISIONING", "bogus")
	if _, err := LoadUserProvisioningMode(); err == nil {
		t.Fatal("expected error for invalid NORI_USER_PROVISIONING")
	}
	if got := UserProvisioningModeFromEnv(); got != UserProvisioningManual {
		t.Fatalf("UserProvisioningModeFromEnv() fallback = %q, want manual", got)
	}
}
