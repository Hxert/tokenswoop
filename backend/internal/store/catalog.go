package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"sort"
	"strings"
)

const modelColumns = "id,name,vendor,enabled,tools,structured_output,price_version,input_price::text,output_price::text,cache_read_price::text,cache_write_price::text,cache_write_1h_price::text,pricing_configured,pricing"

func scanModel(r pgx.Row) (m Model, e error) {
	e = r.Scan(&m.ID, &m.Name, &m.Vendor, &m.Enabled, &m.Tools, &m.StructuredOutput, &m.PriceVersion, &m.InputPrice, &m.OutputPrice, &m.CacheReadPrice, &m.CacheWritePrice, &m.CacheWrite1hPrice, &m.PricingConfigured, &m.Pricing)
	return
}
func (s *Store) Model(ctx context.Context, id string) (Model, error) {
	return scanModel(s.DB.QueryRow(ctx, "SELECT "+modelColumns+" FROM models WHERE id=$1 AND enabled AND pricing_configured", id))
}
func (s *Store) Models(ctx context.Context, all bool) ([]Model, error) {
	r, e := s.DB.Query(ctx, "SELECT "+modelColumns+" FROM models WHERE ($1 OR (enabled AND pricing_configured)) ORDER BY vendor,id", all)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []Model{}
	for r.Next() {
		m, e := scanModel(r)
		if e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, r.Err()
}
func (s *Store) SaveModel(ctx context.Context, actor string, m Model) (Model, error) {
	if e := m.Pricing.Validate(); e != nil {
		return m, e
	}
	m.PricingConfigured = true
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return m, e
	}
	defer tx.Rollback(ctx)
	e = tx.QueryRow(ctx, `INSERT INTO models(id,name,vendor,enabled,tools,structured_output,input_price,output_price,cache_read_price,cache_write_price,cache_write_1h_price,pricing)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(id) DO UPDATE SET name=excluded.name,vendor=excluded.vendor,enabled=excluded.enabled,tools=excluded.tools,structured_output=excluded.structured_output,input_price=excluded.input_price,output_price=excluded.output_price,cache_read_price=excluded.cache_read_price,cache_write_price=excluded.cache_write_price,cache_write_1h_price=excluded.cache_write_1h_price,pricing=excluded.pricing,pricing_configured=true,price_version=models.price_version+1,updated_at=now() RETURNING price_version`, m.ID, m.Name, m.Vendor, m.Enabled, m.Tools, m.StructuredOutput, m.InputPrice, m.OutputPrice, m.CacheReadPrice, m.CacheWritePrice, m.CacheWrite1hPrice, m.Pricing).Scan(&m.PriceVersion)
	if e != nil {
		return m, e
	}
	b, e := json.Marshal(m)
	if e != nil {
		return m, e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO model_prices(model_id,version,snapshot) VALUES($1,$2,$3)", m.ID, m.PriceVersion, b); e != nil {
		return m, e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO audit_events(id,actor_id,action,target,details) VALUES($1,$2,'model.save',$3,$4)", uuid.NewString(), actor, m.ID, b); e != nil {
		return m, e
	}
	return m, tx.Commit(ctx)
}

const channelColumns = "id::text,name,protocol,base_url,secret_cipher,model_map,priority,enabled,failure_count,disabled_until,last_error,revision,options"

func scanChannel(row pgx.Row) (c Channel, e error) {
	var b, options []byte
	e = row.Scan(&c.ID, &c.Name, &c.Protocol, &c.BaseURL, &c.SecretCipher, &b, &c.Priority, &c.Enabled, &c.Failures, &c.DisabledUntil, &c.LastError, &c.Revision, &options)
	if e == nil {
		e = json.Unmarshal(b, &c.Models)
	}
	if e == nil {
		e = json.Unmarshal(options, &c.Options)
	}
	return
}

func (s *Store) Channel(ctx context.Context, id string) (Channel, error) {
	return scanChannel(s.DB.QueryRow(ctx, "SELECT "+channelColumns+" FROM channels WHERE id=$1", id))
}

func (s *Store) Channels(ctx context.Context, model string, all bool) ([]Channel, error) {
	rows, e := s.DB.Query(ctx, "SELECT "+channelColumns+` FROM channels WHERE ($1 OR (enabled AND (disabled_until IS NULL OR disabled_until<now()))) AND ($2='' OR model_map ? $2) ORDER BY priority DESC,id`, all, model)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		c, e := scanChannel(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) SaveChannel(ctx context.Context, actor string, c Channel) error {
	_, _, e := s.SaveChannelCatalog(ctx, actor, c, false, nil)
	return e
}

// DraftMetadata is only a starting point. Discovery never infers prices or enables a model.
type DraftMetadata struct {
	Name   string `json:"name"`
	Vendor string `json:"vendor"`
}

var ErrChannelConflict = errors.New("channel changed; reload before saving")
var ErrMissingModel = errors.New("a mapped public model does not exist")

// SaveChannelCatalog atomically saves mappings and creates missing, disabled model drafts.
// Revision zero creates a channel; updates must carry the revision the operator read.
func (s *Store) SaveChannelCatalog(ctx context.Context, actor string, c Channel, createMissing bool, defaults map[string]DraftMetadata) (created []string, revision int, e error) {
	created = []string{}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback(ctx)
	if c.Models == nil {
		c.Models = map[string]string{}
	}
	b, e := json.Marshal(c.Models)
	if e != nil {
		return nil, 0, e
	}
	options, e := json.Marshal(c.Options)
	if e != nil {
		return nil, 0, e
	}
	if c.Revision == 0 {
		e = tx.QueryRow(ctx, `INSERT INTO channels(id,name,protocol,base_url,secret_cipher,model_map,priority,enabled,options) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(id) DO NOTHING RETURNING revision`, c.ID, c.Name, c.Protocol, c.BaseURL, c.SecretCipher, b, c.Priority, c.Enabled, options).Scan(&revision)
	} else {
		e = tx.QueryRow(ctx, `UPDATE channels SET name=$2,protocol=$3,base_url=$4,secret_cipher=CASE WHEN $5='' THEN secret_cipher ELSE $5 END,model_map=$6,priority=$7,enabled=$8,options=$10,failure_count=0,disabled_until=NULL,last_error='',revision=revision+1 WHERE id=$1 AND revision=$9 RETURNING revision`, c.ID, c.Name, c.Protocol, c.BaseURL, c.SecretCipher, b, c.Priority, c.Enabled, c.Revision, options).Scan(&revision)
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, 0, ErrChannelConflict
	}
	if e != nil {
		return nil, 0, e
	}
	ids := make([]string, 0, len(c.Models))
	for id := range c.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids) // Consistent lock order when two channels import the same models.
	for _, id := range ids {
		if !createMissing {
			var exists bool
			if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM models WHERE id=$1)", id).Scan(&exists); e != nil {
				return nil, 0, e
			}
			if !exists {
				return nil, 0, ErrMissingModel
			}
			continue
		}
		d := defaults[id]
		if strings.TrimSpace(d.Name) == "" || len(d.Name) > 100 {
			d.Name = id
			if len(d.Name) > 100 {
				d.Name = d.Name[:100]
			}
		}
		if strings.TrimSpace(d.Vendor) == "" || len(d.Vendor) > 80 {
			d.Vendor = "Unconfigured"
		}
		m := Model{ID: id, Name: d.Name, Vendor: d.Vendor, PriceVersion: 1, InputPrice: "0", OutputPrice: "0", CacheReadPrice: "0", CacheWritePrice: "0", CacheWrite1hPrice: "0"}
		tag, err := tx.Exec(ctx, `INSERT INTO models(id,name,vendor,enabled,pricing_configured,input_price,output_price,cache_read_price,cache_write_price,cache_write_1h_price) VALUES($1,$2,$3,false,false,0,0,0,0,0) ON CONFLICT(id) DO NOTHING`, id, m.Name, m.Vendor)
		if err != nil {
			return nil, 0, err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		snapshot, err := json.Marshal(m)
		if err != nil {
			return nil, 0, err
		}
		if _, e = tx.Exec(ctx, "INSERT INTO model_prices(model_id,version,snapshot) VALUES($1,1,$2)", id, snapshot); e != nil {
			return nil, 0, e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO audit_events(id,actor_id,action,target,details) VALUES($1,$2,'model.import',$3,$4)", uuid.NewString(), actor, id, snapshot); e != nil {
			return nil, 0, e
		}
		created = append(created, id)
	}
	details, _ := json.Marshal(map[string]any{"revision": revision, "created_models": created, "model_count": len(c.Models)})
	_, e = tx.Exec(ctx, "INSERT INTO audit_events(id,actor_id,action,target,details) VALUES($1,$2,'channel.save',$3,$4)", uuid.NewString(), actor, c.ID, details)
	if e != nil {
		return nil, 0, e
	}
	return created, revision, tx.Commit(ctx)
}
func (s *Store) ChannelResult(ctx context.Context, id string, status int, outcome string) error {
	if status >= 200 && status < 300 && outcome == "completed" {
		_, e := s.DB.Exec(ctx, "UPDATE channels SET failure_count=0,disabled_until=NULL,last_error='' WHERE id=$1", id)
		return e
	}
	if status != 401 && status != 403 && status != 429 && status < 500 && status != 0 {
		return nil
	}
	_, e := s.DB.Exec(ctx, `UPDATE channels SET failure_count=failure_count+1,last_error=$2,disabled_until=CASE WHEN failure_count+1>=3 OR $3 IN(401,403) THEN now()+interval '60 seconds' ELSE disabled_until END WHERE id=$1`, id, outcome, status)
	return e
}

// JSONRows only executes constant, server-owned SQL. Numeric amounts must be cast to text in the query.
func (s *Store) JSONRows(ctx context.Context, query string, args ...any) ([]json.RawMessage, error) {
	r, e := s.DB.Query(ctx, query, args...)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []json.RawMessage{}
	for r.Next() {
		var b []byte
		if e = r.Scan(&b); e != nil {
			return nil, e
		}
		out = append(out, json.RawMessage(b))
	}
	return out, r.Err()
}
