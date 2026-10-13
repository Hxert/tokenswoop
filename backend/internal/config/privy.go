package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type Privy struct {
	AppID, AppSecret  string
	GasSponsorship    bool
	DailyTransactions int
}

func (p Privy) Enabled() bool { return p.AppID != "" && p.AppSecret != "" }

func loadPrivy(chainID int64) (Privy, error) {
	p := Privy{AppID: strings.TrimSpace(sharedNetworkEnv(chainID, "PRIVY_APP_ID", "")), AppSecret: strings.TrimSpace(sharedNetworkEnv(chainID, "PRIVY_APP_SECRET", ""))}
	var err error
	p.GasSponsorship, err = strconv.ParseBool(sharedNetworkEnv(chainID, "PRIVY_GAS_SPONSORSHIP_ENABLED", "false"))
	if err != nil {
		return p, fmt.Errorf("invalid PRIVY_GAS_SPONSORSHIP_ENABLED")
	}
	p.DailyTransactions, err = strconv.Atoi(sharedNetworkEnv(chainID, "PRIVY_DAILY_SPONSORED_TRANSACTIONS", "30"))
	if err != nil || p.DailyTransactions < 1 || p.DailyTransactions > 1000 {
		return p, fmt.Errorf("PRIVY_DAILY_SPONSORED_TRANSACTIONS must be between 1 and 1000")
	}
	if p.AppID != "" && !regexp.MustCompile(`^[a-zA-Z0-9_-]{10,80}$`).MatchString(p.AppID) {
		return p, fmt.Errorf("invalid PRIVY_APP_ID")
	}
	if (p.AppID == "") != (p.AppSecret == "") {
		return p, fmt.Errorf("configure both PRIVY_APP_ID and PRIVY_APP_SECRET")
	}
	if p.GasSponsorship && (!p.Enabled() || (chainID != 10143 && chainID != 143)) {
		return p, fmt.Errorf("Privy gas sponsorship requires a configured app and Monad network")
	}
	return p, nil
}
