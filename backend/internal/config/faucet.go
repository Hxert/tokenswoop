package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Faucet struct {
	Enabled                      bool
	SiteKey, SecretKey, Hostname string
	Cooldown                     time.Duration
	IPDailyLimit, DailyLimit     int
}

func loadFaucet(p Payments, origin, environment string) (Faucet, error) {
	var f Faucet
	// A network selector change can never enable distribution of mainnet USDC.
	if p.NetworkID() != 10143 || !p.Enabled {
		return f, nil
	}
	enabled, err := strconv.ParseBool(networkEnv(10143, "FAUCET_ENABLED", "true"))
	if err != nil {
		return f, fmt.Errorf("invalid TESTNET_FAUCET_ENABLED")
	}
	if !enabled {
		return f, nil
	}
	f.SiteKey = networkEnv(10143, "TURNSTILE_SITE_KEY", "")
	f.SecretKey = networkEnv(10143, "TURNSTILE_SECRET_KEY", "")
	if f.SiteKey == "" || f.SecretKey == "" {
		return Faucet{}, nil
	}
	if environment == "production" && (strings.HasPrefix(f.SiteKey, "1x") || strings.HasPrefix(f.SiteKey, "2x") || strings.HasPrefix(f.SiteKey, "3x") || strings.HasPrefix(f.SecretKey, "1x") || strings.HasPrefix(f.SecretKey, "2x") || strings.HasPrefix(f.SecretKey, "3x")) {
		return f, fmt.Errorf("production faucet requires real Turnstile keys")
	}
	hours, err := strconv.Atoi(networkEnv(10143, "FAUCET_COOLDOWN_HOURS", "24"))
	if err != nil || hours < 1 || hours > 8760 {
		return f, fmt.Errorf("TESTNET_FAUCET_COOLDOWN_HOURS must be between 1 and 8760")
	}
	f.Cooldown = time.Duration(hours) * time.Hour
	f.IPDailyLimit, err = strconv.Atoi(networkEnv(10143, "FAUCET_IP_DAILY_LIMIT", "10"))
	if err != nil || f.IPDailyLimit < 1 || f.IPDailyLimit > 10000 {
		return f, fmt.Errorf("invalid TESTNET_FAUCET_IP_DAILY_LIMIT")
	}
	f.DailyLimit, err = strconv.Atoi(networkEnv(10143, "FAUCET_DAILY_LIMIT", "100"))
	if err != nil || f.DailyLimit < 1 || f.DailyLimit > 100000 {
		return f, fmt.Errorf("invalid TESTNET_FAUCET_DAILY_LIMIT")
	}
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		return f, fmt.Errorf("faucet requires PUBLIC_ORIGIN")
	}
	f.Hostname = strings.ToLower(u.Hostname())
	f.Enabled = true
	return f, nil
}
