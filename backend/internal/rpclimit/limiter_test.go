package rpclimit

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestEndpointLimitSharedAcrossClientsAndFailsClosed(t *testing.T) {
	dsn := os.Getenv("TEST_REDIS_URL")
	if dsn == "" {
		t.Skip("TEST_REDIS_URL required")
	}
	opts, err := redis.ParseURL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	a, b := redis.NewClient(opts), redis.NewClient(opts)
	defer a.Close()
	defer b.Close()
	endpoint := "https://test.invalid/" + uuid.NewString()
	api, worker := New(a, endpoint, 40), New(b, endpoint, 40)
	defer a.Del(context.Background(), api.key)
	if api.key != worker.key || strings.Contains(api.key, endpoint) {
		t.Fatal("endpoint key not shared or contains credentials")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	var wg sync.WaitGroup
	errors := make(chan error, 80)
	for i := 0; i < 80; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l := api
			if i%2 != 0 {
				l = worker
			}
			errors <- l.Wait(ctx)
		}(i)
	}
	wg.Wait()
	close(errors)
	for e := range errors {
		if e != nil {
			t.Fatal(e)
		}
	}
	if elapsed := time.Since(started); elapsed < 1900*time.Millisecond {
		t.Fatalf("80 shared calls bypassed 40 RPS: %v", elapsed)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if api.Wait(ctx2) == nil {
		t.Fatal("cancelled caller admitted")
	}
	_ = b.Close()
	if worker.Wait(context.Background()) == nil {
		t.Fatal("Redis outage bypassed RPC cap")
	}
}
