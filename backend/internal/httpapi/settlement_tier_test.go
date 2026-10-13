package httpapi

import (
	"context"
	"testing"

	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
)

func TestTierSettlementSettingsValidationAndExposure(t *testing.T) {
	a := testApp(t)
	client, user := operatorClient(t, a)
	a.Payments = payments.New(a.Store, config.Payments{Enabled: true}, walletEvidence{}, a.Config.Secret)
	body := map[string]any{"id": "free", "min_principal": "0", "discount_bps": 0, "rpm": 30, "concurrency": 2, "settlement_threshold": "0.025"}
	saved := object(t, client.call("PUT", "/api/admin/tiers", body, 200))
	if saved["settlement_threshold"] != "0.025000" {
		t.Fatalf("threshold did not round-trip: %v", saved["settlement_threshold"])
	}
	for _, amount := range []string{"0", "-1", "0.0000001", "100.01", "invalid"} {
		body["settlement_threshold"] = amount
		client.call("PUT", "/api/admin/tiers", body, 400)
	}
	// Editing other fields through an older client preserves the configured value.
	delete(body, "settlement_threshold")
	body["rpm"] = 31
	client.call("PUT", "/api/admin/tiers", body, 200)
	tier, err := a.Store.Tier(context.Background(), "free")
	if err != nil || tier.SettlementThreshold != "0.025000" {
		t.Fatal("an unrelated edit replaced the threshold", err)
	}
	var snapshots int
	if err = a.Store.DB.QueryRow(context.Background(), `SELECT count(*) FROM tier_versions WHERE tier_id='free' AND snapshot->>'settlement_threshold' IN('0.025000','0.025')`).Scan(&snapshots); err != nil || snapshots != 2 {
		t.Fatal("settlement policy changes are not versioned", err, snapshots)
	}
	me := object(t, client.call("GET", "/api/me", nil, 200))
	if me["tier"].(map[string]any)["settlement_threshold"] != tier.SettlementThreshold {
		t.Fatal("account tier is missing its settlement policy")
	}
	billing := object(t, client.call("GET", "/api/billing", nil, 200))
	if billing["settlement_threshold"] != tier.SettlementThreshold || billing["settlement_window_seconds"] != float64(5) {
		t.Fatal("billing does not expose the current time and amount triggers")
	}
	if _, present := billing["max_outstanding"]; present || billing["calls_blocked"] != false {
		t.Fatal("billing retained a debt admission cap or blocked a healthy account")
	}
	admin := object(t, client.call("GET", "/api/admin/tiers", nil, 200))
	if admin["max_settlement_threshold"] != "100" {
		t.Fatal("admin editor cannot validate the settlement amount range")
	}
	var audits int
	if err = a.Store.DB.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='tier.save'`, user).Scan(&audits); err != nil || audits != 2 {
		t.Fatal("invalid thresholds changed policy or valid edits lost their audit", err)
	}
	body["settlement_threshold"] = "1.000001"
	client.call("PUT", "/api/admin/tiers", body, 200)
}
