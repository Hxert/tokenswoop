package config

import (
	"encoding/base64"
	"testing"
)

func TestProductionCannotEnableUnsafeDevDefaults(t *testing.T) {
	t.Setenv("APP_SECRET", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("APP_ENV", "production")
	t.Setenv("PUBLIC_ORIGIN", "https://tokenswoop.example")
	t.Setenv("SOURCE_URL", "https://source.example/tokenswoop")
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "true")
	t.Setenv("ADMIN_WALLETS", "")
	c, e := Load()
	if e != nil {
		t.Fatal(e)
	}
	if c.AllowPrivateUpstreams {
		t.Fatal("private upstreams enabled in production")
	}
	t.Setenv("SOURCE_URL", "")
	if _, e = Load(); e != nil {
		t.Fatal("optional source URL prevented production startup", e)
	}
	t.Setenv("SOURCE_URL", "https://source.example/tokenswoop")
	t.Setenv("PUBLIC_ORIGIN", "http://tokenswoop.example")
	if _, e = Load(); e == nil {
		t.Fatal("HTTP origin allowed in production")
	}
}

func TestPaymentConfigurationRejectsUnsupportedNetworksAndUnpinnedCode(t *testing.T) {
	t.Setenv("SETTLEMENT_ENABLED", "true")
	if _, e := loadPayments(1); e == nil {
		t.Fatal("unsupported network allowed")
	}
	t.Setenv("PAYMENT_RPC_URL", "https://testnet.example")
	for _, name := range []string{"PAYMENT_CONTRACT", "PAYMENT_TOKEN", "PAYMENT_PROVIDER"} {
		t.Setenv(name, "0x1111111111111111111111111111111111111111")
	}
	t.Setenv("PAYMENT_CONTRACT_CODE_HASH", "")
	t.Setenv("PAYMENT_TOKEN_CODE_HASH", "")
	if _, e := loadPayments(10143); e == nil {
		t.Fatal("unverified runtime allowed")
	}
	t.Setenv("SETTLEMENT_ENABLED", "false")
	if p, e := loadPayments(143); e != nil || p.Enabled {
		t.Fatal("disabled mode changed", e)
	}
}

func TestReserveRequiresSettlementAndPinnedOperator(t *testing.T) {
	t.Setenv("RESERVE_ENABLED", "true")
	t.Setenv("RESERVE_MODE", "")
	t.Setenv("RESERVE_MANAGER", "0x3333333333333333333333333333333333333333")
	if _, e := loadReserve(Payments{}); e == nil {
		t.Fatal("reserve enabled without settlement")
	}
	t.Setenv("RESERVE_CONTRACT", "0x1111111111111111111111111111111111111111")
	t.Setenv("RESERVE_OPERATOR", "0x2222222222222222222222222222222222222222")
	t.Setenv("RESERVE_CODE_HASH", "")
	if _, e := loadReserve(Payments{Enabled: true}); e == nil {
		t.Fatal("unverified reserve accepted")
	}
	t.Setenv("RESERVE_CODE_HASH", "0x1111111111111111111111111111111111111111111111111111111111111111")
	t.Setenv("RESERVE_KEYSTORE", "relative-key")
	if _, e := loadReserve(Payments{Enabled: true}); e == nil {
		t.Fatal("relative signer path accepted")
	}
	t.Setenv("RESERVE_KEYSTORE", "")
	t.Setenv("RESERVE_PASSWORD_FILE", "")
	if r, e := loadReserve(Payments{Enabled: true}); e != nil || !r.DebtMode() {
		t.Fatal("API must not require signer credentials", e)
	}
	t.Setenv("RESERVE_MODE", "approval")
	if _, e := loadReserve(Payments{Enabled: true}); e == nil {
		t.Fatal("retired approval reserve accepted")
	}
}
