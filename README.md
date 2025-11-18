# go-octopi
A crypto wallet tracker for monitoring staking rewards for SOL and ETH

## Configuration

OctoPi keeps frequently requested RPC data in memory to reduce API latency while staying within a 700 MB footprint on small VPS hosts. Each cache has sensible defaults and can be tuned at runtime via environment variables (they all share the `OCTOPI_` prefix used elsewhere in the project):

- `OCTOPI_CACHE_SIGNATURES_MAX_ENTRIES` / `OCTOPI_CACHE_SIGNATURES_TTL` – defaults: `3000`, `5m`
- `OCTOPI_CACHE_TRANSACTIONS_MAX_ENTRIES` / `OCTOPI_CACHE_TRANSACTIONS_TTL` – defaults: `3000`, `10m`
- `OCTOPI_CACHE_REWARDS_MAX_ENTRIES` / `OCTOPI_CACHE_REWARDS_TTL` – defaults: `1000`, `30m`
- `OCTOPI_CACHE_JANITOR_INTERVAL` – defaults to `1m` and controls how often stale entries are purged in the background.

Use Go-style duration strings (e.g. `15m`, `1h`) for TTL and janitor values. Setting `MAX_ENTRIES` to `0` disables the corresponding cache completely.
