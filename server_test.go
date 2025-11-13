package gooctopi

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerIndexRendering(t *testing.T) {
	ensureHeliusEnv(t)

	handler := NewServer()

	tests := []struct {
		name   string
		assert func(t *testing.T, body string)
	}{
		{
			name: "contains OctoPi",
			assert: func(t *testing.T, body string) {
				if !strings.Contains(body, "OctoPi") {
					t.Fatalf("body missing OctoPi: %q", body)
				}
			},
		},
		{
			name: "contains embedded logo",
			assert: func(t *testing.T, body string) {
				const prefix = `src="data:image/png;base64,`
				if !strings.Contains(body, prefix) {
					t.Fatalf("response missing embedded logo (looking for %q)", prefix)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := performRequest(t, handler, http.MethodGet, "/")
			if rec.Code != http.StatusOK {
				t.Fatalf("unexpected status: got %d, want %d", rec.Code, http.StatusOK)
			}
			tt.assert(t, rec.Body.String())
		})
	}
}

func TestWalletPageRendering(t *testing.T) {
	ensureHeliusEnv(t)

	rec := performRequest(t, NewServer(), http.MethodGet, "/wallet/demo")
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d, want %d", rec.Code, http.StatusOK)
	}

	if !strings.Contains(rec.Body.String(), "Wallet summary") {
		t.Fatalf("wallet page missing summary section: %q", rec.Body.String())
	}
}

func TestWalletAddressRouteRendersBalance(t *testing.T) {
	ensureHeliusEnv(t)

	const lamports = 3450000000
	const delegatedLamports = 1230000000
	const rewardLamports = 170000000
	const currentEpoch = 880
	const previousEpoch = currentEpoch - 1
	stubClient := &stubSolanaClient{
		balance: lamports,
		epochInfo: &EpochInfo{
			Epoch: currentEpoch,
		},
		inflationRewards: map[string]*InflationReward{
			"Stake111": {
				Epoch:         previousEpoch,
				EffectiveSlot: 12345,
				Amount:        rewardLamports,
				PostBalance:   delegatedLamports + rewardLamports,
			},
		},
		stakeAccounts: []StakeAccount{
			{Address: "Stake111", DelegatedLamports: delegatedLamports, VoteAccount: "Vote111"},
		},
	}

	handler := NewServer(
		WithSolanaClient(stubClient),
		WithLogger(newTestLogger()),
	)

	rec := performRequest(t, handler, http.MethodGet, "/wallet/4Nd1mYFHGQMiZ1ZkZZgwyUrKvYzUKGwEuUXXSb9Qe7CG")
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d, want %d", rec.Code, http.StatusOK)
	}

	if stubClient.calledWith != "4Nd1mYFHGQMiZ1ZkZZgwyUrKvYzUKGwEuUXXSb9Qe7CG" {
		t.Fatalf("solana client called with %q", stubClient.calledWith)
	}
	if !stubClient.listStakeCalled {
		t.Fatalf("expected stake accounts to be requested")
	}
	if len(stubClient.inflationRequests) != 1 {
		t.Fatalf("expected a single inflation reward request, got %d", len(stubClient.inflationRequests))
	}
	if got := stubClient.inflationRequests[0]; len(got) != 1 || got[0] != "Stake111" {
		t.Fatalf("unexpected inflation reward request addresses: %#v", got)
	}

	expectedBalance := fmt.Sprintf(">%s SOL<", formatNumber(float64(lamports+delegatedLamports)/lamportsPerSOL))
	if !strings.Contains(rec.Body.String(), expectedBalance) {
		t.Fatalf("wallet view missing balance marker %q: body=%q", expectedBalance, rec.Body.String())
	}

	if !strings.Contains(rec.Body.String(), "Delegated: <span>1.23 SOL</span>") {
		t.Fatalf("wallet view missing delegated amount: %q", rec.Body.String())
	}

	expectedDate := fmt.Sprintf("Epoch %d", previousEpoch)
	if !strings.Contains(rec.Body.String(), expectedDate) {
		t.Fatalf("wallet view missing reward date %q: body=%q", expectedDate, rec.Body.String())
	}

	expectedAmount := formatNumber(float64(rewardLamports) / lamportsPerSOL)
	if !strings.Contains(rec.Body.String(), expectedAmount) {
		t.Fatalf("wallet view missing reward amount %q: body=%q", expectedAmount, rec.Body.String())
	}

	if !strings.Contains(rec.Body.String(), "Vote111") {
		t.Fatalf("wallet view missing validator Vote111: %q", rec.Body.String())
	}
}

func TestWalletAddressRouteValidatesInput(t *testing.T) {
	ensureHeliusEnv(t)

	stubClient := &stubSolanaClient{}

	handler := NewServer(
		WithSolanaClient(stubClient),
		WithLogger(newTestLogger()),
	)

	rec := performRequest(t, handler, http.MethodGet, "/wallet/not-a-sol-address")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}

	if stubClient.calledWith != "" {
		t.Fatalf("expected solana client to not be called, but got %q", stubClient.calledWith)
	}
	if stubClient.listStakeCalled {
		t.Fatalf("expected stake accounts to not be requested")
	}
	if len(stubClient.signaturesRequested) != 0 {
		t.Fatalf("expected no signature lookups")
	}
	if len(stubClient.transactionsReq) != 0 {
		t.Fatalf("expected no transaction fetches")
	}
}

type stubSolanaClient struct {
	balance             uint64
	err                 error
	calledWith          string
	stakeAccounts       []StakeAccount
	stakeErr            error
	listStakeCalled     bool
	signaturesByAddress map[string][]SignatureInfo
	signaturesErr       error
	signaturesRequested []string
	transactions        map[string]*TransactionDetail
	transactionErr      error
	transactionsReq     []string
	epochInfo           *EpochInfo
	epochErr            error
	inflationRewards    map[string]*InflationReward
	inflationErr        error
	inflationRequests   [][]string
}

func (s *stubSolanaClient) GetBalance(_ context.Context, address string) (uint64, error) {
	s.calledWith = address
	if s.err != nil {
		return 0, s.err
	}
	return s.balance, nil
}

func (s *stubSolanaClient) ListStakeAccounts(_ context.Context, owner string) ([]StakeAccount, error) {
	s.listStakeCalled = true
	if s.stakeErr != nil {
		return nil, s.stakeErr
	}
	return append([]StakeAccount(nil), s.stakeAccounts...), nil
}

func (s *stubSolanaClient) GetSignaturesForAddress(_ context.Context, address string, limit int) ([]SignatureInfo, error) {
	s.signaturesRequested = append(s.signaturesRequested, address)
	if s.signaturesErr != nil {
		return nil, s.signaturesErr
	}
	entries := s.signaturesByAddress[address]
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return append([]SignatureInfo(nil), entries...), nil
}

func (s *stubSolanaClient) GetTransaction(_ context.Context, signature string) (*TransactionDetail, error) {
	s.transactionsReq = append(s.transactionsReq, signature)
	if s.transactionErr != nil {
		return nil, s.transactionErr
	}
	tx, ok := s.transactions[signature]
	if !ok {
		return nil, fmt.Errorf("transaction %s not found", signature)
	}
	copyTx := *tx
	copyTx.Meta.Rewards = append([]TransactionReward(nil), tx.Meta.Rewards...)
	return &copyTx, nil
}

func (s *stubSolanaClient) GetInflationReward(_ context.Context, addresses []string, epoch *uint64) ([]*InflationReward, error) {
	s.inflationRequests = append(s.inflationRequests, append([]string(nil), addresses...))
	if s.inflationErr != nil {
		return nil, s.inflationErr
	}
	results := make([]*InflationReward, len(addresses))
	for i, addr := range addresses {
		reward, ok := s.inflationRewards[addr]
		if !ok || reward == nil {
			continue
		}
		copyReward := *reward
		results[i] = &copyReward
	}
	return results, nil
}

func (s *stubSolanaClient) GetEpochInfo(_ context.Context) (*EpochInfo, error) {
	if s.epochErr != nil {
		return nil, s.epochErr
	}
	if s.epochInfo == nil {
		return nil, fmt.Errorf("epoch info not set")
	}
	copyInfo := *s.epochInfo
	return &copyInfo, nil
}

func newTestLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

func performRequest(t *testing.T, handler http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func ensureHeliusEnv(t *testing.T) {
	t.Helper()
	t.Setenv(heliusAPIKeyEnv, "test-key")
}
