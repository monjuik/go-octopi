package gooctopi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	solanaMainnetHost      = "mainnet.helius-rpc.com"
	defaultHTTPTimeout     = 15 * time.Second
	stakeProgramID         = "Stake11111111111111111111111111111111111111"
	approxSlotDuration     = 400 * time.Millisecond
	maxTransactionBatchLen = 10
)

var (
	solanaRPCLogger = NewLogger("solana-rpc")
	epochLog        = NewLogger("epoch-cache")
)

// ErrEventsUnsupported indicates that the RPC endpoint does not implement getEvents.
var ErrEventsUnsupported = errors.New("solana getEvents not supported on this endpoint")

// SolanaClient defines the operations needed by the HTTP layer.
type SolanaClient interface {
	GetBalance(ctx context.Context, address string) (uint64, error)
	ListStakeAccounts(ctx context.Context, owner string) ([]StakeAccount, error)
	GetSignaturesForAddress(ctx context.Context, address string, limit int, before string) ([]SignatureInfo, error)
	GetTransactions(ctx context.Context, signatures []string) (map[string]*TransactionDetail, error)
	GetTransaction(ctx context.Context, signature string) (*TransactionDetail, error)
	GetInflationReward(ctx context.Context, addresses []string, epoch *uint64) ([]*InflationReward, error)
	GetEpochInfo(ctx context.Context) (*EpochInfo, error)
	GetEpochBoundaries(ctx context.Context, minEndTime time.Time) ([]EpochBoundary, error)
	GetEvents(ctx context.Context, req GetEventsRequest) (*EventsPage, error)
}

// RPCSolanaClient calls the public Solana JSON-RPC endpoint.
type RPCSolanaClient struct {
	Endpoint   string
	HTTPClient *http.Client
	epochCache *epochCache
}

// StakeAccount carries parsed information about a delegated stake account.
type StakeAccount struct {
	Address           string
	DelegatedLamports uint64
	State             string
	VoteAccount       string
	WithdrawAuthority string
}

// SignatureInfo represents a transaction signature reference for an address.
type SignatureInfo struct {
	Signature string
	Slot      uint64
	BlockTime *int64
}

// TransactionDetail contains the subset of fields we care about from getTransaction.
type TransactionDetail struct {
	Slot         uint64
	BlockTime    *int64
	Meta         TransactionMeta
	AccountKeys  []string
	Instructions []TransactionInstruction
}

// TransactionMeta captures the reward entries emitted for a transaction.
type TransactionMeta struct {
	Rewards           []TransactionReward
	PreBalances       []uint64
	PostBalances      []uint64
	Err               json.RawMessage
	InnerInstructions []InnerInstruction
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

// InnerInstruction describes a single inner instruction entry.
type InnerInstruction struct {
	Index        int
	Instructions []TransactionInstruction
}

// TransactionInstruction captures minimal instruction metadata.
type TransactionInstruction struct {
	ProgramID   string
	Accounts    []string
	Data        string
	Program     string
	StackHeight *int
}

// GetEventsRequest configures the Helius getEvents RPC call.
type GetEventsRequest struct {
	Query      map[string]any
	Before     string
	Limit      int
	StartSlot  *uint64
	EndSlot    *uint64
	Commitment string
}

// EventsPage carries the events returned by getEvents.
type EventsPage struct {
	Events          []Event
	PaginationToken string
}

// Event represents a single entry returned from getEvents.
type Event struct {
	Signature       string
	Slot            uint64
	Timestamp       time.Time
	Type            string
	ProgramID       string
	Accounts        []string
	TipDistribution *TipDistributionEvent
}

// TipDistributionEvent models parsed information specific to Jito claims.
type TipDistributionEvent struct {
	Validator string
	Epoch     uint64
	Recipient string
	Amount    uint64
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
	Epoch        uint64
	AbsoluteSlot uint64
	SlotIndex    uint64
	SlotsInEpoch uint64
}

// EpochBoundary describes when an epoch completed.
type EpochBoundary struct {
	Epoch   uint64
	EndSlot uint64
	EndTime time.Time
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
			WithdrawAuthority: acct.Account.Data.Parsed.Info.Meta.Authorized.Withdrawer,
		})
	}

	return accounts, nil
}

// GetSignaturesForAddress returns the most recent signatures touching the address.
func (c *RPCSolanaClient) GetSignaturesForAddress(ctx context.Context, address string, limit int, before string) ([]SignatureInfo, error) {
	if limit <= 0 {
		limit = 1
	}

	solanaRPCLogger.Printf("getSignaturesForAddress address=%s limit=%d before=%s", address, limit, before)

	payload := rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "getSignaturesForAddress",
		Params: []any{
			address,
			func() map[string]any {
				config := map[string]any{
					"limit":      limit,
					"commitment": "confirmed",
				}
				if before != "" {
					config["before"] = before
				}
				return config
			}(),
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

	detail := convertTransactionResult(rpcResp.Result)
	if detail == nil {
		return nil, fmt.Errorf("transaction details missing")
	}
	return detail, nil
}

func convertTransactionResult(result *rpcTransactionResult) *TransactionDetail {
	if result == nil {
		return nil
	}

	var accountKeys []string
	if result.Transaction != nil {
		accountKeys = append(accountKeys, result.Transaction.Message.AccountKeys...)
	}
	if result.Meta != nil && result.Meta.LoadedAddresses != nil {
		if len(result.Meta.LoadedAddresses.Writable) > 0 {
			accountKeys = append(accountKeys, result.Meta.LoadedAddresses.Writable...)
		}
		if len(result.Meta.LoadedAddresses.Readonly) > 0 {
			accountKeys = append(accountKeys, result.Meta.LoadedAddresses.Readonly...)
		}
	}

	var instructions []TransactionInstruction
	if result.Transaction != nil && len(result.Transaction.Message.Instructions) > 0 {
		instructions = make([]TransactionInstruction, 0, len(result.Transaction.Message.Instructions))
		for _, inst := range result.Transaction.Message.Instructions {
			programID := resolveProgramID(inst.ProgramID, inst.ProgramIDIndex, accountKeys)
			resolvedAccounts := resolveAccounts(inst.Accounts, accountKeys)
			instructions = append(instructions, TransactionInstruction{
				ProgramID: programID,
				Accounts:  resolvedAccounts,
				Data:      inst.Data,
			})
		}
	}

	meta := TransactionMeta{}
	if result.Meta != nil {
		if len(result.Meta.Rewards) > 0 {
			meta.Rewards = make([]TransactionReward, 0, len(result.Meta.Rewards))
			for _, reward := range result.Meta.Rewards {
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
		if len(result.Meta.PreBalances) > 0 {
			meta.PreBalances = append(meta.PreBalances, result.Meta.PreBalances...)
		}
		if len(result.Meta.PostBalances) > 0 {
			meta.PostBalances = append(meta.PostBalances, result.Meta.PostBalances...)
		}
		if len(result.Meta.Err) > 0 {
			trimmed := bytes.TrimSpace(result.Meta.Err)
			if !bytes.Equal(trimmed, []byte("null")) {
				meta.Err = append([]byte(nil), trimmed...)
			}
		}
		if len(result.Meta.InnerInstructions) > 0 {
			meta.InnerInstructions = make([]InnerInstruction, 0, len(result.Meta.InnerInstructions))
			for _, inner := range result.Meta.InnerInstructions {
				entry := InnerInstruction{
					Index: int(inner.Index),
				}
				if len(inner.Instructions) > 0 {
					entry.Instructions = make([]TransactionInstruction, 0, len(inner.Instructions))
					for _, inst := range inner.Instructions {
						var stackHeight *int
						if inst.StackHeight != nil {
							value := int(*inst.StackHeight)
							stackHeight = &value
						}
						entry.Instructions = append(entry.Instructions, TransactionInstruction{
							ProgramID:   resolveProgramID(inst.ProgramID, inst.ProgramIDIndex, accountKeys),
							Accounts:    resolveAccounts(inst.Accounts, accountKeys),
							Data:        inst.Data,
							Program:     inst.Program,
							StackHeight: stackHeight,
						})
					}
				}
				meta.InnerInstructions = append(meta.InnerInstructions, entry)
			}
		}
	}

	return &TransactionDetail{
		Slot:         result.Slot,
		BlockTime:    result.BlockTime,
		Meta:         meta,
		AccountKeys:  accountKeys,
		Instructions: instructions,
	}
}

func resolveProgramID(explicit string, index *uint16, accountKeys []string) string {
	if explicit != "" {
		return explicit
	}
	if index != nil && int(*index) < len(accountKeys) {
		return accountKeys[*index]
	}
	return ""
}

func resolveAccounts(refs []rpcAccountReference, accountKeys []string) []string {
	if len(refs) == 0 {
		return nil
	}
	resolved := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.Address != "" {
			resolved = append(resolved, ref.Address)
			continue
		}
		if ref.Index != nil && int(*ref.Index) < len(accountKeys) {
			resolved = append(resolved, accountKeys[*ref.Index])
		}
	}
	return resolved
}

// GetEvents invokes the Helius enhanced getEvents RPC.
func (c *RPCSolanaClient) GetEvents(ctx context.Context, req GetEventsRequest) (*EventsPage, error) {
	query := map[string]any{}
	if req.Query != nil {
		for k, v := range req.Query {
			query[k] = v
		}
	}

	config := map[string]any{
		"query":      query,
		"commitment": "confirmed",
	}

	if req.Commitment != "" {
		config["commitment"] = req.Commitment
	}
	if req.Limit > 0 {
		config["limit"] = req.Limit
	}
	if req.Before != "" {
		config["before"] = req.Before
	}
	if req.StartSlot != nil {
		config["startSlot"] = *req.StartSlot
	}
	if req.EndSlot != nil {
		config["endSlot"] = *req.EndSlot
	}

	payload := rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "getEvents",
		Params:  []any{config},
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return nil, fmt.Errorf("encode events request: %w", err)
	}

	resp, err := c.doRPCRequest(ctx, buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("rpc request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		if resp.StatusCode == http.StatusNotFound && containsMethodNotFound(body) {
			return nil, ErrEventsUnsupported
		}
		return nil, fmt.Errorf("rpc status %d: %s", resp.StatusCode, string(body))
	}

	var rpcResp rpcGetEventsResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("decode events response: %w", err)
	}
	if rpcResp.Error != nil {
		if isMethodNotFoundError(rpcResp.Error) {
			return nil, ErrEventsUnsupported
		}
		return nil, fmt.Errorf("rpc error (%d): %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if rpcResp.Result == nil {
		return nil, fmt.Errorf("events response missing result")
	}

	page := &EventsPage{
		PaginationToken: rpcResp.Result.PaginationToken,
	}
	if len(rpcResp.Result.Events) > 0 {
		page.Events = make([]Event, 0, len(rpcResp.Result.Events))
		for _, evt := range rpcResp.Result.Events {
			page.Events = append(page.Events, convertRPCEvent(evt))
		}
	}
	return page, nil
}

func convertRPCEvent(evt rpcEvent) Event {
	event := Event{
		Signature: evt.Signature,
		Slot:      evt.Slot,
		Type:      evt.Type,
		ProgramID: evt.ProgramID,
	}
	if len(evt.Accounts) > 0 {
		event.Accounts = append(event.Accounts, evt.Accounts...)
	}
	event.Timestamp = parseEventTimestamp(evt.Timestamp)

	if evt.TipDistribution != nil {
		copyTip := *evt.TipDistribution
		event.TipDistribution = &copyTip
	}

	if event.TipDistribution == nil && evt.Parsed != nil {
		if td := decodeTipDistribution(evt.Parsed.Info); td != nil {
			event.TipDistribution = td
		}
	}

	return event
}

func parseEventTimestamp(raw json.RawMessage) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		asString = strings.TrimSpace(asString)
		if asString == "" {
			return time.Time{}
		}
		if ts, err := time.Parse(time.RFC3339, asString); err == nil {
			return ts.UTC()
		}
	}

	var asInt64 int64
	if err := json.Unmarshal(raw, &asInt64); err == nil && asInt64 > 0 {
		return time.Unix(asInt64, 0).UTC()
	}

	var asFloat float64
	if err := json.Unmarshal(raw, &asFloat); err == nil && asFloat > 0 {
		return time.Unix(int64(asFloat), 0).UTC()
	}

	return time.Time{}
}

func decodeTipDistribution(raw json.RawMessage) *TipDistributionEvent {
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil
	}
	var payload tipDistributionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}

	var epoch uint64
	if payload.Epoch != "" {
		if value, err := jsonNumberToUint64(payload.Epoch); err == nil {
			epoch = value
		}
	}

	var amount uint64
	if payload.Amount != "" {
		if value, err := jsonNumberToUint64(payload.Amount); err == nil {
			amount = value
		}
	}

	return &TipDistributionEvent{
		Validator: payload.Validator,
		Epoch:     epoch,
		Recipient: payload.Recipient,
		Amount:    amount,
	}
}

func containsMethodNotFound(body []byte) bool {
	lower := bytes.ToLower(bytes.TrimSpace(body))
	return bytes.Contains(lower, []byte("method not found"))
}

func isMethodNotFoundError(err *rpcError) bool {
	if err == nil {
		return false
	}
	if err.Code == -32601 {
		return true
	}
	return strings.Contains(strings.ToLower(err.Message), "method not found")
}

// GetTransactions fetches multiple parsed transactions via a single RPC batch request.
func (c *RPCSolanaClient) GetTransactions(ctx context.Context, signatures []string) (map[string]*TransactionDetail, error) {
	if len(signatures) == 0 {
		return nil, nil
	}

	results := make(map[string]*TransactionDetail, len(signatures))
	signatureBatches := batchSignatures(signatures, maxTransactionBatchLen)
	for _, batch := range signatureBatches {
		batchResults, err := c.fetchTransactionBatch(ctx, batch)
		if err != nil {
			return nil, err
		}
		for sig, detail := range batchResults {
			results[sig] = detail
		}
	}
	return results, nil
}

func (c *RPCSolanaClient) fetchTransactionBatch(ctx context.Context, signatures []string) (map[string]*TransactionDetail, error) {
	solanaRPCLogger.Printf("getTransactions batch size=%d signatures=%s", len(signatures), strings.Join(signatures, ","))
	type lookup struct {
		id        int
		signature string
	}

	seen := make(map[string]struct{}, len(signatures))
	requests := make([]rpcRequest, 0, len(signatures))
	lookups := make([]lookup, 0, len(signatures))
	nextID := 1
	for _, sig := range signatures {
		if sig == "" {
			continue
		}
		if _, ok := seen[sig]; ok {
			continue
		}
		seen[sig] = struct{}{}
		requests = append(requests, rpcRequest{
			JSONRPC: "2.0",
			ID:      nextID,
			Method:  "getTransaction",
			Params: []any{
				sig,
				map[string]any{
					"encoding":                       "json",
					"commitment":                     "confirmed",
					"maxSupportedTransactionVersion": 0,
				},
			},
		})
		lookups = append(lookups, lookup{id: nextID, signature: sig})
		nextID++
	}
	if len(requests) == 0 {
		return nil, nil
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(requests); err != nil {
		return nil, fmt.Errorf("encode batch transaction request: %w", err)
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

	var rpcResp []rpcGetTransactionResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("decode batch transaction response: %w", err)
	}

	idToSignature := make(map[int]string, len(lookups))
	for _, l := range lookups {
		idToSignature[l.id] = l.signature
	}

	results := make(map[string]*TransactionDetail, len(rpcResp))
	for _, item := range rpcResp {
		if item.Error != nil {
			return nil, fmt.Errorf("rpc error (%d): %s", item.Error.Code, item.Error.Message)
		}
		sig := idToSignature[item.ID]
		if sig == "" {
			continue
		}
		if item.Result == nil {
			continue
		}
		if detail := convertTransactionResult(item.Result); detail != nil {
			results[sig] = detail
		}
	}
	return results, nil
}

func batchSignatures(signatures []string, size int) [][]string {
	if size <= 0 {
		size = len(signatures)
	}
	batches := make([][]string, 0, (len(signatures)+size-1)/size)
	for start := 0; start < len(signatures); start += size {
		end := start + size
		if end > len(signatures) {
			end = len(signatures)
		}
		batches = append(batches, signatures[start:end])
	}
	return batches
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
		Epoch:        rpcResp.Result.Epoch,
		AbsoluteSlot: rpcResp.Result.AbsoluteSlot,
		SlotIndex:    rpcResp.Result.SlotIndex,
		SlotsInEpoch: rpcResp.Result.SlotsInEpoch,
	}, nil
}

// GetEpochBoundaries returns metadata about completed epochs covering the provided lookback window.
func (c *RPCSolanaClient) GetEpochBoundaries(ctx context.Context, minEndTime time.Time) ([]EpochBoundary, error) {
	cache := c.ensureEpochCache()
	entries, err := cache.getBoundaries(ctx, minEndTime)
	if err != nil {
		return nil, err
	}
	result := make([]EpochBoundary, len(entries))
	copy(result, entries)
	return result, nil
}

func (c *RPCSolanaClient) getBlockTime(ctx context.Context, slot uint64) (time.Time, error) {
	payload := rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "getBlockTime",
		Params:  []any{slot},
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return time.Time{}, fmt.Errorf("encode block time request: %w", err)
	}

	resp, err := c.doRPCRequest(ctx, buf.Bytes())
	if err != nil {
		return time.Time{}, fmt.Errorf("rpc request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return time.Time{}, fmt.Errorf("rpc status %d: %s", resp.StatusCode, string(body))
	}

	var rpcResp rpcGetBlockTimeResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return time.Time{}, fmt.Errorf("decode block time response: %w", err)
	}
	if rpcResp.Error != nil {
		return time.Time{}, fmt.Errorf("rpc error (%d): %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if rpcResp.Result == nil {
		return time.Time{}, fmt.Errorf("block time result missing")
	}

	return time.Unix(*rpcResp.Result, 0).UTC(), nil
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
	Slot        uint64              `json:"slot"`
	BlockTime   *int64              `json:"blockTime"`
	Meta        *rpcTransactionMeta `json:"meta"`
	Transaction *rpcTransactionData `json:"transaction"`
}

type rpcTransactionData struct {
	Message rpcTransactionMessage `json:"message"`
}

type rpcTransactionMessage struct {
	AccountKeys  []string                `json:"accountKeys"`
	Instructions []rpcMessageInstruction `json:"instructions"`
}

type rpcMessageInstruction struct {
	ProgramIDIndex *uint16               `json:"programIdIndex"`
	ProgramID      string                `json:"programId"`
	Accounts       []rpcAccountReference `json:"accounts"`
	Data           string                `json:"data"`
}

type rpcTransactionMeta struct {
	Rewards           []rpcTransactionReward `json:"rewards"`
	PreBalances       []uint64               `json:"preBalances"`
	PostBalances      []uint64               `json:"postBalances"`
	Err               json.RawMessage        `json:"err"`
	InnerInstructions []rpcInnerInstruction  `json:"innerInstructions"`
	LoadedAddresses   *rpcLoadedAddresses    `json:"loadedAddresses"`
}

type rpcTransactionReward struct {
	Pubkey      string `json:"pubkey"`
	Lamports    int64  `json:"lamports"`
	PostBalance uint64 `json:"postBalance"`
	RewardType  string `json:"rewardType"`
	Commission  *int   `json:"commission"`
	VoteAccount string `json:"voteAccount"`
}

type rpcInnerInstruction struct {
	Index        uint64           `json:"index"`
	Instructions []rpcInstruction `json:"instructions"`
}

type rpcInstruction struct {
	ProgramIDIndex *uint16               `json:"programIdIndex"`
	ProgramID      string                `json:"programId"`
	Accounts       []rpcAccountReference `json:"accounts"`
	Data           string                `json:"data"`
	Program        string                `json:"program"`
	StackHeight    *int                  `json:"stackHeight"`
}

type rpcAccountReference struct {
	Index   *uint16
	Address string
}

type rpcLoadedAddresses struct {
	Writable []string `json:"writable"`
	Readonly []string `json:"readonly"`
}

func (r *rpcAccountReference) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil
	}
	if data[0] == '"' {
		var addr string
		if err := json.Unmarshal(data, &addr); err != nil {
			return err
		}
		r.Address = addr
		r.Index = nil
		return nil
	}
	var idx uint16
	if err := json.Unmarshal(data, &idx); err != nil {
		return err
	}
	r.Index = &idx
	r.Address = ""
	return nil
}

type rpcGetTransactionsForAddressResponse struct {
	JSONRPC string                  `json:"jsonrpc"`
	ID      int                     `json:"id"`
	Result  []rpcAddressTransaction `json:"result"`
	Error   *rpcError               `json:"error"`
}

type rpcAddressTransaction struct {
	Signature   string              `json:"signature"`
	Slot        uint64              `json:"slot"`
	BlockTime   *int64              `json:"blockTime"`
	Meta        *rpcTransactionMeta `json:"meta"`
	Transaction *rpcTransactionData `json:"transaction"`
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

type rpcGetBlockTimeResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      int       `json:"id"`
	Result  *int64    `json:"result"`
	Error   *rpcError `json:"error"`
}

type rpcGetEpochInfoResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Result  *rpcEpochInfo `json:"result"`
	Error   *rpcError     `json:"error"`
}

type rpcEpochInfo struct {
	Epoch        uint64 `json:"epoch"`
	AbsoluteSlot uint64 `json:"absoluteSlot"`
	SlotIndex    uint64 `json:"slotIndex"`
	SlotsInEpoch uint64 `json:"slotsInEpoch"`
}

type rpcGetEventsResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      int              `json:"id"`
	Result  *rpcEventsResult `json:"result"`
	Error   *rpcError        `json:"error"`
}

type rpcEventsResult struct {
	Events          []rpcEvent `json:"events"`
	PaginationToken string     `json:"paginationToken"`
}

type rpcEvent struct {
	Signature       string                `json:"signature"`
	Slot            uint64                `json:"slot"`
	Timestamp       json.RawMessage       `json:"timestamp"`
	Type            string                `json:"type"`
	ProgramID       string                `json:"programId"`
	Accounts        []string              `json:"accounts"`
	TipDistribution *TipDistributionEvent `json:"tipDistribution"`
	Parsed          *rpcParsedEvent       `json:"parsed"`
	Description     string                `json:"description"`
}

type rpcParsedEvent struct {
	Type string          `json:"type"`
	Info json.RawMessage `json:"info"`
}

type tipDistributionPayload struct {
	Validator string      `json:"validator"`
	Epoch     json.Number `json:"epoch"`
	Recipient string      `json:"recipient"`
	Amount    json.Number `json:"amount"`
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
			solanaRPCLogger.Printf("429 response headers: %v", resp.Header)
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

func (c *RPCSolanaClient) ensureEpochCache() *epochCache {
	if c.epochCache != nil {
		return c.epochCache
	}
	c.epochCache = &epochCache{client: c}
	return c.epochCache
}

type epochCache struct {
	client      *RPCSolanaClient
	mu          sync.Mutex
	boundaries  []EpochBoundary
	expiresAt   time.Time
	coveredFrom time.Time
}

func (c *epochCache) getBoundaries(ctx context.Context, minEndTime time.Time) ([]EpochBoundary, error) {
	minEndTime = minEndTime.UTC()

	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	needsRefresh := len(c.boundaries) == 0 || now.After(c.expiresAt) || (!c.coveredFrom.IsZero() && minEndTime.Before(c.coveredFrom))

	if needsRefresh {
		if err := c.refreshLocked(ctx, minEndTime); err != nil {
			return nil, err
		}
		epochLog.Printf("refresh windowStart=%s epochs=%d expiresAt=%s", minEndTime.Format(time.RFC3339), len(c.boundaries), c.expiresAt.Format(time.RFC3339))
	} else {
		epochLog.Printf("cache hit windowStart=%s epochs=%d expiresAt=%s", minEndTime.Format(time.RFC3339), len(c.boundaries), c.expiresAt.Format(time.RFC3339))
	}

	results := make([]EpochBoundary, 0, len(c.boundaries))
	for _, entry := range c.boundaries {
		if entry.EndTime.Before(minEndTime) {
			continue
		}
		results = append(results, entry)
	}
	return results, nil
}

func (c *epochCache) refreshLocked(ctx context.Context, minEndTime time.Time) error {
	epochLog.Printf("requesting epoch info via RPC")
	info, err := c.client.GetEpochInfo(ctx)
	if err != nil {
		return fmt.Errorf("epoch info: %w", err)
	}
	if info == nil {
		return fmt.Errorf("epoch info: empty response")
	}
	if info.SlotsInEpoch == 0 {
		return fmt.Errorf("epoch info: slotsInEpoch is zero")
	}
	if info.SlotIndex > info.SlotsInEpoch {
		return fmt.Errorf("epoch info: slotIndex %d exceeds %d", info.SlotIndex, info.SlotsInEpoch)
	}
	if info.AbsoluteSlot < info.SlotIndex {
		return fmt.Errorf("epoch info: absoluteSlot %d less than slotIndex %d", info.AbsoluteSlot, info.SlotIndex)
	}

	currentEpochStartSlot := info.AbsoluteSlot - info.SlotIndex
	if currentEpochStartSlot == 0 {
		return fmt.Errorf("epoch info: current epoch start slot is zero")
	}
	if info.Epoch == 0 {
		return fmt.Errorf("epoch info: current epoch is zero")
	}

	slot := currentEpochStartSlot - 1
	epoch := info.Epoch - 1
	epochLog.Printf("rebuilding epochs from epoch=%d slot=%d minWindow=%s", epoch, slot, minEndTime.Format(time.RFC3339))

	boundaries := make([]EpochBoundary, 0, 64)
	for {
		epochLog.Printf("fetching block time via RPC epoch=%d slot=%d", epoch, slot)
		blockTime, err := c.client.getBlockTime(ctx, slot)
		if err != nil {
			return fmt.Errorf("block time slot %d: %w", slot, err)
		}

		entry := EpochBoundary{
			Epoch:   epoch,
			EndSlot: slot,
			EndTime: blockTime,
		}
		boundaries = append(boundaries, entry)

		if blockTime.Before(minEndTime) {
			epochLog.Printf("reached window limit epoch=%d slot=%d ts=%s", epoch, slot, blockTime.Format(time.RFC3339))
			break
		}
		if epoch == 0 {
			break
		}

		if slot < info.SlotsInEpoch {
			epochLog.Printf("reached genesis boundary epoch=%d slot=%d", epoch, slot)
			break
		}
		slot -= info.SlotsInEpoch
		epoch--
	}

	c.boundaries = boundaries
	if len(boundaries) > 0 {
		c.coveredFrom = boundaries[len(boundaries)-1].EndTime
	} else {
		c.coveredFrom = time.Time{}
	}
	c.expiresAt = estimateEpochExpiry(info)
	return nil
}

func estimateEpochExpiry(info *EpochInfo) time.Time {
	if info == nil || info.SlotsInEpoch == 0 || info.SlotIndex >= info.SlotsInEpoch {
		return time.Now().Add(5 * time.Minute)
	}
	slotsRemaining := info.SlotsInEpoch - info.SlotIndex
	return time.Now().Add(time.Duration(slotsRemaining) * approxSlotDuration)
}
