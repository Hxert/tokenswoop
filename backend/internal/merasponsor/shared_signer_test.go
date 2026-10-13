package merasponsor

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"tokenswoop/backend/internal/store"
)

func TestPlatformSponsorSharesCollectionLock(t *testing.T) {
	s, f, key, user, wallet := setup(t)
	s.Config.SharedSigner = true
	ctx := context.Background()
	o := prepare(t, s, user, wallet)
	if _, err := s.Submit(ctx, o.ID, user, signature(key, o), nil); err != nil {
		t.Fatal(err)
	}
	lock, err := s.Store.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err = lock.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, store.PlatformSigningLock); err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(ctx); err != nil || f.sign != 0 {
		t.Fatal("signed while the collection worker owned the signer", err)
	}
	if err = lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(ctx); err != nil || f.sign != 1 {
		t.Fatal("did not resume after the signer was released", err)
	}
}

func TestPlatformSponsorWaitsForUnbroadcastDebtReport(t *testing.T) {
	s, f, key, user, wallet := setup(t)
	s.Config.SharedSigner = true
	ctx := context.Background()
	o := prepare(t, s, user, wallet)
	if _, err := s.Submit(ctx, o.ID, user, signature(key, o), nil); err != nil {
		t.Fatal(err)
	}
	_, err := s.Store.DB.Exec(ctx, `INSERT INTO reserve_debt_reports
 (id,user_id,wallet,chain_id,contract,operator,epoch,items,calldata,tx_hash,nonce,raw_cipher,state)
 VALUES($1,$2,$3,$4,$5,$6,0,'[]','0x','0xdef',5,'encrypted','pending')`, uuid.NewString(), user, wallet, s.ChainID, s.Config.Implementation, s.Config.Sponsor)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		resumed := *s // A restart must also honor the durable reservation.
		if err = resumed.Tick(ctx); err != nil || f.sign != 0 || f.broadcasts != 0 {
			t.Fatal("reused a nonce reserved before broadcast", err)
		}
	}
	if _, err = s.Store.DB.Exec(ctx, `UPDATE reserve_debt_reports SET state='confirmed'`); err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(ctx); err != nil || f.sign != 1 {
		t.Fatal("sponsorship did not resume after debt confirmation", err)
	}
}
