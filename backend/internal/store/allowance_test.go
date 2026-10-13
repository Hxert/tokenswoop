package store_test

import (
	"context"
	"testing"
	"tokenswoop/backend/internal/testutil"
)

func TestAllowanceAdmissionIgnoresWalletBalanceAndDetectsRevocation(t *testing.T) {
	s := testutil.Database(t)
	u := testutil.Account(t, s, "10")
	s.WalletPolicy.CheckAllowance = true
	ctx := context.Background()
	for _, c := range []struct {
		balance, allowance, debt string
		blocked                  bool
	}{{"0", "10000000", "0", false}, {"10000000", "0", "0", true}, {"10000000", "100000", "0.2", true}, {"0", "300000", "0.2", false}, {"0", "200000", "0.2", true}} {
		if _, err := s.DB.Exec(ctx, `UPDATE payment_accounts SET balance=$2,allowance=$3,checked_at=now() WHERE user_id=$1`, u, c.balance, c.allowance); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.Exec(ctx, `UPDATE users SET unpaid_balance=$2 WHERE id=$1`, u, c.debt); err != nil {
			t.Fatal(err)
		}
		got, err := s.PaidCallsBlocked(ctx, u)
		if err != nil || got != c.blocked {
			t.Fatal(c, got, err)
		}
	}
}

func TestDeploymentDatabaseCannotSwitchNetworks(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	if err := s.BindNetwork(ctx, 10143); err != nil {
		t.Fatal(err)
	}
	if err := s.BindNetwork(ctx, 10143); err != nil {
		t.Fatal(err)
	}
	if err := s.BindNetwork(ctx, 143); err == nil {
		t.Fatal("testnet database reused for mainnet")
	}
}
