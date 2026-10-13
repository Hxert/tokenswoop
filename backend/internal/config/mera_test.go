package config

import (
	"strings"
	"testing"
)

func TestMeraNetworkIsolationAndSponsorPolicy(t *testing.T) {
	t.Setenv("TESTNET_MERA_GAS_SPONSORSHIP_ENABLED", "true")
	t.Setenv("TESTNET_MERA_IMPLEMENTATION", "0x"+strings.Repeat("1", 40))
	t.Setenv("TESTNET_MERA_CODE_HASH", "0x"+strings.Repeat("1", 64))
	t.Setenv("TESTNET_MERA_SPONSOR_ADDRESS", "0x"+strings.Repeat("2", 40))
	p := Payments{Enabled: true, ChainID: 10143, Provider: "0x" + strings.Repeat("3", 40)}
	m, e := loadMera(p, Reserve{Enabled: true})
	if e != nil || !m.Enabled || m.DailyTransactions != 30 {
		t.Fatal(m, e)
	}
	p.ChainID = 143
	m, e = loadMera(p, Reserve{Enabled: true})
	if e != nil || m.Enabled {
		t.Fatal("testnet signer leaked to mainnet", e)
	}
	p.ChainID = 10143
	t.Setenv("TESTNET_MERA_SPONSOR_ADDRESS", p.Provider)
	if m, e = loadMera(p, Reserve{Enabled: true}); e != nil || !m.SharedSigner {
		t.Fatal("shared signer must coordinate with settlement", e)
	}
	p.Keystore, p.PasswordFile = "/platform/key", "/platform/password"
	t.Setenv("TESTNET_MERA_SPONSOR_MODE", "platform")
	t.Setenv("TESTNET_MERA_KEYSTORE", "/retired/mera-key")
	if m, e = loadMera(p, Reserve{Enabled: true}); e != nil || m.Sponsor != p.Provider || m.Keystore != p.Keystore || m.PasswordFile != p.PasswordFile || !m.SharedSigner {
		t.Fatal("platform mode must reuse the selected network provider", m, e)
	}
	p.ChainID = 143
	if m, e = loadMera(p, Reserve{Enabled: true}); e != nil || m.Enabled {
		t.Fatal("testnet platform mode leaked into mainnet", e)
	}
	p.ChainID = 10143
	t.Setenv("TESTNET_MERA_SPONSOR_MODE", "invalid")
	if _, e = loadMera(p, Reserve{Enabled: true}); e == nil {
		t.Fatal("invalid sponsor mode accepted")
	}
	t.Setenv("TESTNET_MERA_SPONSOR_MODE", "dedicated")
	t.Setenv("TESTNET_MERA_SPONSOR_ADDRESS", "0x"+strings.Repeat("2", 40))
	t.Setenv("TESTNET_MERA_MAX_GAS_WEI", "0")
	if _, e = loadMera(p, Reserve{Enabled: true}); e == nil {
		t.Fatal("unbounded gas accepted")
	}
}
