package store

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"time"
)

// Cleanup processes bounded batches and commits progress with the deletion, so
// interrupted workers resume without deleting financial or unresolved records.
func (s *Store) ProcessOperations(ctx context.Context) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var id string
	var raw []byte
	e = tx.QueryRow(ctx, `SELECT id::text,parameters FROM operations_tasks WHERE state IN('queued','running') ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id, &raw)
	if e == pgx.ErrNoRows {
		return nil
	}
	if e != nil {
		return e
	}
	var params struct {
		Before time.Time `json:"before"`
	}
	if json.Unmarshal(raw, &params) != nil || params.Before.IsZero() {
		_, e = tx.Exec(ctx, `UPDATE operations_tasks SET state='failed',error_code='invalid_parameters',finished_at=now() WHERE id=$1`, id)
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	result, e := tx.Exec(ctx, `DELETE FROM request_attempts WHERE id IN(SELECT a.id FROM request_attempts a JOIN requests r ON r.id=a.request_id WHERE r.created_at<$1 AND r.state IN('completed','failed') LIMIT 500)`, params.Before)
	if e != nil {
		return e
	}
	state := "running"
	var finished any
	if result.RowsAffected() < 500 {
		state = "completed"
		finished = time.Now()
	}
	_, e = tx.Exec(ctx, `UPDATE operations_tasks SET state=$2,processed=processed+$3,finished_at=$4 WHERE id=$1`, id, state, result.RowsAffected(), finished)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *Store) TrackInstance(ctx context.Context, role string) {
	id := uuid.NewString()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		call, cancel := context.WithTimeout(ctx, 3*time.Second)
		s.DB.Exec(call, `INSERT INTO runtime_instances(id,role) VALUES($1,$2) ON CONFLICT(id) DO UPDATE SET last_seen=now()`, id, role)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
