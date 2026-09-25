package mcp

import (
	"math"
	"sync"
	"time"
)

const maxBuckets = 10000

// limiter is a keyed token bucket with bounded memory.
type limiter struct {
	mu       sync.Mutex
	capacity float64
	refill   float64 // tokens per second
	buckets  map[string]*bucket
	now      func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(capacity int, per time.Duration, now func() time.Time) *limiter {
	return &limiter{capacity: float64(capacity), refill: float64(capacity) / per.Seconds(), buckets: map[string]*bucket{}, now: now}
}

// take consumes one token for key; when none is left it returns the wait.
func (l *limiter) take(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.fill(key)
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, l.wait(b)
}

// peek reports whether key has a token left without consuming it.
func (l *limiter) peek(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.fill(key)
	if b.tokens >= 1 {
		return true, 0
	}
	return false, l.wait(b)
}

func (l *limiter) fill(key string) *bucket {
	now := l.now()
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) >= maxBuckets {
			l.gc(now)
		}
		b = &bucket{tokens: l.capacity, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.capacity, b.tokens+now.Sub(b.last).Seconds()*l.refill)
	b.last = now
	return b
}

func (l *limiter) wait(b *bucket) time.Duration {
	return time.Duration((1 - b.tokens) / l.refill * float64(time.Second))
}

func (l *limiter) gc(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.last).Seconds()*l.refill >= l.capacity {
			delete(l.buckets, k)
		}
	}
	// Many distinct keys (IPv6 clients, say) can all be active; dropping an
	// arbitrary one keeps memory bounded at the cost of refilling its bucket.
	for k := range l.buckets {
		if len(l.buckets) < maxBuckets {
			break
		}
		delete(l.buckets, k)
	}
}
