package config

import "testing"

func TestPrivyConfigurationFailClosed(t *testing.T) {
	for _, v := range []struct {
		name, id, secret, gas, limit string
		chain                        int64
		valid                        bool
	}{
		{"disabled", "", "", "false", "30", 10143, true},
		{"embedded", "validprivyappid", "secret", "false", "30", 10143, true},
		{"sponsored", "validprivyappid", "secret", "true", "30", 10143, true},
		{"missing secret", "validprivyappid", "", "false", "30", 10143, false},
		{"unconfigured gas", "", "", "true", "30", 10143, false},
		{"mainnet gas", "validprivyappid", "secret", "true", "30", 143, true},
		{"unsupported network", "validprivyappid", "secret", "true", "30", 1, false},
		{"bad limit", "validprivyappid", "secret", "true", "0", 10143, false},
		{"bad app", "https://attacker", "secret", "true", "30", 10143, false},
	} {
		t.Run(v.name, func(t *testing.T) {
			t.Setenv("PRIVY_APP_ID", v.id)
			t.Setenv("PRIVY_APP_SECRET", v.secret)
			t.Setenv("PRIVY_GAS_SPONSORSHIP_ENABLED", v.gas)
			t.Setenv("PRIVY_DAILY_SPONSORED_TRANSACTIONS", v.limit)
			_, err := loadPrivy(v.chain)
			if (err == nil) != v.valid {
				t.Fatal("unexpected config acceptance", err)
			}
		})
	}
}
