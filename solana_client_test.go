package gooctopi

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRPCSolanaClientGetBalance(t *testing.T) {
	t.Parallel()

	var capturedRequest rpcRequest

	client := &RPCSolanaClient{
		Endpoint: "http://solana.test",
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost {
					t.Fatalf("unexpected method: %s", req.Method)
				}

				defer req.Body.Close()
				var body rpcRequest
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatalf("failed to decode request: %v", err)
				}
				capturedRequest = body

				resp := rpcGetBalanceResponse{
					JSONRPC: "2.0",
					ID:      "1",
					Result: &rpcBalanceResult{
						Context: rpcContext{Slot: 123},
						Value:   789000000,
					},
				}

				var buf bytes.Buffer
				if err := json.NewEncoder(&buf).Encode(resp); err != nil {
					t.Fatalf("failed to encode response: %v", err)
				}

				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(buf.Bytes())),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}

	got, err := client.GetBalance(context.Background(), "4Nd1mYFHGQMiZ1ZkZZgwyUrKvYzUKGwEuUXXSb9Qe7CG")
	if err != nil {
		t.Fatalf("GetBalance returned error: %v", err)
	}

	if capturedRequest.Method != "getBalance" {
		t.Fatalf("unexpected rpc method %q", capturedRequest.Method)
	}

	if len(capturedRequest.Params) == 0 || capturedRequest.Params[0] != "4Nd1mYFHGQMiZ1ZkZZgwyUrKvYzUKGwEuUXXSb9Qe7CG" {
		t.Fatalf("unexpected params: %#v", capturedRequest.Params)
	}

	if got != 789000000 {
		t.Fatalf("unexpected balance: got %d want %d", got, 789000000)
	}
}

func TestRPCSolanaClientReturnsError(t *testing.T) {
	t.Parallel()

	client := &RPCSolanaClient{
		Endpoint: "http://solana.test",
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				resp := rpcGetBalanceResponse{
					JSONRPC: "2.0",
					ID:      "1",
					Error: &rpcError{
						Code:    -32602,
						Message: "invalid address",
					},
				}
				var buf bytes.Buffer
				if err := json.NewEncoder(&buf).Encode(resp); err != nil {
					t.Fatalf("failed to encode response: %v", err)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(buf.Bytes())),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}

	_, err := client.GetBalance(context.Background(), "bad")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRPCSolanaClientListStakeAccounts(t *testing.T) {
	t.Parallel()

	client := &RPCSolanaClient{
		Endpoint: "http://solana.test",
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				defer req.Body.Close()
				var payload rpcRequest
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Fatalf("failed to decode payload: %v", err)
				}
				if payload.Method != "getProgramAccounts" {
					t.Fatalf("unexpected method %s", payload.Method)
				}

				resp := `{
					"jsonrpc":"2.0",
					"id":"1",
					"result":[
						{
							"pubkey":"StakePub111",
							"account":{
								"data":{
									"parsed":{
										"type":"delegated",
										"info":{
											"meta":{
												"authorized":{
													"staker":"Owner111",
													"withdrawer":"Owner111"
												}
											},
											"stake":{
												"delegation":{
													"stake":"500000000",
													"voter":"Vote111111111111111111111111111111111111111"
												}
											}
										}
									}
								}
							}
						},
						{
							"pubkey":"FilteredStake",
							"account":{
								"data":{
									"parsed":{
										"type":"initialized",
										"info":{
											"meta":{
												"authorized":{
													"staker":"Other",
													"withdrawer":"Other"
												}
											},
											"stake":{
												"delegation":{
													"stake":"10000000",
													"voter":"Vote222"
												}
											}
										}
									}
								}
							}
						}
					]
				}`

				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBufferString(resp)),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}

	accounts, err := client.ListStakeAccounts(context.Background(), "Owner111")
	if err != nil {
		t.Fatalf("ListStakeAccounts returned error: %v", err)
	}

	if len(accounts) != 1 {
		t.Fatalf("expected 1 stake account, got %d", len(accounts))
	}

	if accounts[0].Address != "StakePub111" {
		t.Fatalf("unexpected stake address: %s", accounts[0].Address)
	}

	if accounts[0].DelegatedLamports != 500000000 {
		t.Fatalf("unexpected delegated lamports: %d", accounts[0].DelegatedLamports)
	}
}

func TestRPCSolanaClientGetTransactionParsesMeta(t *testing.T) {
	t.Parallel()

	client := &RPCSolanaClient{
		Endpoint: "http://solana.test",
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				payload := `{
					"jsonrpc":"2.0",
					"id":"1",
					"result":{
						"slot":123,
						"blockTime":1700000000,
						"meta":{
							"rewards":[
								{
									"pubkey":"Reward111",
									"lamports":100,
									"postBalance":200,
									"rewardType":"rent",
									"commission":null,
									"voteAccount":"VoteReward"
								}
							],
							"preBalances":[1000,500],
							"postBalances":[1500,500],
							"err":null,
							"innerInstructions":[
								{
									"index":0,
									"instructions":[
										{
											"programId":"Compute11111111111111111111111111111111",
											"accounts":["Withdraw111","Other111"],
											"data":"00",
											"program":"system",
											"stackHeight":1
										}
									]
								}
							]
						},
						"transaction":{
							"message":{
								"accountKeys":["Withdraw111","Other111"]
							}
						}
					}
				}`

				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(payload)),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}

	detail, err := client.GetTransaction(context.Background(), "sig1")
	if err != nil {
		t.Fatalf("GetTransaction returned error: %v", err)
	}

	if len(detail.AccountKeys) != 2 || detail.AccountKeys[0] != "Withdraw111" {
		t.Fatalf("unexpected account keys: %#v", detail.AccountKeys)
	}
	if len(detail.Meta.PreBalances) != 2 || detail.Meta.PreBalances[0] != 1000 {
		t.Fatalf("preBalances not parsed: %#v", detail.Meta.PreBalances)
	}
	if len(detail.Meta.PostBalances) != 2 || detail.Meta.PostBalances[0] != 1500 {
		t.Fatalf("postBalances not parsed: %#v", detail.Meta.PostBalances)
	}
	if len(detail.Meta.InnerInstructions) != 1 {
		t.Fatalf("missing inner instructions: %#v", detail.Meta.InnerInstructions)
	}
	inst := detail.Meta.InnerInstructions[0].Instructions
	if len(inst) != 1 || inst[0].ProgramID != "Compute11111111111111111111111111111111" {
		t.Fatalf("inner instruction not parsed: %#v", inst)
	}
	if inst[0].StackHeight == nil || *inst[0].StackHeight != 1 {
		t.Fatalf("stack height missing in instruction: %#v", inst[0])
	}
	if len(detail.Meta.Err) != 0 {
		t.Fatalf("expected empty error, got %q", string(detail.Meta.Err))
	}
}

func TestRPCSolanaClientGetSignaturesForAddress(t *testing.T) {
	t.Parallel()

	client := &RPCSolanaClient{
		Endpoint: "http://solana.test",
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				defer req.Body.Close()
				var payload rpcRequest
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Fatalf("failed to decode payload: %v", err)
				}
				params, _ := payload.Params[1].(map[string]interface{})
				if params["limit"].(float64) != 10 {
					t.Fatalf("expected limit 10, got %v", params["limit"])
				}
				if params["before"].(string) != "sig-before" {
					t.Fatalf("expected before signature, got %v", params["before"])
				}

				resp := `{
					"jsonrpc":"2.0",
					"id":"1",
					"result":[
						{"signature":"sig1","slot":123,"blockTime":1700000000},
						{"signature":"sig2","slot":124,"blockTime":null}
					]
				}`

				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBufferString(resp)),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}

	sigs, err := client.GetSignaturesForAddress(context.Background(), "StakePub111", 10, "sig-before")
	if err != nil {
		t.Fatalf("GetSignaturesForAddress returned error: %v", err)
	}

	if len(sigs) != 2 {
		t.Fatalf("expected 2 signatures, got %d", len(sigs))
	}
	if sigs[0].Signature != "sig1" || sigs[0].Slot != 123 {
		t.Fatalf("unexpected entry: %#v", sigs[0])
	}
	if sigs[1].BlockTime != nil {
		t.Fatalf("expected nil blockTime for second entry")
	}
}

func TestRPCSolanaClientGetTransaction(t *testing.T) {
	t.Parallel()

	client := &RPCSolanaClient{
		Endpoint: "http://solana.test",
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				defer req.Body.Close()

				resp := `{
					"jsonrpc":"2.0",
					"id":"1",
					"result":{
						"slot":567,
						"blockTime":1700000100,
						"meta":{
							"rewards":[
								{
									"pubkey":"StakePub111",
									"lamports":5000000,
									"postBalance":123450000,
									"rewardType":"Staking",
									"commission":5,
									"voteAccount":"Vote111"
								}
							]
						}
					}
				}`

				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBufferString(resp)),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}

	tx, err := client.GetTransaction(context.Background(), "sig1")
	if err != nil {
		t.Fatalf("GetTransaction returned error: %v", err)
	}

	if tx.Slot != 567 {
		t.Fatalf("unexpected slot: %d", tx.Slot)
	}
	if tx.BlockTime == nil || *tx.BlockTime != 1700000100 {
		t.Fatalf("unexpected block time: %v", tx.BlockTime)
	}
	if len(tx.Meta.Rewards) != 1 || tx.Meta.Rewards[0].Pubkey != "StakePub111" {
		t.Fatalf("unexpected rewards: %#v", tx.Meta.Rewards)
	}
}

func TestRPCSolanaClientGetInflationReward(t *testing.T) {
	t.Parallel()

	var captured rpcRequest
	client := &RPCSolanaClient{
		Endpoint: "http://solana.test",
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				defer req.Body.Close()
				if err := json.NewDecoder(req.Body).Decode(&captured); err != nil {
					t.Fatalf("failed to decode request: %v", err)
				}

				resp := `{
					"jsonrpc":"2.0",
					"id":"1",
					"result":[
						{
							"epoch":879,
							"effectiveSlot":379728159,
							"amount":491371,
							"postBalance":1457139076,
							"commission":0
						},
						null
					]
				}`

				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(resp)),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}

	targetEpoch := uint64(879)
	rewards, err := client.GetInflationReward(context.Background(), []string{"Stake111", "Stake222"}, &targetEpoch)
	if err != nil {
		t.Fatalf("GetInflationReward returned error: %v", err)
	}

	if captured.Method != "getInflationReward" {
		t.Fatalf("unexpected method %s", captured.Method)
	}

	if len(rewards) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(rewards))
	}
	if rewards[0] == nil || rewards[0].Amount != 491371 {
		t.Fatalf("unexpected reward entry %#v", rewards[0])
	}
	if rewards[1] != nil {
		t.Fatalf("expected nil reward for second address")
	}
}

func TestRPCSolanaClientGetEpochInfo(t *testing.T) {
	t.Parallel()

	client := &RPCSolanaClient{
		Endpoint: "http://solana.test",
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				defer req.Body.Close()

				resp := `{
					"jsonrpc":"2.0",
					"id":"1",
					"result":{
						"epoch":880,
						"absoluteSlot":123456,
						"slotIndex":456,
						"slotsInEpoch":432000
					}
				}`

				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(resp)),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}

	info, err := client.GetEpochInfo(context.Background())
	if err != nil {
		t.Fatalf("GetEpochInfo returned error: %v", err)
	}
	if info == nil || info.Epoch != 880 {
		t.Fatalf("unexpected epoch info %#v", info)
	}
	if info.AbsoluteSlot != 123456 || info.SlotIndex != 456 || info.SlotsInEpoch != 432000 {
		t.Fatalf("unexpected extended epoch data %#v", info)
	}
}

func TestRPCSolanaClientGetEpochBoundaries(t *testing.T) {
	t.Parallel()

	slotTimes := map[uint64]int64{
		9999: 1700000500,
		9899: 1700000000,
		9799: 1699999300,
	}
	var methods []string

	client := &RPCSolanaClient{
		Endpoint: "http://solana.test",
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				defer req.Body.Close()

				var captured rpcRequest
				if err := json.NewDecoder(req.Body).Decode(&captured); err != nil {
					return nil, err
				}
				methods = append(methods, captured.Method)

				switch captured.Method {
				case "getEpochInfo":
					resp := `{
						"jsonrpc":"2.0",
						"id":"1",
						"result":{
							"epoch":1000,
							"absoluteSlot":10050,
							"slotIndex":50,
							"slotsInEpoch":100
						}
					}`
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(resp)),
						Header:     make(http.Header),
					}, nil
				case "getBlockTime":
					slotVal, ok := captured.Params[0].(float64)
					if !ok {
						t.Fatalf("unexpected slot param %#v", captured.Params[0])
					}
					ts, ok := slotTimes[uint64(slotVal)]
					if !ok {
						t.Fatalf("unexpected block time lookup for slot %v", slotVal)
					}
					resp := fmt.Sprintf(`{
						"jsonrpc":"2.0",
						"id":"1",
						"result":%d
					}`, ts)
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(resp)),
						Header:     make(http.Header),
					}, nil
				default:
					t.Fatalf("unexpected RPC method %s", captured.Method)
				}
				return nil, nil
			}),
		},
	}

	minTime := time.Unix(1699999900, 0)
	entries, err := client.GetEpochBoundaries(context.Background(), minTime)
	if err != nil {
		t.Fatalf("GetEpochBoundaries returned error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 boundaries, got %d (%v)", len(entries), entries)
	}
	if entries[0].Epoch != 999 || entries[1].Epoch != 998 {
		t.Fatalf("unexpected epochs %+v", entries)
	}
	if got := entries[0].EndTime.Unix(); got != 1700000500 {
		t.Fatalf("unexpected first boundary time %d", got)
	}
	if got := entries[1].EndTime.Unix(); got != 1700000000 {
		t.Fatalf("unexpected second boundary time %d", got)
	}
	if len(methods) != 4 {
		t.Fatalf("unexpected RPC calls %v", methods)
	}
}

func TestRPCSolanaClientRetriesAfter429(t *testing.T) {
	var calls int

	client := &RPCSolanaClient{
		Endpoint: "http://solana.test",
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					resp := &http.Response{
						StatusCode: http.StatusTooManyRequests,
						Body:       io.NopCloser(strings.NewReader("limit")),
						Header:     make(http.Header),
					}
					resp.Header.Set("Retry-After", "0.01")
					resp.Header.Set("X-Debug", "first")
					return resp, nil
				}

				resp := rpcGetBalanceResponse{
					JSONRPC: "2.0",
					ID:      "1",
					Result: &rpcBalanceResult{
						Context: rpcContext{Slot: 999},
						Value:   42,
					},
				}
				var buf bytes.Buffer
				if err := json.NewEncoder(&buf).Encode(resp); err != nil {
					t.Fatalf("failed to encode response: %v", err)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(buf.Bytes())),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}

	start := time.Now()
	balance, err := client.GetBalance(context.Background(), "addr")
	if err != nil {
		t.Fatalf("GetBalance returned error: %v", err)
	}

	if balance != 42 {
		t.Fatalf("unexpected balance %d", balance)
	}

	elapsed := time.Since(start)
	if elapsed < 10*time.Millisecond {
		t.Fatalf("expected retry delay, elapsed %v", elapsed)
	}

	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
}

func TestRPCSolanaClientDoesNotRetryWithoutRetryAfter(t *testing.T) {
	var calls int

	client := &RPCSolanaClient{
		Endpoint: "http://solana.test",
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(strings.NewReader("limit")),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}

	_, err := client.GetBalance(context.Background(), "addr")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if calls != 1 {
		t.Fatalf("expected single request, got %d", calls)
	}
}

func TestEpochCacheLoadFromDisk(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "sol-epochs.gob")
	prevPath := epochCachePath
	epochCachePath = path
	defer func() { epochCachePath = prevPath }()

	wantCovered := time.Unix(1700000000, 0).UTC()
	wantExpires := time.Now().Add(30 * time.Minute).UTC()
	snapshot := epochCacheSnapshot{
		Boundaries: []EpochBoundary{
			{Epoch: 99, EndSlot: 12345, EndTime: wantCovered},
		},
		ExpiresAt:   wantExpires,
		CoveredFrom: wantCovered,
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(snapshot); err != nil {
		t.Fatalf("failed to encode snapshot: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("failed to write snapshot: %v", err)
	}

	cache := &epochCache{}
	cache.loadFromDisk()

	if len(cache.boundaries) != 1 {
		t.Fatalf("expected 1 boundary, got %d", len(cache.boundaries))
	}
	if cache.boundaries[0].Epoch != 99 || cache.boundaries[0].EndSlot != 12345 {
		t.Fatalf("unexpected boundary %+v", cache.boundaries[0])
	}
	if !cache.coveredFrom.Equal(wantCovered) {
		t.Fatalf("unexpected coveredFrom %s", cache.coveredFrom)
	}
	if !cache.expiresAt.Equal(wantExpires) {
		t.Fatalf("unexpected expiresAt %s want %s", cache.expiresAt, wantExpires)
	}
}

func TestEpochCachePersistLockedWritesFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "sol-epochs.gob")
	prevPath := epochCachePath
	epochCachePath = path
	defer func() { epochCachePath = prevPath }()

	boundary := EpochBoundary{
		Epoch:   77,
		EndSlot: 999,
		EndTime: time.Unix(1700001000, 0).UTC(),
	}
	expiry := time.Now().Add(time.Hour).UTC()

	cache := &epochCache{
		boundaries:  []EpochBoundary{boundary},
		expiresAt:   expiry,
		coveredFrom: boundary.EndTime,
	}
	cache.persistLocked()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read snapshot: %v", err)
	}
	var snapshot epochCacheSnapshot
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&snapshot); err != nil {
		t.Fatalf("failed to decode snapshot: %v", err)
	}
	if len(snapshot.Boundaries) != 1 {
		t.Fatalf("expected 1 boundary, got %d", len(snapshot.Boundaries))
	}
	if snapshot.Boundaries[0].Epoch != boundary.Epoch || snapshot.Boundaries[0].EndSlot != boundary.EndSlot {
		t.Fatalf("mismatched boundary %+v", snapshot.Boundaries[0])
	}
	if !snapshot.ExpiresAt.Equal(expiry) {
		t.Fatalf("unexpected expiresAt %s want %s", snapshot.ExpiresAt, expiry)
	}
	if !snapshot.CoveredFrom.Equal(boundary.EndTime) {
		t.Fatalf("unexpected coveredFrom %s want %s", snapshot.CoveredFrom, boundary.EndTime)
	}
}

func TestRPCSolanaClientGetSOLPriceFetchesFromValidators(t *testing.T) {
	t.Parallel()

	repo := newSOLPriceRepository("token-123", newTestLogger())
	repo.now = func() time.Time {
		return time.Date(2025, time.November, 16, 9, 30, 0, 0, time.UTC)
	}

	var requestedURL string
	var token string
	repo.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requestedURL = req.URL.String()
			token = req.Header.Get("Token")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`[{"average_price":"139.00"}]`)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	client := &RPCSolanaClient{
		priceRepo: repo,
	}

	price, err := client.GetSOLPrice(context.Background())
	if err != nil {
		t.Fatalf("GetSOLPrice returned error: %v", err)
	}
	if math.Abs(price-139.0) > 1e-9 {
		t.Fatalf("unexpected price %.2f", price)
	}

	if token != "token-123" {
		t.Fatalf("expected validators token header, got %q", token)
	}
	if !strings.Contains(requestedURL, "from=2025-11-15T00:00:00") || !strings.Contains(requestedURL, "to=2025-11-16T00:00:00") {
		t.Fatalf("unexpected url %q", requestedURL)
	}
}

func TestRPCSolanaClientGetSOLPriceUsesCache(t *testing.T) {
	t.Parallel()

	repo := newSOLPriceRepository("token-123", newTestLogger())
	repo.now = func() time.Time {
		return time.Date(2025, time.November, 16, 0, 0, 0, 0, time.UTC)
	}

	var calls int
	repo.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`[{"average_price":"140.50"}]`)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	client := &RPCSolanaClient{
		priceRepo: repo,
	}

	if _, err := client.GetSOLPrice(context.Background()); err != nil {
		t.Fatalf("first GetSOLPrice call returned error: %v", err)
	}
	if _, err := client.GetSOLPrice(context.Background()); err != nil {
		t.Fatalf("second GetSOLPrice call returned error: %v", err)
	}

	if calls != 1 {
		t.Fatalf("expected single HTTP call, got %d", calls)
	}
}

func TestRPCSolanaClientGetSOLPriceErrorsWithoutCache(t *testing.T) {
	t.Parallel()

	repo := newSOLPriceRepository("token-123", newTestLogger())
	repo.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("boom")
		}),
	}

	client := &RPCSolanaClient{
		priceRepo: repo,
	}

	if _, err := client.GetSOLPrice(context.Background()); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestValidatorRepositoryRecordXIRRAndRecent(t *testing.T) {
	repo := newValidatorRepository("token", nil, nil)
	if repo == nil {
		t.Fatalf("expected repository")
	}
	base := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return base }

	repo.recordXIRR("Vote111", 5.5)
	repo.store("Vote111", "Atlas Nodes")

	stats := repo.recentValidators(5)
	if len(stats) != 1 {
		t.Fatalf("expected single entry, got %d", len(stats))
	}
	entry := stats[0]
	if entry.VoteAccount != "Vote111" {
		t.Fatalf("unexpected vote account: %s", entry.VoteAccount)
	}
	if entry.Name != "Atlas Nodes" {
		t.Fatalf("unexpected name: %s", entry.Name)
	}
	if math.Abs(entry.XIRR-5.5) > 1e-9 {
		t.Fatalf("unexpected xirr: %.2f", entry.XIRR)
	}
	if !entry.UpdatedAt.Equal(base) {
		t.Fatalf("unexpected updated time: %s", entry.UpdatedAt)
	}
}

func TestValidatorRepositoryRecentRespectsTTL(t *testing.T) {
	repo := newValidatorRepository("token", nil, nil)
	if repo == nil {
		t.Fatalf("expected repository")
	}
	base := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return base }
	repo.recordXIRR("VoteA", 4.2)
	repo.store("VoteA", "Alpha")

	repo.now = func() time.Time { return base.Add(time.Hour) }
	repo.recordXIRR("VoteB", 6.1)
	repo.store("VoteB", "Beta")

	stats := repo.recentValidators(1)
	if len(stats) != 1 || stats[0].VoteAccount != "VoteB" {
		t.Fatalf("expected newest entry first, got %#v", stats)
	}

	repo.now = func() time.Time { return base.Add(repo.ttl + 2*time.Hour) }
	stats = repo.recentValidators(5)
	if len(stats) != 0 {
		t.Fatalf("expected entries to expire, got %#v", stats)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
