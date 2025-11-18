package gooctopi

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

const (
	signaturesMaxEntriesEnv   = "OCTOPI_CACHE_SIGNATURES_MAX_ENTRIES"
	signaturesTTLEnv          = "OCTOPI_CACHE_SIGNATURES_TTL"
	transactionsMaxEntriesEnv = "OCTOPI_CACHE_TRANSACTIONS_MAX_ENTRIES"
	transactionsTTLEnv        = "OCTOPI_CACHE_TRANSACTIONS_TTL"
	rewardsMaxEntriesEnv      = "OCTOPI_CACHE_REWARDS_MAX_ENTRIES"
	rewardsTTLEnv             = "OCTOPI_CACHE_REWARDS_TTL"
	cacheJanitorEnv           = "OCTOPI_CACHE_JANITOR_INTERVAL"
)

const (
	defaultSignaturesMaxEntries = 3000
	defaultSignaturesTTL        = 5 * time.Minute
	defaultTransactionsMax      = 3000
	defaultTransactionsTTL      = 10 * time.Minute
	defaultRewardsMaxEntries    = 1000
	defaultRewardsTTL           = 30 * time.Minute
	defaultJanitorInterval      = time.Minute
)

var defaultCacheSettings = loadCacheSettingsFromEnv()

type cacheConfig struct {
	maxEntries int
	ttl        time.Duration
}

type cacheSettings struct {
	signatures      cacheConfig
	transactions    cacheConfig
	rewards         cacheConfig
	janitorInterval time.Duration
}

func loadCacheSettingsFromEnv() cacheSettings {
	return cacheSettings{
		signatures: cacheConfig{
			maxEntries: loadIntEnv(signaturesMaxEntriesEnv, defaultSignaturesMaxEntries),
			ttl:        loadDurationEnv(signaturesTTLEnv, defaultSignaturesTTL),
		},
		transactions: cacheConfig{
			maxEntries: loadIntEnv(transactionsMaxEntriesEnv, defaultTransactionsMax),
			ttl:        loadDurationEnv(transactionsTTLEnv, defaultTransactionsTTL),
		},
		rewards: cacheConfig{
			maxEntries: loadIntEnv(rewardsMaxEntriesEnv, defaultRewardsMaxEntries),
			ttl:        loadDurationEnv(rewardsTTLEnv, defaultRewardsTTL),
		},
		janitorInterval: loadDurationEnv(cacheJanitorEnv, defaultJanitorInterval),
	}
}

func loadIntEnv(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	num, err := strconv.Atoi(value)
	if err != nil || num < 0 {
		return fallback
	}
	return num
}

func loadDurationEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	dur, err := time.ParseDuration(value)
	if err != nil || dur < 0 {
		return fallback
	}
	return dur
}

type cacheEntry[V any] struct {
	value    V
	storedAt time.Time
}

type methodCache[V any] struct {
	ttl   time.Duration
	mu    sync.RWMutex
	store *lru.Cache[string, cacheEntry[V]]
}

func newMethodCache[V any](cfg cacheConfig) *methodCache[V] {
	if cfg.maxEntries <= 0 {
		return nil
	}
	store, _ := lru.New[string, cacheEntry[V]](cfg.maxEntries)
	return &methodCache[V]{
		ttl:   cfg.ttl,
		store: store,
	}
}

func (c *methodCache[V]) Get(key string) (V, bool) {
	var zero V
	if c == nil || key == "" {
		return zero, false
	}
	c.mu.RLock()
	entry, ok := c.store.Get(key)
	c.mu.RUnlock()
	if !ok {
		return zero, false
	}
	if c.ttl > 0 && time.Since(entry.storedAt) > c.ttl {
		c.mu.Lock()
		c.store.Remove(key)
		c.mu.Unlock()
		return zero, false
	}
	return entry.value, true
}

func (c *methodCache[V]) Add(key string, value V) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	c.store.Add(key, cacheEntry[V]{value: value, storedAt: time.Now()})
	c.mu.Unlock()
}

func (c *methodCache[V]) Remove(key string) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	c.store.Remove(key)
	c.mu.Unlock()
}

func (c *methodCache[V]) PurgeExpired(now time.Time) {
	if c == nil || c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range c.store.Keys() {
		entry, ok := c.store.Peek(key)
		if !ok {
			continue
		}
		if now.Sub(entry.storedAt) > c.ttl {
			c.store.Remove(key)
		}
	}
}
