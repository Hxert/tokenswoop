package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

type Reserve struct {
	ChainID                                              int64
	Mode, Manager                                        string
	Custody                                              string
	Pool, AToken, Treasury                               string
	Enabled                                              bool
	Contract, CodeHash, Operator, Keystore, PasswordFile string
}

func loadReserve(p Payments) (Reserve, error) {
	r := Reserve{Enabled: networkEnv(p.NetworkID(), "RESERVE_ENABLED", "") == "true", ChainID: p.NetworkID(), Mode: networkEnv(p.NetworkID(), "RESERVE_MODE", "debt")}
	defaultCustody := "aave"
	if p.NetworkID() == 143 {
		defaultCustody = "vault"
	}
	r.Custody = networkEnv(p.NetworkID(), "RESERVE_CUSTODY", defaultCustody)
	if r.Custody != "vault" && r.Custody != "aave" {
		return r, fmt.Errorf("invalid RESERVE_CUSTODY")
	}
	if p.NetworkID() == 143 && r.Custody != "vault" {
		return r, fmt.Errorf("mainnet reserve must hold USDC directly in the vault")
	}
	if r.Mode != "debt" {
		return r, fmt.Errorf("RESERVE_MODE must be debt; approval contracts are no longer supported")
	}
	if !r.Enabled {
		return r, nil
	}
	if !p.Enabled {
		return r, fmt.Errorf("reserve requires configured settlement")
	}
	r.Contract = strings.ToLower(networkEnv(p.NetworkID(), "RESERVE_CONTRACT", ""))
	r.CodeHash = strings.ToLower(networkEnv(p.NetworkID(), "RESERVE_CODE_HASH", ""))
	r.Operator = strings.ToLower(networkEnv(p.NetworkID(), "RESERVE_OPERATOR", ""))
	address := regexp.MustCompile(`^0x[0-9a-f]{40}$`)
	r.Manager = strings.ToLower(networkEnv(p.NetworkID(), "RESERVE_MANAGER", ""))
	r.Pool = strings.ToLower(networkEnv(p.NetworkID(), "RESERVE_POOL", ""))
	r.AToken = strings.ToLower(networkEnv(p.NetworkID(), "RESERVE_ATOKEN", ""))
	r.Treasury = strings.ToLower(networkEnv(p.NetworkID(), "RESERVE_TREASURY", ""))
	if r.Custody == "vault" && (r.Pool != "" || r.AToken != "" || r.Treasury != "") {
		return r, fmt.Errorf("vault custody does not use a pool, aToken or treasury")
	}
	for _, v := range []string{r.Pool, r.AToken, r.Treasury} {
		if v != "" && (!address.MatchString(v) || v == "0x"+strings.Repeat("0", 40)) {
			return r, fmt.Errorf("invalid reserve pool, aToken or treasury")
		}
	}
	if r.DebtMode() && (!address.MatchString(r.Manager) || r.Manager == r.Operator || r.Manager == "0x"+strings.Repeat("0", 40)) {
		return r, fmt.Errorf("RESERVE_MANAGER must be a separate nonzero address")
	}
	for _, v := range []string{r.Contract, r.Operator} {
		if !address.MatchString(v) || v == "0x"+strings.Repeat("0", 40) {
			return r, fmt.Errorf("invalid reserve address")
		}
	}
	if !regexp.MustCompile(`^0x[0-9a-f]{64}$`).MatchString(r.CodeHash) {
		return r, fmt.Errorf("pin reserve runtime code hash")
	}
	r.Keystore = networkEnv(p.NetworkID(), "RESERVE_KEYSTORE", "")
	r.PasswordFile = networkEnv(p.NetworkID(), "RESERVE_PASSWORD_FILE", "")
	for _, v := range []string{r.Keystore, r.PasswordFile} {
		if v != "" && !filepath.IsAbs(v) {
			return r, fmt.Errorf("reserve signer paths must be absolute")
		}
	}
	return r, nil
}
