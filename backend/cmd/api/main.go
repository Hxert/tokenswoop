package main

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/faucet"
	"tokenswoop/backend/internal/gateway"
	"tokenswoop/backend/internal/httpapi"
	"tokenswoop/backend/internal/merasponsor"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/reserve"
	"tokenswoop/backend/internal/rpclimit"
	"tokenswoop/backend/internal/store"
)

func main() {
	if e := run(); e != nil {
		slog.Error("API stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	cfg, e := config.Load()
	if e != nil {
		return e
	}
	if cfg.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
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
	go s.TrackInstance(ctx, "api")
	options, e := redis.ParseURL(cfg.RedisURL)
	if e != nil {
		return e
	}
	cache := redis.NewClient(options)
	defer cache.Close()
	if e = cache.Ping(ctx).Err(); e != nil {
		return e
	}
	app := &httpapi.App{Config: cfg, Store: s, Redis: cache, Gateway: gateway.New(s, cfg.Secret, cfg.AllowPrivateUpstreams)}
	rpcLimit := rpclimit.New(cache, cfg.Payments.RPC, cfg.Payments.RPCRequestsPerSecond)
	if cfg.Payments.Enabled {
		chain := payments.NewChain(cfg.Payments, rpcLimit.Wait)
		check, cancel := context.WithTimeout(ctx, 45*time.Second)
		e = chain.Validate(check)
		cancel()
		if e != nil {
			return e
		}
		app.Payments = payments.New(s, cfg.Payments, chain, cfg.Secret)
		app.ChainRPC = chain
	}
	if cfg.Reserve.Enabled {
		chain := reserve.NewChain(cfg.Payments, cfg.Reserve, rpcLimit.Wait)
		check, cancel := context.WithTimeout(ctx, 45*time.Second)
		e = chain.Validate(check)
		cancel()
		if e != nil {
			return e
		}
		app.Reserve = reserve.New(s, cfg.Reserve, cfg.Payments.Token, chain, cfg.Secret)
	}
	if cfg.Mera.Enabled {
		chain := merasponsor.NewChain(cfg.Payments, cfg.Mera, cfg.Reserve.Contract, rpcLimit.Wait)
		check, cancel := context.WithTimeout(ctx, 45*time.Second)
		e = chain.Validate(check, false)
		cancel()
		if e != nil {
			return e
		}
		app.Mera = &merasponsor.Service{Store: s, Config: cfg.Mera, ChainID: cfg.ChainID, Chain: chain}
	}
	if cfg.Faucet.Enabled && cfg.ChainID == 10143 {
		app.Faucet = &faucet.Service{Store: s, Config: cfg.Faucet, Payment: cfg.Payments, Secret: cfg.Secret, Verify: faucet.NewTurnstile(cfg.Faucet.SecretKey, cfg.Faucet.Hostname)}
	}
	server := &http.Server{Addr: cfg.Address, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32768}
	errs := make(chan error, 1)
	go func() {
		slog.Info("TokenSwoop API listening", "address", cfg.Address, "settlement_enabled", cfg.Payments.Enabled, "rpc_rps", cfg.Payments.RPCRequestsPerSecond, "rpc_limit_scope", "shared_endpoint")
		errs <- server.ListenAndServe()
	}()
	select {
	case e = <-errs:
		if !errors.Is(e, http.ErrServerClosed) {
			return e
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
	return nil
}
