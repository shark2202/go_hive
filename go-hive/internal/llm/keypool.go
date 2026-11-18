package llm

import (
	"fmt"
	"sync"
	"time"
)

type KeyHealth struct {
	RateLimitedUntil  float64
	ConsecutiveErrors int
	TotalRequests     int
	TotalSuccesses    int
}

type KeyPool struct {
	mu     sync.Mutex
	keys   []string
	index  int
	health map[string]*KeyHealth
	now    func() float64
}

func NewKeyPool(keys []string) (*KeyPool, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("KeyPool requires at least one key")
	}
	health := make(map[string]*KeyHealth, len(keys))
	for _, key := range keys {
		health[key] = &KeyHealth{}
	}
	return &KeyPool{
		keys:   append([]string(nil), keys...),
		health: health,
		now:    monotonicSeconds,
	}, nil
}

func monotonicSeconds() float64 {
	return time.Since(processStart).Seconds()
}

var processStart = time.Now()

func (p *KeyPool) Size() int { return len(p.keys) }

func (p *KeyPool) GetKey() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for range p.keys {
		key := p.keys[p.index]
		p.index = (p.index + 1) % len(p.keys)
		health := p.health[key]
		if health.RateLimitedUntil <= now {
			health.TotalRequests++
			return key
		}
	}
	soonest := p.keys[0]
	for _, key := range p.keys[1:] {
		if p.health[key].RateLimitedUntil < p.health[soonest].RateLimitedUntil {
			soonest = key
		}
	}
	p.health[soonest].TotalRequests++
	return soonest
}

func (p *KeyPool) MarkRateLimited(key string, retryAfter ...float64) {
	seconds := 60.0
	if len(retryAfter) > 0 {
		seconds = retryAfter[0]
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	health, ok := p.health[key]
	if !ok {
		return
	}
	health.RateLimitedUntil = p.now() + seconds
	health.ConsecutiveErrors++
}

func (p *KeyPool) MarkSuccess(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	health, ok := p.health[key]
	if !ok {
		return
	}
	health.ConsecutiveErrors = 0
	health.TotalSuccesses++
}

func (p *KeyPool) GetStats() map[string]map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	stats := make(map[string]map[string]any, len(p.keys))
	for _, key := range p.keys {
		health := p.health[key]
		stats["..."+lastChars(key, 6)] = map[string]any{
			"healthy":            health.RateLimitedUntil <= now,
			"requests":           health.TotalRequests,
			"successes":          health.TotalSuccesses,
			"consecutive_errors": health.ConsecutiveErrors,
		}
	}
	return stats
}

func lastChars(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[len(value)-n:]
}
