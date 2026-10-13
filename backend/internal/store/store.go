package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"time"
	"tokenswoop/backend/internal/pricing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

//go:embed 002_cache_write_breakdown.sql
var cacheBreakdownMigration string

//go:embed 003_postpaid_billing.sql
var postpaidMigration string

//go:embed 004_wallet_billing.sql
var walletMigration string

//go:embed 005_channel_catalog.sql
var channelCatalogMigration string

//go:embed 006_llm_compatibility.sql
var llmCompatibilityMigration string

//go:embed 007_pricing_observability.sql
var pricingObservabilityMigration string

//go:embed 008_provider_token_limits.sql
var providerTokenLimitsMigration string

//go:embed 009_unlimited_key_budget.sql
var unlimitedKeyBudgetMigration string

//go:embed 010_reserve_tiers.sql
var reserveTiersMigration string

//go:embed 011_wallet_only.sql
var walletOnlyMigration string

//go:embed 012_tier_settlement.sql
var tierSettlementMigration string

//go:embed 013_settlement_admission.sql
var settlementAdmissionMigration string

//go:embed 014_privy_wallets.sql
var privyWalletsMigration string

//go:embed 015_wallet_sessions.sql
var walletSessionsMigration string

//go:embed 016_reserve_debt.sql
var reserveDebtMigration string

//go:embed 017_settlement_recovery.sql
var settlementRecoveryMigration string

//go:embed 018_payment_recipient.sql
var paymentRecipientMigration string

//go:embed 019_mera_sponsorship.sql
var meraSponsorshipMigration string

//go:embed 020_testnet_faucet.sql
var testnetFaucetMigration string

type WalletPolicy struct {
	CheckAllowance            bool
	ChainID                   int64
	Contract, Token, Provider string
}

func (s *Store) ReserveNetworkID() int64 {
	if s.ReserveChainID == 0 {
		return 10143
	}
	return s.ReserveChainID
}

type Store struct {
	DB              *pgxpool.Pool
	WalletPolicy    *WalletPolicy
	ReserveContract string
	ReserveChainID  int64
	ReserveDebtMode bool
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err = db.Ping(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db}, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(78016345)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		return err
	}
	var applied bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=1)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, schema); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=2)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, cacheBreakdownMigration); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=3)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, postpaidMigration); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=4)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, walletMigration); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=5)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, channelCatalogMigration); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=6)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, llmCompatibilityMigration); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=7)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, pricingObservabilityMigration); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=8)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, providerTokenLimitsMigration); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=9)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, unlimitedKeyBudgetMigration); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=10)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, reserveTiersMigration); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=11)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, walletOnlyMigration); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=12)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, tierSettlementMigration); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=13)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, settlementAdmissionMigration); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=14)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, privyWalletsMigration); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=15)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, walletSessionsMigration); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=16)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, reserveDebtMigration); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=17)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, settlementRecoveryMigration); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=18)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, paymentRecipientMigration); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=19)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, meraSponsorshipMigration); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=20)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err = tx.Exec(ctx, testnetFaucetMigration); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

type User struct {
	ID           string    `json:"id"`
	Wallet       string    `json:"wallet"`
	Role         string    `json:"role"`
	Status       string    `json:"status"`
	Tier         string    `json:"tier"`
	TierOverride bool      `json:"tier_override"`
	Unpaid       string    `json:"unpaid_balance"`
	CreatedAt    time.Time `json:"created_at"`
}
type Tier struct {
	ID                  string  `json:"id"`
	DiscountBPS         int     `json:"discount_bps"`
	RPM                 int     `json:"rpm"`
	Concurrency         int     `json:"concurrency"`
	MinPrincipal        *string `json:"min_principal"`
	Version             int     `json:"version"`
	SettlementThreshold string  `json:"settlement_threshold"`
}
type Key struct {
	ID        string     `json:"id"`
	UserID    string     `json:"user_id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	Status    string     `json:"status"`
	Models    []string   `json:"allowed_models"`
	Budget    *string    `json:"budget"`
	Spent     string     `json:"spent"`
	ExpiresAt *time.Time `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
}
type Model struct {
	Pricing           pricing.Config `json:"pricing"`
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Vendor            string         `json:"vendor"`
	Enabled           bool           `json:"enabled"`
	PricingConfigured bool           `json:"pricing_configured"`
	Tools             bool           `json:"tools"`
	StructuredOutput  bool           `json:"structured_output"`
	PriceVersion      int            `json:"price_version"`
	InputPrice        string         `json:"input_price"`
	OutputPrice       string         `json:"output_price"`
	CacheReadPrice    string         `json:"cache_read_price"`
	CacheWritePrice   string         `json:"cache_write_price"`
	CacheWrite1hPrice string         `json:"cache_write_1h_price"`
}

func (m Model) Rates() pricing.Rates {
	return pricing.Rates{Input: m.InputPrice, Output: m.OutputPrice, CacheRead: m.CacheReadPrice, CacheWrite: m.CacheWritePrice, CacheWrite1h: m.CacheWrite1hPrice}
}

type Channel struct {
	Options       ChannelOptions    `json:"options"`
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Protocol      string            `json:"protocol"`
	BaseURL       string            `json:"base_url"`
	SecretCipher  string            `json:"-"`
	Models        map[string]string `json:"model_map"`
	Priority      int               `json:"priority"`
	Enabled       bool              `json:"enabled"`
	Revision      int               `json:"revision"`
	Failures      int               `json:"failure_count"`
	DisabledUntil *time.Time        `json:"disabled_until"`
	LastError     string            `json:"last_error"`
}

const userColumns = "id::text,wallet,role,status,tier,unpaid_balance::text,created_at,tier_override"

func scanUser(row pgx.Row) (u User, err error) {
	err = row.Scan(&u.ID, &u.Wallet, &u.Role, &u.Status, &u.Tier, &u.Unpaid, &u.CreatedAt, &u.TierOverride)
	return
}
func (s *Store) User(ctx context.Context, id string) (User, error) {
	return scanUser(s.DB.QueryRow(ctx, "SELECT "+userColumns+" FROM users WHERE id=$1", id))
}
func (s *Store) Tier(ctx context.Context, id string) (t Tier, err error) {
	err = s.DB.QueryRow(ctx, "SELECT id,discount_bps,rpm,concurrency,min_principal::text,version,settlement_threshold::text FROM tiers WHERE id=$1", id).Scan(&t.ID, &t.DiscountBPS, &t.RPM, &t.Concurrency, &t.MinPrincipal, &t.Version, &t.SettlementThreshold)
	return
}
func (s *Store) Audit(ctx context.Context, actor, action, target string, details any) error {
	b, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, "INSERT INTO audit_events(id,actor_id,action,target,details) VALUES($1,$2,$3,$4,$5)", uuid.NewString(), actor, action, target, b)
	return err
}

var ErrSettlementUnavailable = errors.New("wallet settlement is not configured on this server")
var ErrBudget = errors.New("API key budget exceeded")
var ErrUnavailable = errors.New("account or key is unavailable")
var ErrPaymentRequired = errors.New("payment allowance is insufficient; increase allowance to cover outstanding usage and new requests")
var ErrSettlementFailed = errors.New("payment failed; settle the outstanding bill to continue")
var ErrConcurrency = errors.New("concurrency limit reached")
var ErrWithdrawalPending = errors.New("new API calls are paused while a reserve withdrawal is pending")
var ErrReserveStale = errors.New("reserve status or tier changed; refresh and try again")
