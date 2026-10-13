package privy_test

import (
	"strings"
	"testing"
	"tokenswoop/backend/internal/privy"
)

func TestSponsoredCalldataPinsBothSpenders(t *testing.T) {
	c := privy.Contracts{Token: "0x" + strings.Repeat("11", 20), Payment: "0x" + strings.Repeat("22", 20), Reserve: "0x" + strings.Repeat("33", 20)}
	for _, v := range []struct{ action, amount, to, data string }{
		{"payment_approve", "10", c.Token, "0x095ea7b3" + strings.Repeat("0", 24) + c.Payment[2:] + strings.Repeat("0", 63) + "a"},
		{"reserve_approve", "10", c.Token, "0x095ea7b3" + strings.Repeat("0", 24) + c.Reserve[2:] + strings.Repeat("0", 63) + "a"},
		{"reserve_deposit", "10", c.Reserve, "0xb6b55f25" + strings.Repeat("0", 63) + "a"},
		{"reserve_withdraw", "10", c.Reserve, "0x2e1a7d4d" + strings.Repeat("0", 63) + "a"},
	} {
		got, e := (privy.Operation{Action: v.action, Amount: v.amount}).Call(c)
		if e != nil || got.To != v.to || got.Data != v.data || got.Value != "0x0" {
			t.Fatal(v.action, got, e)
		}
	}
	for _, o := range []privy.Operation{{Action: "transfer", Amount: "1"}, {Action: "payment_approve", Amount: "100000001"}, {Action: "reserve_request", Amount: "1"}, {Action: "reserve_cancel", Amount: "0"}, {Action: "legacy_reserve_withdraw", Amount: "1"}, {Action: "reserve_deposit", Amount: "0"}} {
		if _, e := o.Call(c); e == nil {
			t.Fatal("unsupported operation accepted", o)
		}
	}
	for _, n := range []string{"-1", "01", "1.5", "1e2", "+1", strings.Repeat("9", 78), ""} {
		if _, e := (privy.Operation{Action: "reserve_approve", Amount: n}).Call(c); e == nil {
			t.Fatal("invalid amount accepted", n)
		}
	}
	if _, e := (privy.Operation{Action: "reserve_deposit", Amount: "1"}).Call(privy.Contracts{Token: c.Token, Payment: c.Payment}); e == nil {
		t.Fatal("disabled reserve sponsored")
	}
}
