package worker

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

func TestSweepRespectsLongReasoningDeadline(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	user := testutil.Account(t, s, "1")
	m, e := s.SaveModel(ctx, user, testutil.Model())
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name     string
		deadline *time.Time
		want     string
	}{
		{"live reasoning", ptrTime(time.Now().Add(time.Hour)), "in_flight"},
		{"expired attempt", ptrTime(time.Now().Add(-time.Minute)), "usage_unknown"},
		{"legacy stale", nil, "usage_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid.NewString()
			if e := s.BeginRequest(ctx, id, user, "", m, store.Tier{Concurrency: 10}); e != nil {
				t.Fatal(e)
			}
			if _, e := s.DB.Exec(ctx, "UPDATE requests SET state='in_flight',updated_at=now()-interval '20 minutes',upstream_deadline_at=$2 WHERE id=$1", id, tc.deadline); e != nil {
				t.Fatal(e)
			}
			if e := Sweep(ctx, s); e != nil {
				t.Fatal(e)
			}
			var state string
			s.DB.QueryRow(ctx, "SELECT state FROM requests WHERE id=$1", id).Scan(&state)
			if state != tc.want {
				t.Fatalf("state %s want %s", state, tc.want)
			}
		})
	}
}
func ptrTime(t time.Time) *time.Time { return &t }
