package gooctopi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	solanaMainnetHost  = "mainnet.helius-rpc.com"
	defaultHTTPTimeout = 5 * time.Second
	stakeProgramID     = "Stake11111111111111111111111111111111111111"
)

// SolanaClient defines the operations needed by the HTTP layer.
type SolanaClient interface {
	GetBalance(ctx context.Context, address string) (uint64, error)
	ListStakeAccounts(ctx context.Context, owner string) ([]StakeAccount, error)
	GetSignaturesForAddress(ctx context.Context, address string, limit int) ([]SignatureInfo, error)
	GetTransaction(ctx context.Context, signature string) (*TransactionDetail, error)
	GetInflationReward(ctx context.Context, addresses []string, epoch *uint64) ([]*InflationReward, error)
	GetEpochInfo(ctx context.Context) (*EpochInfo, error)
}

// RPCSolanaClient calls the public Solana JSON-RPC endpoint.
type RPCSolanaClient struct {
	Endpoint   string
	HTTPClient *http.Client
}

// StakeAccount carries parsed information about a delegated stake account.
type StakeAccount struct {
	Address           string
	DelegatedLamports uint64
	State             string
	VoteAccount       string
}

// SignatureInfo represents a transaction signature reference for an address.
type SignatureInfo struct {
	Signature string
	Slot      uint64
	BlockTime *int64
}

// TransactionDetail contains the subset of fields we care about from getTransaction.
type TransactionDetail struct {
	Slot      uint64
	BlockTime *int64
	Meta      TransactionMeta
}

// TransactionMeta captures the reward entries emitted for a transaction.
type TransactionMeta struct {
	Rewards []TransactionReward
}

// TransactionReward represents a single reward event inside a transaction.
type TransactionReward struct {
	Pubkey      string
	Lamports    int64
	PostBalance uint64
	RewardType  string
	Commission  *int
	VoteAccount string
}

// InflationReward represents a reward entry returned from getInflationReward.
type InflationReward struct {
	Epoch         uint64
	EffectiveSlot uint64
	Amount        int64
	PostBalance   uint64
	Commission    *int
}

// EpochInfo carries the current epoch metadata.
type EpochInfo struct {
	Epoch uint64
}

// GetBalance retrieves the lamport balance for the provided wallet.
func (c *RPCSolanaClient) GetBalance(ctx context.Context, address string) (uint64, error) {
	payload := rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "getBalance",
		Params: []any{
			address,
			map[string]string{"commitment": "confirmed"},
		},
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return 0, fmt.Errorf("encode request: %w", err)
	}

	resp, err := c.doRPCRequest(ctx, buf.Bytes())
	if err != nil {
		return 0, fmt.Errorf("rpc request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return 0, fmt.Errorf("rpc status %d: %s", resp.StatusCode, string(body))
	}

	var rpcResp rpcGetBalanceResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}

	if rpcResp.Error != nil {
		return 0, fmt.Errorf("rpc error (%d): %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	if rpcResp.Result == nil {
		return 0, fmt.Errorf("rpc response missing result")
	}

	return rpcResp.Result.Value, nil
}

func (c *RPCSolanaClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	c.HTTPClient = newRateLimitedHTTPClient(c.endpoint())
	return c.HTTPClient
}

func (c *RPCSolanaClient) endpoint() string {
	if c.Endpoint == "" {
		panic("RPCSolanaClient endpoint not configured")
	}
	return c.Endpoint
}

// ListStakeAccounts fetches stake accounts where the provided wallet is authorized.
func (c *RPCSolanaClient) ListStakeAccounts(ctx context.Context, owner string) ([]StakeAccount, error) {
	payload := rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "getProgramAccounts",
		Params: []any{
			stakeProgramID,
			map[string]any{
				"encoding":   "jsonParsed",
				"commitment": "confirmed",
				"filters": []any{
					map[string]any{
						"memcmp": map[string]any{
							"offset": 12, // TODO: refine offset calculation for production readiness
							"bytes":  owner,
						},
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return nil, fmt.Errorf("encode stake request: %w", err)
	}

	resp, err := c.doRPCRequest(ctx, buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("rpc request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("rpc status %d: %s", resp.StatusCode, string(body))
	}

	var rpcResp rpcProgramAccountsResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("decode stake response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("rpc error (%d): %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	var accounts []StakeAccount
	for _, acct := range rpcResp.Result {
		if acct.Account.Data.Parsed == nil {
			continue
		}
		if acct.Account.Data.Parsed.Info.Meta.Authorized.Staker != owner &&
			acct.Account.Data.Parsed.Info.Meta.Authorized.Withdrawer != owner {
			continue
		}

		lamports, err := jsonNumberToUint64(acct.Account.Data.Parsed.Info.Stake.Delegation.Stake)
		if err != nil {
			continue
		}

		accounts = append(accounts, StakeAccount{
			Address:           acct.Pubkey,
			DelegatedLamports: lamports,
			State:             acct.Account.Data.Parsed.Type,
			VoteAccount:       acct.Account.Data.Parsed.Info.Stake.Delegation.Voter,
		})
	}

	return accounts, nil
}

// GetSignaturesForAddress returns the most recent signatures touching the address.
func (c *RPCSolanaClient) GetSignaturesForAddress(ctx context.Context, address string, limit int) ([]SignatureInfo, error) {
	if limit <= 0 {
		limit = 1
	}

	payload := rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "getSignaturesForAddress",
		Params: []any{
			address,
			map[string]any{
				"limit":      limit,
				"commitment": "confirmed",
			},
		},
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return nil, fmt.Errorf("encode signatures request: %w", err)
	}

	resp, err := c.doRPCRequest(ctx, buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("rpc request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("rpc status %d: %s", resp.StatusCode, string(body))
	}

	var rpcResp rpcGetSignaturesResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("decode signatures response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("rpc error (%d): %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	results := make([]SignatureInfo, 0, len(rpcResp.Result))
	for _, item := range rpcResp.Result {
		results = append(results, SignatureInfo{
			Signature: item.Signature,
			Slot:      item.Slot,
			BlockTime: item.BlockTime,
		})
	}
	return results, nil
}

// GetTransaction fetches a parsed transaction and returns its reward data.
func (c *RPCSolanaClient) GetTransaction(ctx context.Context, signature string) (*TransactionDetail, error) {
	payload := rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "getTransaction",
		Params: []any{
			signature,
			map[string]any{
				"encoding":                       "json",
				"commitment":                     "confirmed",
				"maxSupportedTransactionVersion": 0,
			},
		},
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return nil, fmt.Errorf("encode transaction request: %w", err)
	}

	resp, err := c.doRPCRequest(ctx, buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("rpc request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("rpc status %d: %s", resp.StatusCode, string(body))
	}

	var rpcResp rpcGetTransactionResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("decode transaction response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("rpc error (%d): %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if rpcResp.Result == nil {
		return nil, fmt.Errorf("transaction not found")
	}

	meta := TransactionMeta{}
	if rpcResp.Result.Meta != nil {
		for _, reward := range rpcResp.Result.Meta.Rewards {
			meta.Rewards = append(meta.Rewards, TransactionReward{
				Pubkey:      reward.Pubkey,
				Lamports:    reward.Lamports,
				PostBalance: reward.PostBalance,
				RewardType:  reward.RewardType,
				Commission:  reward.Commission,
				VoteAccount: reward.VoteAccount,
			})
		}
	}

	return &TransactionDetail{
		Slot:      rpcResp.Result.Slot,
		BlockTime: rpcResp.Result.BlockTime,
		Meta:      meta,
	}, nil
}

// GetInflationReward fetches inflation rewards for the provided stake accounts.
func (c *RPCSolanaClient) GetInflationReward(ctx context.Context, addresses []string, epoch *uint64) ([]*InflationReward, error) {
	if len(addresses) == 0 {
		return nil, nil
	}

	params := []any{addresses}
	config := map[string]any{
		"commitment": "confirmed",
	}
	if epoch != nil {
		config["epoch"] = *epoch
	}
	params = append(params, config)

	payload := rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "getInflationReward",
		Params:  params,
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return nil, fmt.Errorf("encode inflation reward request: %w", err)
	}

	resp, err := c.doRPCRequest(ctx, buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("rpc request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("rpc status %d: %s", resp.StatusCode, string(body))
	}

	var rpcResp rpcGetInflationRewardResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("decode inflation reward response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("rpc error (%d): %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	results := make([]*InflationReward, 0, len(rpcResp.Result))
	for _, entry := range rpcResp.Result {
		if entry == nil {
			results = append(results, nil)
			continue
		}
		results = append(results, &InflationReward{
			Epoch:         entry.Epoch,
			EffectiveSlot: entry.EffectiveSlot,
			Amount:        entry.Amount,
			PostBalance:   entry.PostBalance,
			Commission:    entry.Commission,
		})
	}

	return results, nil
}

// GetEpochInfo retrieves the current epoch metadata.
func (c *RPCSolanaClient) GetEpochInfo(ctx context.Context) (*EpochInfo, error) {
	payload := rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "getEpochInfo",
		Params:  []any{},
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return nil, fmt.Errorf("encode epoch info request: %w", err)
	}

	resp, err := c.doRPCRequest(ctx, buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("rpc request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("rpc status %d: %s", resp.StatusCode, string(body))
	}

	var rpcResp rpcGetEpochInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("decode epoch info response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("rpc error (%d): %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if rpcResp.Result == nil {
		return nil, fmt.Errorf("epoch info missing result")
	}

	return &EpochInfo{
		Epoch: rpcResp.Result.Epoch,
	}, nil
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcGetBalanceResponse struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      int               `json:"id"`
	Result  *rpcBalanceResult `json:"result"`
	Error   *rpcError         `json:"error"`
}

type rpcBalanceResult struct {
	Context rpcContext `json:"context"`
	Value   uint64     `json:"value"`
}

type rpcContext struct {
	Slot uint64 `json:"slot"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcProgramAccountsResponse struct {
	JSONRPC string              `json:"jsonrpc"`
	ID      int                 `json:"id"`
	Result  []rpcProgramAccount `json:"result"`
	Error   *rpcError           `json:"error"`
}

type rpcProgramAccount struct {
	Pubkey  string                `json:"pubkey"`
	Account rpcProgramAccountInfo `json:"account"`
}

type rpcProgramAccountInfo struct {
	Data rpcProgramAccountData `json:"data"`
}

type rpcProgramAccountData struct {
	Parsed *rpcStakeAccountParsed `json:"parsed"`
}

type rpcStakeAccountParsed struct {
	Type string              `json:"type"`
	Info rpcStakeAccountInfo `json:"info"`
}

type rpcStakeAccountInfo struct {
	Meta struct {
		Authorized struct {
			Staker     string `json:"staker"`
			Withdrawer string `json:"withdrawer"`
		} `json:"authorized"`
	} `json:"meta"`
	Stake struct {
		Delegation rpcStakeDelegation `json:"delegation"`
	} `json:"stake"`
}

type rpcStakeDelegation struct {
	Stake json.Number `json:"stake"`
	Voter string      `json:"voter"`
}

func jsonNumberToUint64(n json.Number) (uint64, error) {
	if n == "" {
		return 0, fmt.Errorf("empty number")
	}
	if i, err := n.Int64(); err == nil && i >= 0 {
		return uint64(i), nil
	}
	return parseUintString(n.String())
}

func parseUintString(s string) (uint64, error) {
	var result uint64
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("invalid digit %q", ch)
		}
		result = result*10 + uint64(ch-'0')
	}
	return result, nil
}

type rpcGetSignaturesResponse struct {
	JSONRPC string             `json:"jsonrpc"`
	ID      int                `json:"id"`
	Result  []rpcSignatureInfo `json:"result"`
	Error   *rpcError          `json:"error"`
}

type rpcSignatureInfo struct {
	Signature string `json:"signature"`
	Slot      uint64 `json:"slot"`
	BlockTime *int64 `json:"blockTime"`
}

type rpcGetTransactionResponse struct {
	JSONRPC string                `json:"jsonrpc"`
	ID      int                   `json:"id"`
	Result  *rpcTransactionResult `json:"result"`
	Error   *rpcError             `json:"error"`
}

type rpcTransactionResult struct {
	Slot      uint64              `json:"slot"`
	BlockTime *int64              `json:"blockTime"`
	Meta      *rpcTransactionMeta `json:"meta"`
}

type rpcTransactionMeta struct {
	Rewards []rpcTransactionReward `json:"rewards"`
}

type rpcTransactionReward struct {
	Pubkey      string `json:"pubkey"`
	Lamports    int64  `json:"lamports"`
	PostBalance uint64 `json:"postBalance"`
	RewardType  string `json:"rewardType"`
	Commission  *int   `json:"commission"`
	VoteAccount string `json:"voteAccount"`
}

type rpcGetInflationRewardResponse struct {
	JSONRPC string                `json:"jsonrpc"`
	ID      int                   `json:"id"`
	Result  []*rpcInflationReward `json:"result"`
	Error   *rpcError             `json:"error"`
}

type rpcInflationReward struct {
	Epoch         uint64 `json:"epoch"`
	EffectiveSlot uint64 `json:"effectiveSlot"`
	Amount        int64  `json:"amount"`
	PostBalance   uint64 `json:"postBalance"`
	Commission    *int   `json:"commission"`
}

type rpcGetEpochInfoResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Result  *rpcEpochInfo `json:"result"`
	Error   *rpcError     `json:"error"`
}

type rpcEpochInfo struct {
	Epoch uint64 `json:"epoch"`
}

func newRateLimitedHTTPClient(endpoint string) *http.Client {
	limiter := limiterForEndpoint(endpoint)
	transport := http.RoundTripper(http.DefaultTransport)
	if limiter != nil {
		transport = &RateLimitedTransport{
			Limiter: limiter,
			Base:    transport,
		}
	}
	return &http.Client{
		Timeout:   defaultHTTPTimeout,
		Transport: transport,
	}
}

func (c *RPCSolanaClient) doRPCRequest(ctx context.Context, payload []byte) (*http.Response, error) {
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient().Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			log.Printf("solana rpc 429 response headers: %v", resp.Header)
			if delay, ok := retryAfterDelay(resp.Header.Get("Retry-After")); ok {
				resp.Body.Close()
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, ctx.Err()
				case <-timer.C:
					continue
				}
			}
		}

		return resp, nil
	}
}

func retryAfterDelay(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}

	if secs, err := strconv.ParseFloat(value, 64); err == nil && secs >= 0 {
		return time.Duration(secs * float64(time.Second)), true
	}

	if when, err := http.ParseTime(value); err == nil {
		delay := max(time.Until(when), 0)
		return delay, true
	}

	return 0, false
}
