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
	walletCacheMaxEntriesEnv   = "OCTOPI_CACHE_WALLET_MAX_ENTRIES"
	defaultWalletCacheCapacity = 300
)

type walletSummaryCache struct {
	mu    sync.Mutex
	store *lru.Cache[string, walletSummaryEntry]
}

type walletSummaryEntry struct {
	rewards   []RewardRow
	expiresAt time.Time
}

func newWalletSummaryCache() *walletSummaryCache {
	maxEntries := loadIntEnv(walletCacheMaxEntriesEnv, defaultWalletCacheCapacity)
	if maxEntries <= 0 {
		return nil
	}
	store, err := lru.New[string, walletSummaryEntry](maxEntries)
	if err != nil {
		return nil
	}
	return &walletSummaryCache{
		store: store,
	}
}

func (c *walletSummaryCache) Get(key string, now time.Time) ([]RewardRow, bool) {
	if c == nil || key == "" {
		return nil, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.store.Get(key)
	if !ok {
		return nil, false
	}
	if now.After(entry.expiresAt) {
		c.store.Remove(key)
		return nil, false
	}
	return cloneRewardRows(entry.rewards), true
}

func (c *walletSummaryCache) Add(key string, rewards []RewardRow, expiresAt time.Time) {
	if c == nil || key == "" || expiresAt.IsZero() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store.Add(key, walletSummaryEntry{
		rewards:   cloneRewardRows(rewards),
		expiresAt: expiresAt,
	})
}

func cloneRewardRows(rows []RewardRow) []RewardRow {
	if len(rows) == 0 {
		return nil
	}
	cloned := make([]RewardRow, len(rows))
	copy(cloned, rows)
	return cloned
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
