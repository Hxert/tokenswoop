package main

import (
	"context"
	"github.com/redis/go-redis/v9"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/faucet"
	"tokenswoop/backend/internal/merasponsor"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/reserve"
	"tokenswoop/backend/internal/rpclimit"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/worker"
)

func main() {
	if e := run(); e != nil {
		slog.Error("worker stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	cfg, e := config.Load()
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s, e := store.Open(ctx, cfg.DatabaseURL)
	if e != nil {
		return e
	}
	defer s.DB.Close()
	if e = s.Migrate(ctx); e != nil {
		return e
	}
	if e = s.BindNetwork(ctx, cfg.ChainID); e != nil {
		return e
	}
	var settlement *payments.Service
	options, e := redis.ParseURL(cfg.RedisURL)
	if e != nil {
		return e
	}
	cache := redis.NewClient(options)
	defer cache.Close()
	if e = cache.Ping(ctx).Err(); e != nil {
		return e
	}
	rpcLimit := rpclimit.New(cache, cfg.Payments.RPC, cfg.Payments.RPCRequestsPerSecond)
	if cfg.Payments.Enabled {
		chain := payments.NewChain(cfg.Payments, rpcLimit.Wait)
		check, cancel := context.WithTimeout(ctx, 45*time.Second)
		e = chain.Validate(check)
		if e == nil {
			e = chain.ValidateSigner(check)
		}
		cancel()
		if e != nil {
			return e
		}
		settlement = payments.New(s, cfg.Payments, chain, cfg.Secret)
	}
	var reserves *reserve.Service
	if cfg.Reserve.Enabled {
		chain := reserve.NewChain(cfg.Payments, cfg.Reserve, rpcLimit.Wait)
		check, cancel := context.WithTimeout(ctx, 45*time.Second)
		e = chain.Validate(check)
		if e == nil {
			e = chain.ValidateSigner(check)
		}
		cancel()
		if e != nil {
			return e
		}
		reserves = reserve.New(s, cfg.Reserve, cfg.Payments.Token, chain, cfg.Secret)
	}
	var sponsorship *merasponsor.Service
	if cfg.Mera.Enabled {
		chain := merasponsor.NewChain(cfg.Payments, cfg.Mera, cfg.Reserve.Contract, rpcLimit.Wait)
		check, cancel := context.WithTimeout(ctx, 45*time.Second)
		e = chain.Validate(check, true)
		cancel()
		if e != nil {
			return e
		}
		sponsorship = &merasponsor.Service{Store: s, Config: cfg.Mera, ChainID: cfg.ChainID, Chain: chain}
	}
	slog.Info("TokenSwoop worker started", "settlement_enabled", cfg.Payments.Enabled, "rpc_rps", cfg.Payments.RPCRequestsPerSecond, "rpc_limit_scope", "shared_endpoint")
	var dispenser *faucet.Service
	if cfg.Faucet.Enabled && cfg.ChainID == 10143 {
		dispenser = &faucet.Service{Store: s, Config: cfg.Faucet, Payment: cfg.Payments, Secret: cfg.Secret, Chain: &faucet.RPCChain{RPC: payments.NewChain(cfg.Payments, rpcLimit.Wait)}}
	}
	worker.Run(ctx, s, settlement, reserves, sponsorship, dispenser)
	return nil
}
