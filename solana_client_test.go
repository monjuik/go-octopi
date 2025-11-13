package gooctopi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
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
					ID:      1,
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
					ID:      1,
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
					"id":1,
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

				resp := `{
					"jsonrpc":"2.0",
					"id":1,
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

	sigs, err := client.GetSignaturesForAddress(context.Background(), "StakePub111", 10)
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
					"id":1,
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
					"id":1,
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
					"id":1,
					"result":{
						"epoch":880
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
					ID:      1,
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
