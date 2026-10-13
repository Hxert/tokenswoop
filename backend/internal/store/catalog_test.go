package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

func TestChannelImportAtomicityAndConcurrentEdits(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	actor := testutil.Account(t, s, "0")
	ch := store.Channel{ID: uuid.NewString(), Name: "Catalog", Protocol: "openai", BaseURL: "https://example.com/v1", SecretCipher: "fixture-cipher", Models: map[string]string{"shared-model": "upstream"}, Enabled: true}
	// A failed mapping cannot leave behind a partially saved channel or audit event.
	if _, _, e := s.SaveChannelCatalog(ctx, actor, ch, false, nil); !errors.Is(e, store.ErrMissingModel) {
		t.Fatalf("missing-model error: %v", e)
	}
	var count int
	if e := s.DB.QueryRow(ctx, "SELECT (SELECT count(*) FROM channels)+(SELECT count(*) FROM audit_events)").Scan(&count); e != nil || count != 0 {
		t.Fatal("failed import left partial data", e)
	}
	// Concurrent first imports share one disabled model and one price snapshot.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := ch
			c.ID = uuid.NewString()
			if _, _, e := s.SaveChannelCatalog(ctx, actor, c, true, nil); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if e := s.DB.QueryRow(ctx, "SELECT count(*) FROM models WHERE id='shared-model' AND NOT enabled AND NOT pricing_configured").Scan(&count); e != nil || count != 1 {
		t.Fatal("duplicate/unsafe draft", e)
	}
	if e := s.DB.QueryRow(ctx, "SELECT count(*) FROM model_prices WHERE model_id='shared-model'").Scan(&count); e != nil || count != 1 {
		t.Fatal("duplicate snapshots", e)
	}
	if _, e := s.DB.Exec(ctx, "UPDATE models SET enabled=true WHERE id='shared-model'"); e == nil {
		t.Fatal("database allowed unpriced model to become enabled")
	}
	_, revision, e := s.SaveChannelCatalog(ctx, actor, ch, true, nil)
	if e != nil {
		t.Fatal(e)
	}
	ch.Revision = revision
	successes := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := s.SaveChannelCatalog(ctx, actor, ch, false, nil)
			if err != nil && !errors.Is(err, store.ErrChannelConflict) {
				t.Error(err)
			}
			successes <- err == nil
		}()
	}
	wg.Wait()
	close(successes)
	successful := 0
	for ok := range successes {
		if ok {
			successful++
		}
	}
	if successful != 1 {
		t.Fatalf("concurrent editors both saved: %d", successful)
	}
	ch.Models = map[string]string{"stale-draft": "upstream"}
	if _, _, e = s.SaveChannelCatalog(ctx, actor, ch, true, nil); !errors.Is(e, store.ErrChannelConflict) {
		t.Fatal("stale update accepted", e)
	}
	if e = s.DB.QueryRow(ctx, "SELECT count(*) FROM models WHERE id='stale-draft'").Scan(&count); e != nil || count != 0 {
		t.Fatal("stale edit imported a draft", e)
	}
}
