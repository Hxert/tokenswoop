package config

import (
	"testing"
	"time"
)

func TestFaucetOnlyUsesTestnetKeysAndNeverEnablesOnMainnet(t *testing.T) {
	p := Payments{Enabled: true, ChainID: 10143}
	t.Setenv("TESTNET_FAUCET_ENABLED", "true")
	t.Setenv("TESTNET_TURNSTILE_SITE_KEY", "")
	t.Setenv("TESTNET_TURNSTILE_SECRET_KEY", "")
	f, err := loadFaucet(p, "http://localhost:5173", "development")
	if err != nil || f.Enabled {
		t.Fatal("missing verification keys enabled faucet", f, err)
	}
	t.Setenv("TESTNET_TURNSTILE_SITE_KEY", "0x-real-public-fixture")
	t.Setenv("TESTNET_TURNSTILE_SECRET_KEY", "fixture-secret")
	f, err = loadFaucet(p, "http://localhost:5173", "development")
	if err != nil || !f.Enabled || f.Hostname != "localhost" || f.Cooldown != 24*time.Hour {
		t.Fatal(f, err)
	}
	p.ChainID = 143
	t.Setenv("MAINNET_FAUCET_ENABLED", "true")
	f, err = loadFaucet(p, "https://mainnet.example", "production")
	if err != nil || f.Enabled || f.SecretKey != "" {
		t.Fatal("mainnet inherited faucet", err)
	}
	p.ChainID = 10143
	t.Setenv("TESTNET_TURNSTILE_SITE_KEY", "1x00000000000000000000AA")
	if _, err = loadFaucet(p, "https://testnet.example", "production"); err == nil {
		t.Fatal("production accepted dummy Turnstile key")
	}
}
