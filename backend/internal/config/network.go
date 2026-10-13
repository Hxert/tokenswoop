package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Circle native USDC and Aave V3 Monad. Never substitute a test token on mainnet.
const MonadUSDC = "0x754704bc059f8c67012fed69bc8a327a5aafb603"
const MonadAavePool = "0x69a5f9ad4f96ebf0a0c792dd42a01cc5c0102fef"
const MonadAaveUSDC = "0x35a73bacb179d3740395a3cecc87ff2e581d6042"

func networkPrefix(chainID int64) string {
	if chainID == 143 {
		return "MAINNET_"
	}
	return "TESTNET_"
}

// An explicitly empty prefixed value clears a setting. Legacy unprefixed chain
// settings belong to testnet only: a selector change cannot reuse its signer,
// database or contract on mainnet. This function never mutates the environment.
func networkEnv(chainID int64, key, fallback string) string {
	if value, ok := os.LookupEnv(networkPrefix(chainID) + key); ok {
		return strings.TrimSpace(value)
	}
	if chainID != 143 {
		return env(key, fallback)
	}
	return fallback
}

// Login application settings may deliberately be shared by both deployments.
func sharedNetworkEnv(chainID int64, key, fallback string) string {
	if value, ok := os.LookupEnv(networkPrefix(chainID) + key); ok {
		return strings.TrimSpace(value)
	}
	return env(key, fallback)
}

type Network struct {
	Name     string `json:"name"`
	ChainID  int64  `json:"chain_id"`
	Testnet  bool   `json:"testnet"`
	Explorer string `json:"explorer"`
	Symbol   string `json:"symbol"`
}

func NetworkFor(id int64) Network {
	if id == 143 {
		return Network{"Monad", 143, false, "https://monadvision.com", "USDC"}
	}
	return Network{"Monad Testnet", 10143, true, "https://testnet.monadexplorer.com", "kUSDC"}
}

func loadChainID() (int64, error) {
	selection := os.Getenv("CHAIN_NETWORK")
	id := int64(10143)
	switch selection {
	case "", "testnet":
	case "mainnet":
		id = 143
	default:
		return 0, fmt.Errorf("CHAIN_NETWORK must be testnet or mainnet")
	}
	if raw := os.Getenv("CHAIN_ID"); raw != "" {
		legacy, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || (legacy != 10143 && legacy != 143) {
			return 0, fmt.Errorf("CHAIN_ID must be 10143 or 143")
		}
		if selection != "" && id != legacy {
			return 0, fmt.Errorf("CHAIN_ID conflicts with CHAIN_NETWORK")
		}
		id = legacy
	}
	return id, nil
}

// Zero-valued fixture configs retain the existing testnet default. Load always
// supplies an explicit, validated network for running services.
func (p Payments) NetworkID() int64 {
	if p.ChainID == 0 {
		return 10143
	}
	return p.ChainID
}
func (r Reserve) NetworkID() int64 {
	if r.ChainID == 0 {
		return 10143
	}
	return r.ChainID
}
func (r Reserve) DebtMode() bool { return r.Mode == "debt" }
