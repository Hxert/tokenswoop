package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestNetworkSelection(t *testing.T) {
	for _, c := range []struct {
		name, id string
		want     int64
		valid    bool
	}{{"testnet", "", 10143, true}, {"mainnet", "", 143, true}, {"", "10143", 10143, true}, {"mainnet", "10143", 0, false}, {"typo", "", 0, false}, {"", "1", 0, false}} {
		t.Run(c.name+c.id, func(t *testing.T) {
			t.Setenv("CHAIN_NETWORK", c.name)
			t.Setenv("CHAIN_ID", c.id)
			got, err := loadChainID()
			if (err == nil) != c.valid || c.valid && got != c.want {
				t.Fatal(got, err)
			}
		})
	}
}

func profileFixture(t *testing.T) {
	t.Helper()
	t.Setenv("APP_SECRET", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("APP_ENV", "test")
	t.Setenv("PUBLIC_ORIGIN", "http://localhost:5173")
	t.Setenv("ADMIN_WALLETS", "")
	t.Setenv("CHAIN_ID", "")
	for _, prefix := range []string{"TESTNET_", "MAINNET_"} {
		t.Setenv(prefix+"DATABASE_URL", "postgres://localhost/"+strings.ToLower(prefix))
		t.Setenv(prefix+"REDIS_URL", map[string]string{"TESTNET_": "redis://localhost:6379/0", "MAINNET_": "redis://localhost:6379/1"}[prefix])
		t.Setenv(prefix+"SETTLEMENT_ENABLED", "true")
		t.Setenv(prefix+"PAYMENT_RPC_URL", "https://"+strings.ToLower(prefix[:len(prefix)-1])+".example")
		t.Setenv(prefix+"PAYMENT_RPC_RPS", "40")
		t.Setenv(prefix+"PAYMENT_CONTRACT", "0x1111111111111111111111111111111111111111")
		t.Setenv(prefix+"PAYMENT_RECIPIENT", "0x7777777777777777777777777777777777777777")
		t.Setenv(prefix+"PAYMENT_PROVIDER", "0x2222222222222222222222222222222222222222")
		t.Setenv(prefix+"PAYMENT_STOP_AUTHORITY", "0x3333333333333333333333333333333333333333")
		t.Setenv(prefix+"PAYMENT_CONTRACT_CODE_HASH", "0x"+strings.Repeat("1", 64))
		t.Setenv(prefix+"PAYMENT_TOKEN_CODE_HASH", "0x"+strings.Repeat("2", 64))
		t.Setenv(prefix+"PAYMENT_CAST_PATH", "/fixture/cast")
		t.Setenv(prefix+"PAYMENT_KEYSTORE", "/fixture/"+prefix+"provider")
		t.Setenv(prefix+"PAYMENT_PASSWORD_FILE", "/fixture/"+prefix+"password")
		t.Setenv(prefix+"RESERVE_ENABLED", "true")
		t.Setenv(prefix+"RESERVE_MODE", "debt")
		t.Setenv(prefix+"RESERVE_MANAGER", "0x3333333333333333333333333333333333333333")
		t.Setenv(prefix+"RESERVE_CONTRACT", "0x4444444444444444444444444444444444444444")
		t.Setenv(prefix+"RESERVE_OPERATOR", "0x2222222222222222222222222222222222222222")
		t.Setenv(prefix+"RESERVE_CODE_HASH", "0x"+strings.Repeat("3", 64))
		t.Setenv(prefix+"RESERVE_POOL", MonadAavePool)
		t.Setenv(prefix+"RESERVE_ATOKEN", MonadAaveUSDC)
		t.Setenv(prefix+"RESERVE_TREASURY", "0x5555555555555555555555555555555555555555")
		t.Setenv(prefix+"RESERVE_KEYSTORE", "/fixture/"+prefix+"operator")
		t.Setenv(prefix+"RESERVE_PASSWORD_FILE", "/fixture/"+prefix+"password")
		t.Setenv(prefix+"PRIVY_APP_ID", "")
		t.Setenv(prefix+"PRIVY_APP_SECRET", "")
		t.Setenv(prefix+"PRIVY_GAS_SPONSORSHIP_ENABLED", "false")
	}
	t.Setenv("TESTNET_PAYMENT_TOKEN", "0x6666666666666666666666666666666666666666")
	t.Setenv("MAINNET_PAYMENT_TOKEN", MonadUSDC)
	t.Setenv("MAINNET_RESERVE_CUSTODY", "vault")
	for _, key := range []string{"RESERVE_POOL", "RESERVE_ATOKEN", "RESERVE_TREASURY"} {
		t.Setenv("MAINNET_"+key, "")
	}
}

func TestOneSelectorSwitchesEntireProfileAndSwitchesBack(t *testing.T) {
	profileFixture(t)
	for _, name := range []string{"testnet", "mainnet", "testnet"} {
		t.Setenv("CHAIN_NETWORK", name)
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		id := int64(10143)
		if name == "mainnet" {
			id = 143
		}
		if c.ChainID != id || c.Payments.NetworkID() != id || c.Reserve.NetworkID() != id || !strings.Contains(c.DatabaseURL, name) || c.Payments.RPC != "https://"+name+".example" {
			t.Fatal("network profile was mixed")
		}
		if !strings.Contains(c.Payments.Keystore, strings.ToUpper(name)) || !strings.Contains(c.Reserve.Keystore, strings.ToUpper(name)) {
			t.Fatal("signer did not follow selector")
		}
		if c.Payments.RPCRequestsPerSecond != 40 || !c.Reserve.DebtMode() {
			t.Fatal("network settings lost")
		}
	}
}

func TestMainnetNeverFallsBackToLegacyTestnetConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://testnet/ledger")
	t.Setenv("PAYMENT_KEYSTORE", "/testnet/key")
	t.Setenv("SETTLEMENT_ENABLED", "true")
	for _, key := range []string{"DATABASE_URL", "PAYMENT_KEYSTORE", "SETTLEMENT_ENABLED"} {
		if networkEnv(143, key, "") != "" {
			t.Fatalf("mainnet inherited %s", key)
		}
	}
	t.Setenv("TESTNET_PAYMENT_KEYSTORE", "")
	if networkEnv(10143, "PAYMENT_KEYSTORE", "") != "" {
		t.Fatal("explicit blank should clear legacy signer")
	}
}

func TestMainnetRejectsWrongCurrencyAndIncompleteReserve(t *testing.T) {
	profileFixture(t)
	t.Setenv("CHAIN_NETWORK", "mainnet")
	for _, field := range []string{"DATABASE_URL", "REDIS_URL", "PAYMENT_TOKEN", "PAYMENT_CONTRACT", "PAYMENT_STOP_AUTHORITY", "PAYMENT_TOKEN_CODE_HASH", "PAYMENT_RECIPIENT", "RESERVE_MANAGER"} {
		t.Run(field, func(t *testing.T) {
			t.Setenv("MAINNET_"+field, "")
			if _, err := Load(); err == nil {
				t.Fatal("incomplete mainnet configuration accepted")
			}
		})
	}
	t.Setenv("MAINNET_PAYMENT_TOKEN", "0x6666666666666666666666666666666666666666")
	if _, err := Load(); err == nil {
		t.Fatal("test currency accepted on mainnet")
	}
}

func TestVaultCustodyRejectsStaleAaveConfig(t *testing.T) {
	profileFixture(t)
	t.Setenv("CHAIN_NETWORK", "mainnet")
	for _, key := range []string{"RESERVE_POOL", "RESERVE_ATOKEN", "RESERVE_TREASURY"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("MAINNET_"+key, MonadAavePool)
			if _, err := Load(); err == nil {
				t.Fatal("vault accepted lending configuration")
			}
		})
	}
	t.Setenv("MAINNET_RESERVE_CUSTODY", "aave")
	if _, err := Load(); err == nil {
		t.Fatal("mainnet accepted lending custody")
	}
}
