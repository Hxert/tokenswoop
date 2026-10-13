// Package rpclimit paces one RPC endpoint across API and worker processes.
package rpclimit

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Use Redis time, so different hosts cannot bypass the limit through clock skew.
// A denied attempt does not reserve a future slot or extend the key's lifetime.
var pace = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000000 + tonumber(t[2])
local previous = tonumber(redis.call('GET', KEYS[1]) or '0')
local remaining = previous + tonumber(ARGV[1]) - now
if remaining > 0 then return remaining end
redis.call('SET', KEYS[1], string.format('%.0f', now), 'PX', 2000)
return 0
`)

type Limiter struct {
	cache    *redis.Client
	key      string
	interval int64
}

func New(cache *redis.Client, endpoint string, rps int) *Limiter {
	if rps < 1 {
		rps = 1
	}
	// Do not store an endpoint credential in Redis keys or logs. The key deliberately
	// excludes process name, origin and RPS: all consumers share the endpoint quota.
	return &Limiter{cache: cache, key: fmt.Sprintf("tokenswoop:rpc:pace:%x", sha256.Sum256([]byte(endpoint))), interval: (1000000 + int64(rps) - 1) / int64(rps)}
}

func (l *Limiter) Wait(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		us, err := pace.Run(ctx, l.cache, []string{l.key}, l.interval).Int64()
		if err != nil {
			return fmt.Errorf("RPC rate limiter unavailable")
		}
		if us == 0 {
			return nil
		}
		timer := time.NewTimer(time.Duration(us) * time.Microsecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
