# OctoPi Tracker – Product Requirements Document (PRD)

## Introduction
OctoPi Tracker is a crypto portfolio tracker, a web-based service with an open-source core (AGPL-3.0). It enables users to monitor the balance and performance of their crypto portfolios. The unique selling point is the correct accounting of **native staking** (ETH, SOL) and **liquid staking** (stETH, rETH, cbETH, mSOL, jitoSOL, bSOL) – calculation of return on investment (XIRR).
Target audience: retail investors with portfolios ranging from €1k to €100k.
Monetisation: free tier up to €10k in assets; paid subscription of π (3.14159) USDT/USDC per month for portfolios exceeding €10k.

## Problem Statement
Existing portfolio trackers:
- offer limited support for multiple chains and staking rewards,
- fail to accurately account for native and liquid staking yield,
- are inconvenient for multi-wallet users,
- charge high fees for retail investors.

There is a need for a **simple, transparent, and fair** service with a free tier for small portfolios and a symbolic fee for larger ones.

## Solution/Feature Overview
- Manual wallet entry (one address at a time). Think later about handling a portfolio – a set of wallets.
- Support for Ethereum and Solana: balances, staking rewards, LSD tokens.
- XIRR is calculated as if today all the amount was received back.
- Mind the current week plus the last full 4 weeks.
- Asset values displayed in either **EUR or USD** (user-selectable).
- Free tier up to €10k (or equivalent in USD).
- Paid subscription π (3.14159) USDT/USDC/month once portfolio value exceeds threshold.
- Data storage:
  - locally (LocalStorage/cookies) for persistence,
  - on server in-memory for caching and aggregating.
- CSV export (Pro only) with transaction/reward details.

## User Stories
1. As a user, I want to add an ETH or SOL wallet addressso I can see my wallet investing returns.
2. As a user, I want to view my portfolio balance in **EUR or USD** so I can measure performance in my preferred currency.
3. As a user, I want my staking rewards (native and liquid) to be correctly tracked so I see my real yield.
5. As a user with ≤ €10k/USD10k portfolio value, I want to use the service for free.
6. As a user with > €10k/USD10k portfolio value, I want to pay π USDT/USDC per month to continue using the service.
7. As a Pro user, I want to export CSV reports with transactions and staking rewards so I can prepare tax filings.
8. As a user, I want to see performance history.
9. As a user, I want to see best stakers or validators providing more yield.

## Technical Requirements
- Backend: Go (standard libraries).
- Frontend: Bootstrap + ApexCharts + vanilla JS/CSS.
   - Reference for the frontend: https://snowball-income.com/public/portfolios/mokKHBIZmY#divs
- Authentication: none, no third-party logins.
- Supported chains:
  - Ethereum (ETH balance, ETH2 staking, stETH/wstETH, rETH, cbETH),
  - Solana (SOL balance, stake accounts, mSOL, jitoSOL, bSOL).
- Base currency: user-selectable (EUR/USD).
- Price feeds: CoinGecko API (free tier).
- Blockchain data: Infura/Alchemy (Ethereum), Helius (Solana), free RPC tiers.
   - Lets keep in mind existing open-source solution https://github.com/hodgerpodger/staketaxcsv
- Rate limiter for API calls, caching responses for a predefined number of wallets – to have a memory consumption limitation.
- Subscription:
  - Subscription tied to wallet number.
  - Payment in USDT/USDC on Ethereum (ERC-20), Tron and Solana (SPL). To think later about a better way to handle this.
- CSV export fields: `date`, `chain`, `wallet_address`, `asset`, `amount`, `tx_hash`, `tx_type (staking_reward|transfer)`, `price`, `value`, `notes`.

### 📊 Charting Library

- **Library:** [ApexCharts.js](https://apexcharts.com/) (MIT Licence)
- **Integration:** Loaded via CDN.
- **Default chart type:** `column` (vertical bar chart) — used to display historical values such as portfolio performance, staking rewards, and income trends.
- **Styling:** Inherits Bootstrap colour variables and supports responsive layouts.
- **Interaction**: Hover tooltips, legend toggle, and smooth animated transitions when updating data.

### 🧭 Site Structure

#### 1. **Index Page** (`/index.html`)
Purpose: landing page + wallet address input.

- Elements:
  - Short service description.
  - Input field for wallet address.
  - "View portfolio" button.
- Mock is provided in `mocks/index_mock.html`
- Behaviour:
  - Validate address format.
  - On success → redirect to `/wallet/<address>`.
  - Show error message if invalid.
  - Optional "Demo wallet" button for quick preview.

#### 2. **Wallet Dashboard** (`/wallet/<address>`)
Purpose: show staking income analytics for the given wallet.

- Elements:
  - Link to the main page in the header – on the logo
  - Switch of the currency EUR/USD
  - Wallet summary (network, balance, balance in fiat currency, last update).
  - Annual return (XIRR).
  - Weekly rewards chart (stacked columns).
  - Recent rewards table.
- Mock is provided in `mocks/wallet_dashboard_mock.html`

### 🧠 Backend Data Retrieval and Performance

#### Solana Data Pipeline

- **Source library:** [`staketaxcsv`](https://github.com/hodgerpodger/staketaxcsv) (Python).
  - Used as a reference implementation for Solana staking analytics.
  - The service may reuse its logic (modules `rpc.py`, `rewards.py`, `wallet.py`) for:
    - retrieving staking rewards via `getInflationReward`;
    - resolving stake accounts via `getStakeAccountsByOwner`;
    - mapping epochs to timestamps via `getBlockTime`.

- **RPC endpoint:**
We are using Helius (`https://mainnet.helius-rpc.com/?api-key=...`) and respecting rate limits and [best practices](https://www.helius.dev/docs/rpc/optimization-techniques#efficient-account-queries).

#### Rate Limiting and Retry Logic

- A built-in **rate limiter** must ensure ≤10 RPC calls per second.
- When RPC responds with **HTTP 429 Too Many Requests**,
  the client must respect the `Retry-After` header and retry after the specified delay.
- Exponential backoff (`2^retries`, capped at 60s) recommended for robustness.
- All external RPC calls must pass through a shared throttling layer. Each external end-point has it's limits, could be configured in the code.

#### Data Depth and Update Policy

- Data history limited to **the current week plus the four full preceding weeks**.
  Example: if today is 9 Nov 2025 → data range = 6 Oct 2025 – 9 Nov 2025.
- Older epochs and transactions are ignored to reduce RPC load and response latency.
- This range typically corresponds to ~30 Solana epochs (~70–80 RPC calls max per wallet).

#### Caching Strategy

- **Epoch timestamps** (epoch → blockTime) cached globally; updated once per new epoch (≈ every 3–4 days).
- Current **price data (SOL/USD, SOL/EUR)** fetched from CoinGecko API and cached for 1 hour.
- **Stake account lists** may be cached per wallet for up to 24 hours.
- **Rewards data** cached incrementally — only new epochs queried on each refresh.

Wallet data should be cached respecting maximum amount of data for caching. Let's say, we can use up to 200 Mb of RAM for this, we should limit number of wallets to some reasonable number and delete the oldest one before adding a new one, if exceeding limits. We will use LRU for caching.



#### Ethereum Data Pipeline

- **Primary RPC provider:** [Alchemy](https://www.alchemy.com/pricing) (Free tier — 30 M Compute Units/month, ≤ 25 req/sec).
  - Fallback provider: [Cloudflare Ethereum Gateway](https://cloudflare-eth.com).
  - Go client: [`github.com/ethereum/go-ethereum/ethclient`](https://pkg.go.dev/github.com/ethereum/go-ethereum/ethclient).

- **Data sources:**
  | Purpose | Source | Endpoint / Method | Update frequency |
  |----------|---------|-------------------|------------------|
  | ETH balance | Alchemy RPC | `eth_getBalance` | on demand |
  | LSD token balances (stETH, rETH, cbETH) | Alchemy RPC | `eth_call` (ERC-20 `balanceOf`) via multicall | on demand |
  | Native staking (ETH2 validators) | [Beaconcha.in API](https://beaconcha.in/api/v1/validator/eth1/{address}) | REST JSON | daily |
  | Validator performance (rewards) | Beaconcha.in `/validator/{index}/performance` | REST JSON | daily |
  | Transaction history (90 days) | Alchemy Transfers API (`alchemy_getAssetTransfers`) | JSON-RPC | on demand |
  | Token prices (ETH/USD, ETH/EUR) | [CoinGecko API](https://api.coingecko.com/api/v3/coins/ethereum/market_chart?vs_currency=usd&days=90) | REST JSON | daily |


#### Caching Strategy (Lazy Load)

- **Cache implementation:** [`github.com/hashicorp/golang-lru`](https://github.com/hashicorp/golang-lru)
  - Eviction policy: **Least Recently Used (LRU)**
  - Each cached entry stores: `{value, expiration_timestamp}`
  - TTL configurable per data type (e.g. 24 h for prices, 24 h for validator performance).

- **Lazy-Load Policy:**
  - No background workers.
  - On first request → data fetched from source, stored in cache.
  - Subsequent requests within TTL → served from cache.
  - Expired or missing data triggers refresh and re-caching.

- **Cache invalidation:**
  - Expired entries checked upon `Get()`.
  - Manual eviction on memory pressure handled by LRU.
  - No separate GC routine required.


## Acceptance Criteria
- Display balances for ETH and SOL wallets.
- Correctly calculate staking rewards (ETH2 validator pubkey/index, Solana stake accounts).
- Support LSD tokens (stETH, rETH, cbETH, mSOL, jitoSOL, bSOL).
- Allow users to switch base currency between EUR and USD.
- When portfolio value exceeds €10k/USD10k, display paywall with payment option.
- After receiving π USDT/USDC on service wallet, subscription is activated for that portfolio.
- Wallet list persists across sessions (local + server sync).
- CSV export generates correct values for all required fields.
- Rate limiter ensures no breach of free RPC quotas.

## Constraints
- **Open-source core under AGPL-3.0 (non-negotiable).**
- **Payments accepted only in crypto (USDT/USDC).**
- **Only free-tier RPC providers at MVP launch.**
- **Development capacity limited to ~10h/week.**
- **MVP chains limited to Ethereum and Solana.**

## APIs/Data Models (Business Objects)

### Wallet
- wallet_id (UUID)
- address (string)
- chain (enum: ETH, SOL)

### Balance
- wallet_id (UUID)
- asset (string)
- amount (decimal)
- price (decimal)
- value (decimal)
- updated_at (timestamp)

### Subscription
- wallet_id (UUID)
- status (enum: free, active, expired)
- tx_hash (string)
- chain (enum: ETH, SOL)
- started_at (timestamp)
- expires_at (timestamp)

## UX Flow

### Add wallets and view balance
1. User opens landing page.
2. User enters one wallet address (ETH or SOL).
3. System validates address and redirects to dashboard page.
4. Browser redirects to dashboard page.

### Exceed free tier
6. If total portfolio value ≤ €10k/USD10k → continue free usage.
7. If total portfolio value > €10k/USD10k → display paywall.

### Payment – TBD

8. Paywall shows service wallet addresses (ETH ERC-20, SOL SPL) for subscription payment.
9. User transfers π (3.14159) USDT/USDC.
10. Backend listener validates transaction on-chain.
11. Subscription status for the portfolio is updated to "active".

### Pro Features – TBD

12. User with active subscription can export CSV report.
13. CSV includes transactions, staking rewards, and values in selected base currency.

## Other Notes
- UI should be lightweight and mobile-friendly (Bootstrap grid).
- BTC and USDT (non-staking) support may be added later.
- Notifications (Telegram/Email) and Premium tier features to be considered post-MVP.
- Roadmap:
  - v0.1: ETH & SOL balances, manual wallet entry.
  - v0.5: staking rewards, base currency switch, CSV export.
  - v1.0: paywall > €10k/USD10k, subscription flow.

### Repository & Code Quality Requirements

#### General Principles

The open-source repository must look and feel like a professionally maintained human-written project — clean, concise, and idiomatic.
All code and documentation should reflect best engineering practices, not auto-generated patterns.
Avoid verbose or repetitive comments.
Comment only why something is done, not what it does — code should explain itself.
No AI-style over-explaining, no templated summaries (“This function does X, Y, Z”).
Markdown docs and comments should use a neutral, technical tone — human but concise.

#### Testing
Minimum coverage: ~50 % for core logic (e.g., yield calculation, data aggregation).
Tests should include:
- Unit tests for data parsing, staking calculations, and caching logic.
- Integration tests for RPC/HTTP mocks.

No synthetic tests — each test should correspond to a real use case (ETH/SOL wallet simulation).
Use realistic sample data (e.g., validator rewards JSON, RPC responses) to make it reproducible.

#### Version Control Practices

Use semantic commits (feat:, fix:, refactor:, docs:).
Each feature branch should contain small, reviewable diffs.
PR descriptions should explain intent, not repeat code.

#### Open-Source Presentation

Add project badge shields (build status, license, coverage, version) in README.
Include a short "Contributing" section: how to report issues, style guide, and pull request rules.
Write commits and docs as if the next maintainer is a real person, not ChatGPT.
