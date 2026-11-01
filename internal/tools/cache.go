package tools

import (
	"context"
	"sync"
	"time"
)

// frameCache protects cameras from repeated requests: a frame for the same camera and width is
// reused for ttl, and concurrent requests for the same frame share one fetch. Errors are not
// cached, so an offline camera is retried on the next call.
type frameCache struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[cacheKey]cached
	flights map[cacheKey]*flight
}

type cacheKey struct {
	camera string
	width  int
}

type cached struct {
	jpeg []byte
	at   time.Time // when the frame was taken
}

type flight struct {
	done chan struct{}
	res  cached
	err  error
}

func newFrameCache(ttl time.Duration) *frameCache {
	return &frameCache{ttl: ttl, entries: map[cacheKey]cached{}, flights: map[cacheKey]*flight{}}
}

// get returns a cached frame if it is fresh, waits for an identical fetch in progress, or calls
// fetch. fetch runs detached from ctx so one caller giving up doesn't fail the others waiting on it;
// the go2rtc client's own timeout still bounds it.
func (c *frameCache) get(ctx context.Context, k cacheKey, fetch func(context.Context) ([]byte, error)) (cached, error) {
	c.mu.Lock()
	if e, ok := c.entries[k]; ok && time.Since(e.at) < c.ttl {
		c.mu.Unlock()
		return e, nil
	}
	f, ok := c.flights[k]
	if !ok {
		f = &flight{done: make(chan struct{})}
		c.flights[k] = f
		go c.run(context.WithoutCancel(ctx), k, f, fetch)
	}
	c.mu.Unlock()

	select {
	case <-f.done:
		return f.res, f.err
	case <-ctx.Done():
		return cached{}, ctx.Err()
	}
}

func (c *frameCache) run(ctx context.Context, k cacheKey, f *flight, fetch func(context.Context) ([]byte, error)) {
	jpeg, err := fetch(ctx)
	f.res, f.err = cached{jpeg: jpeg, at: time.Now()}, err

	c.mu.Lock()
	delete(c.flights, k)
	if err == nil && c.ttl > 0 {
		for key, e := range c.entries { // drop stale entries so the map stays small
			if time.Since(e.at) >= c.ttl {
				delete(c.entries, key)
			}
		}
		c.entries[k] = f.res
	}
	c.mu.Unlock()
	close(f.done)
}
