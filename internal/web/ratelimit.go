package web

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A limiter is a per-client token bucket.
//
// The endpoint is open to anyone and one miss costs eight seconds of CPU
// plus a round of requests to Nasdaq, who rate-limit by IP and will happily
// throttle this whole server for one visitor's script. The bucket is
// therefore sized for a person exploring -- a handful of symbols at once,
// then a steady trickle -- rather than for throughput.
type limiter struct {
	burst  int           // tokens available at once
	refill time.Duration // time to earn one back
	ttl    time.Duration // how long an idle bucket is remembered

	mu      sync.Mutex
	buckets map[string]*bucket
	swept   time.Time
}

type bucket struct {
	tokens float64
	seen   time.Time
}

func newLimiter(burst int, refill time.Duration) *limiter {
	return &limiter{
		burst:   burst,
		refill:  refill,
		ttl:     time.Hour,
		buckets: make(map[string]*bucket),
		swept:   time.Now(),
	}
}

// allow takes a token for client, reporting whether one was available and,
// if not, how long until the next one.
func (l *limiter) allow(client string) (bool, time.Duration) {
	if l == nil || l.burst <= 0 {
		return true, 0
	}
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)

	b, ok := l.buckets[client]
	if !ok {
		b = &bucket{tokens: float64(l.burst)}
		l.buckets[client] = b
	}
	if !b.seen.IsZero() {
		b.tokens += now.Sub(b.seen).Seconds() / l.refill.Seconds()
		if b.tokens > float64(l.burst) {
			b.tokens = float64(l.burst)
		}
	}
	b.seen = now

	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) * float64(l.refill))
	}
	b.tokens--
	return true, 0
}

// refund returns a token, for work that turned out not to need one.
func (l *limiter) refund(client string) {
	if l == nil || l.burst <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if b, ok := l.buckets[client]; ok && b.tokens < float64(l.burst) {
		b.tokens++
	}
}

// sweep drops buckets nobody has touched for a while, so the map tracks
// current visitors rather than every address that ever arrived. Callers
// hold the lock.
func (l *limiter) sweep(now time.Time) {
	if now.Sub(l.swept) < l.ttl {
		return
	}
	for key, b := range l.buckets {
		if now.Sub(b.seen) > l.ttl {
			delete(l.buckets, key)
		}
	}
	l.swept = now
}

// clientIP identifies the caller behind a proxy.
//
// Fly terminates TLS and forwards, so RemoteAddr is the proxy for every
// visitor and limiting on it would limit everyone as one. Fly-Client-IP is
// set by that proxy and is the trustworthy value; X-Forwarded-For is a
// fallback for other hosts and is read left-most, which is the original
// client. Both are client-settable in principle, so this is a fair-use
// measure, not a security boundary.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("Fly-Client-IP"); ip != "" {
		return ip
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if first, _, ok := strings.Cut(fwd, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(fwd)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
