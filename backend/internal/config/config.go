package config

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	Address, DatabaseURL, RedisURL, Origin, Environment string
	SourceURL                                           string
	TrustedProxies                                      []string
	Secret                                              []byte
	ChainID                                             int64
	AdminWallets                                        map[string]bool
	AllowPrivateUpstreams                               bool
	SessionTTL                                          time.Duration
	Payments                                            Payments
	Reserve                                             Reserve
	Privy                                               Privy
	Mera                                                Mera
	Faucet                                              Faucet
}

func Load() (Config, error) {
	c := Config{Address: env("LISTEN_ADDR", "127.0.0.1:8080"), Origin: env("PUBLIC_ORIGIN", "http://localhost:5173"), Environment: env("APP_ENV", "development"), AdminWallets: map[string]bool{}, SessionTTL: 24 * time.Hour}
	var err error
	c.ChainID, err = loadChainID()
	if err != nil {
		return c, err
	}
	c.DatabaseURL = networkEnv(c.ChainID, "DATABASE_URL", "")
	redisDefault := "redis://127.0.0.1:6379/0"
	if c.ChainID == 143 {
		redisDefault = ""
	}
	c.RedisURL = networkEnv(c.ChainID, "REDIS_URL", redisDefault)
	c.Secret, err = base64.StdEncoding.DecodeString(os.Getenv("APP_SECRET"))
	if err != nil || len(c.Secret) != 32 {
		return c, fmt.Errorf("APP_SECRET must be a base64 encoded random 32-byte key")
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("%sDATABASE_URL is required", networkPrefix(c.ChainID))
	}
	if c.RedisURL == "" {
		return c, fmt.Errorf("%sREDIS_URL is required", networkPrefix(c.ChainID))
	}
	origin, err := url.Parse(c.Origin)
	if err != nil || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.User != nil || (origin.Scheme != "http" && origin.Scheme != "https") {
		return c, fmt.Errorf("PUBLIC_ORIGIN must be a bare http(s) origin")
	}
	if c.Environment != "development" && c.Environment != "production" && c.Environment != "test" {
		return c, fmt.Errorf("invalid APP_ENV")
	}
	if c.Environment == "production" && origin.Scheme != "https" {
		return c, fmt.Errorf("production requires HTTPS")
	}
	c.SourceURL = os.Getenv("SOURCE_URL")
	if c.SourceURL != "" {
		source, e := url.Parse(c.SourceURL)
		if e != nil || source.Scheme != "https" || source.Host == "" || source.User != nil {
			return c, fmt.Errorf("SOURCE_URL must be a public HTTPS source offer")
		}
	}
	for _, proxy := range strings.Split(os.Getenv("TRUSTED_PROXY_CIDRS"), ",") {
		if proxy = strings.TrimSpace(proxy); proxy != "" {
			c.TrustedProxies = append(c.TrustedProxies, proxy)
		}
	}
	c.AllowPrivateUpstreams = c.Environment != "production" && os.Getenv("ALLOW_PRIVATE_UPSTREAMS") == "true"
	for _, w := range strings.Split(os.Getenv("ADMIN_WALLETS"), ",") {
		w = strings.ToLower(strings.TrimSpace(w))
		if w != "" {
			if len(w) != 42 || !strings.HasPrefix(w, "0x") {
				return c, fmt.Errorf("invalid ADMIN_WALLETS address")
			}
			if _, e := hex.DecodeString(w[2:]); e != nil {
				return c, fmt.Errorf("invalid ADMIN_WALLETS address")
			}
			c.AdminWallets[w] = true
		}
	}
	c.Payments, err = loadPayments(c.ChainID)
	if err == nil {
		c.Reserve, err = loadReserve(c.Payments)
	}
	if err == nil {
		c.Privy, err = loadPrivy(c.ChainID)
	}
	if err == nil {
		c.Mera, err = loadMera(c.Payments, c.Reserve)
	}
	if err == nil {
		c.Faucet, err = loadFaucet(c.Payments, c.Origin, c.Environment)
	}
	return c, err
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
