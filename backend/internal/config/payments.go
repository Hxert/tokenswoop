package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type Payments struct {
	ChainID                                                         int64
	StopAuthority                                                   string
	Recipient                                                       string
	Enabled                                                         bool
	RPC, Contract, Token, Provider, ContractCodeHash, TokenCodeHash string
	Cast, Keystore, PasswordFile                                    string
	RPCRequestsPerSecond                                            int
}

func loadPayments(chainID int64) (Payments, error) {
	p := Payments{Enabled: networkEnv(chainID, "SETTLEMENT_ENABLED", "") == "true", ChainID: chainID}
	if !p.Enabled {
		return p, nil
	}
	if chainID != 10143 && chainID != 143 {
		return p, fmt.Errorf("wallet settlement requires a configured Monad network")
	}
	p.RPC = networkEnv(chainID, "PAYMENT_RPC_URL", "")
	rpcRPS, e := strconv.Atoi(networkEnv(chainID, "PAYMENT_RPC_RPS", "5"))
	p.RPCRequestsPerSecond = rpcRPS
	if e != nil || p.RPCRequestsPerSecond < 1 || p.RPCRequestsPerSecond > 1000 {
		return p, fmt.Errorf("PAYMENT_RPC_RPS must be between 1 and 1000")
	}
	u, e := url.Parse(p.RPC)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return p, fmt.Errorf("PAYMENT_RPC_URL must be HTTPS")
	}
	p.Contract = strings.ToLower(networkEnv(chainID, "PAYMENT_CONTRACT", ""))
	p.Token = strings.ToLower(networkEnv(chainID, "PAYMENT_TOKEN", ""))
	if chainID == 143 && p.Token != MonadUSDC {
		return p, fmt.Errorf("MAINNET_PAYMENT_TOKEN must be Circle native USDC on Monad")
	}
	p.Provider = strings.ToLower(networkEnv(chainID, "PAYMENT_PROVIDER", ""))
	p.Recipient = strings.ToLower(networkEnv(chainID, "PAYMENT_RECIPIENT", ""))
	if chainID == 143 && p.Recipient == "" {
		return p, fmt.Errorf("MAINNET_PAYMENT_RECIPIENT is required")
	}
	address := regexp.MustCompile(`^0x[0-9a-f]{40}$`)
	p.StopAuthority = strings.ToLower(networkEnv(chainID, "PAYMENT_STOP_AUTHORITY", ""))
	if chainID == 143 && p.StopAuthority == "" {
		return p, fmt.Errorf("MAINNET_PAYMENT_STOP_AUTHORITY is required")
	}
	if p.StopAuthority != "" && (!address.MatchString(p.StopAuthority) || p.StopAuthority == p.Provider || p.StopAuthority == "0x"+strings.Repeat("0", 40)) {
		return p, fmt.Errorf("PAYMENT_STOP_AUTHORITY must be a separate nonzero address")
	}
	for _, v := range []string{p.Contract, p.Token, p.Provider} {
		if !address.MatchString(v) || v == "0x"+strings.Repeat("0", 40) {
			return p, fmt.Errorf("invalid payment address")
		}
	}
	if p.Recipient != "" && (!address.MatchString(p.Recipient) || p.Recipient == "0x"+strings.Repeat("0", 40) || p.Recipient == p.Contract) {
		return p, fmt.Errorf("invalid payment recipient")
	}
	p.ContractCodeHash = strings.ToLower(networkEnv(chainID, "PAYMENT_CONTRACT_CODE_HASH", ""))
	p.TokenCodeHash = strings.ToLower(networkEnv(chainID, "PAYMENT_TOKEN_CODE_HASH", ""))
	hash := regexp.MustCompile(`^0x[0-9a-f]{64}$`)
	if !hash.MatchString(p.ContractCodeHash) || !hash.MatchString(p.TokenCodeHash) {
		return p, fmt.Errorf("pin both payment runtime code hashes")
	}
	p.Cast = networkEnv(chainID, "PAYMENT_CAST_PATH", "")
	p.Keystore = networkEnv(chainID, "PAYMENT_KEYSTORE", "")
	p.PasswordFile = networkEnv(chainID, "PAYMENT_PASSWORD_FILE", "")
	// API processes may omit signer paths entirely. Worker validates these at startup.
	for _, v := range []string{p.Cast, p.Keystore, p.PasswordFile} {
		if v != "" && !filepath.IsAbs(v) {
			return p, fmt.Errorf("payment signer paths must be absolute")
		}
	}
	return p, nil
}

// RecipientAddress preserves already-deployed testnet contracts, whose provider
// is also their recipient. New contracts must configure and verify recipient().
func (p Payments) RecipientAddress() string {
	if p.Recipient != "" {
		return p.Recipient
	}
	return p.Provider
}
