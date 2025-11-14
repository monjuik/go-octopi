package gooctopi

import (
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServerIndexRendering(t *testing.T) {
	ensureHeliusEnv(t)

	handler := NewServer(WithSolanaClient(&stubSolanaClient{}))

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

	rec := performRequest(t, NewServer(WithSolanaClient(&stubSolanaClient{})), http.MethodGet, "/wallet/demo")
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
	const previousEpoch = 879
	rewardEndTime := time.Date(2024, time.January, 15, 12, 0, 0, 0, time.UTC)
	stubClient := &stubSolanaClient{
		balance: lamports,
		epochBoundaries: []EpochBoundary{
			{Epoch: previousEpoch, EndTime: rewardEndTime},
		},
		inflationRewards: map[uint64]map[string]*InflationReward{
			previousEpoch: {
				"Stake111": {
					Epoch:         previousEpoch,
					EffectiveSlot: 12345,
					Amount:        rewardLamports,
					PostBalance:   delegatedLamports + rewardLamports,
				},
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
	if len(stubClient.inflationEpochs) != 1 || stubClient.inflationEpochs[0] != previousEpoch {
		t.Fatalf("unexpected epochs requested: %#v", stubClient.inflationEpochs)
	}

	expectedBalance := fmt.Sprintf(">%s SOL<", formatNumber(float64(lamports+delegatedLamports)/lamportsPerSOL))
	if !strings.Contains(rec.Body.String(), expectedBalance) {
		t.Fatalf("wallet view missing balance marker %q: body=%q", expectedBalance, rec.Body.String())
	}

	if !strings.Contains(rec.Body.String(), "Delegated: <span>1.23 SOL</span>") {
		t.Fatalf("wallet view missing delegated amount: %q", rec.Body.String())
	}

	expectedDate := rewardEndTime.UTC().Format(rewardDateFormat) + " UTC"
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
	if !strings.Contains(rec.Body.String(), "Rewards (SOL)") {
		t.Fatalf("wallet view missing reward chart payload: %q", rec.Body.String())
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

func TestRewardChartPayload(t *testing.T) {
	now := time.Date(2025, time.November, 15, 0, 0, 0, 0, time.UTC)
	rewards := []RewardRow{
		{Timestamp: time.Date(2025, time.November, 2, 9, 0, 0, 0, time.UTC), AmountSOLValue: 0.3},
		{Timestamp: time.Date(2025, time.September, 18, 12, 0, 0, 0, time.UTC), AmountSOLValue: 0.1},
		{Timestamp: time.Date(2025, time.August, 5, 7, 0, 0, 0, time.UTC), AmountSOLValue: 0.2},
		{Timestamp: time.Date(2025, time.July, 30, 23, 0, 0, 0, time.UTC), AmountSOLValue: 0.4},
	}

	payload := rewardChartPayload(now, rewards)

	expectedLabels := []string{"Aug", "Sep", "Oct", "Nov"}
	if len(payload.Labels) != len(expectedLabels) {
		t.Fatalf("unexpected number of labels: %#v", payload.Labels)
	}
	for i, label := range payload.Labels {
		if label != expectedLabels[i] {
			t.Fatalf("unexpected label at %d: got %s want %s", i, label, expectedLabels[i])
		}
	}

	if len(payload.Series) != 1 {
		t.Fatalf("expected single series, got %d", len(payload.Series))
	}
	expectedData := []float64{0.2, 0.1, 0.0, 0.3}
	if len(payload.Series[0].Data) != len(expectedData) {
		t.Fatalf("unexpected data points: %#v", payload.Series[0].Data)
	}
	for i, got := range payload.Series[0].Data {
		if math.Abs(got-expectedData[i]) > 1e-9 {
			t.Fatalf("unexpected data at %d: got %.9f want %.9f", i, got, expectedData[i])
		}
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
	epochBoundaries     []EpochBoundary
	epochBoundariesErr  error
	epochBoundaryReqs   []time.Time
	inflationRewards    map[uint64]map[string]*InflationReward
	inflationErr        error
	inflationRequests   [][]string
	inflationEpochs     []uint64
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
	if epoch != nil {
		s.inflationEpochs = append(s.inflationEpochs, *epoch)
	}
	if s.inflationErr != nil {
		return nil, s.inflationErr
	}
	var rewardsForEpoch map[string]*InflationReward
	if epoch != nil {
		rewardsForEpoch = s.inflationRewards[*epoch]
	}
	results := make([]*InflationReward, len(addresses))
	for i, addr := range addresses {
		var reward *InflationReward
		if rewardsForEpoch != nil {
			reward = rewardsForEpoch[addr]
		}
		if reward == nil {
			continue
		}
		copyReward := *reward
		results[i] = &copyReward
	}
	return results, nil
}

func (s *stubSolanaClient) GetEpochBoundaries(_ context.Context, minEndTime time.Time) ([]EpochBoundary, error) {
	if s.epochBoundariesErr != nil {
		return nil, s.epochBoundariesErr
	}
	s.epochBoundaryReqs = append(s.epochBoundaryReqs, minEndTime)
	if len(s.epochBoundaries) == 0 {
		return nil, nil
	}
	results := make([]EpochBoundary, len(s.epochBoundaries))
	copy(results, s.epochBoundaries)
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
