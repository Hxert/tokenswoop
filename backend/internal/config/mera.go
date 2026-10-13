package config

import (
	"fmt"
	"math/big"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type Mera struct {
	Enabled                                                   bool
	SponsorMode                                               string
	SharedSigner                                              bool
	Implementation, CodeHash, Sponsor, Keystore, PasswordFile string
	DailyTransactions                                         int
	MaxGasWei, DailyGasWei                                    string
}

func loadMera(p Payments, reserve Reserve) (Mera, error) {
	var m Mera
	value := func(key, fallback string) string { return networkEnv(p.NetworkID(), "MERA_"+key, fallback) }
	var err error
	m.Enabled, err = strconv.ParseBool(value("GAS_SPONSORSHIP_ENABLED", "false"))
	if err != nil {
		return m, fmt.Errorf("invalid MERA_GAS_SPONSORSHIP_ENABLED")
	}
	if !m.Enabled {
		return m, nil
	}
	if !p.Enabled || !reserve.Enabled {
		return m, fmt.Errorf("Mera sponsorship requires payments and reserve")
	}
	m.Implementation = strings.ToLower(value("IMPLEMENTATION", ""))
	m.CodeHash = strings.ToLower(value("CODE_HASH", ""))
	m.SponsorMode = value("SPONSOR_MODE", "dedicated")
	switch m.SponsorMode {
	case "platform":
		m.Sponsor, m.Keystore, m.PasswordFile = strings.ToLower(p.Provider), p.Keystore, p.PasswordFile
	case "dedicated":
		m.Sponsor = strings.ToLower(value("SPONSOR_ADDRESS", ""))
		m.Keystore, m.PasswordFile = value("KEYSTORE", ""), value("PASSWORD_FILE", "")
	default:
		return m, fmt.Errorf("MERA_SPONSOR_MODE must be platform or dedicated")
	}
	m.SharedSigner = strings.EqualFold(m.Sponsor, p.Provider) || strings.EqualFold(m.Sponsor, reserve.Operator)
	address := regexp.MustCompile(`^0x[0-9a-f]{40}$`)
	for _, a := range []string{m.Implementation, m.Sponsor} {
		if !address.MatchString(a) || a == "0x"+strings.Repeat("0", 40) {
			return m, fmt.Errorf("invalid Mera address")
		}
	}
	if strings.EqualFold(m.Sponsor, p.StopAuthority) || m.Sponsor == m.Implementation || strings.EqualFold(m.Sponsor, p.RecipientAddress()) && !m.SharedSigner {
		return m, fmt.Errorf("Mera sponsor cannot use the stop authority, implementation or a receive-only wallet")
	}
	if !regexp.MustCompile(`^0x[0-9a-f]{64}$`).MatchString(m.CodeHash) {
		return m, fmt.Errorf("pin MERA_CODE_HASH")
	}
	for _, path := range []string{m.Keystore, m.PasswordFile} {
		if path != "" && !filepath.IsAbs(path) {
			return m, fmt.Errorf("Mera signer paths must be absolute")
		}
	}
	m.DailyTransactions, err = strconv.Atoi(value("DAILY_TRANSACTIONS", "30"))
	if err != nil || m.DailyTransactions < 1 || m.DailyTransactions > 1000 {
		return m, fmt.Errorf("invalid MERA_DAILY_TRANSACTIONS")
	}
	m.MaxGasWei, m.DailyGasWei = value("MAX_GAS_WEI", "50000000000000000"), value("DAILY_GAS_WEI", "1000000000000000000")
	for _, v := range []string{m.MaxGasWei, m.DailyGasWei} {
		n, ok := new(big.Int).SetString(v, 10)
		if !ok || n.Sign() <= 0 || n.BitLen() > 128 {
			return m, fmt.Errorf("invalid Mera gas budget")
		}
	}
	return m, nil
}
