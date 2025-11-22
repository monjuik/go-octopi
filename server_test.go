package gooctopi

import (
	"context"
	"fmt"
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

func TestHomePageShowsRecentValidators(t *testing.T) {
	ensureHeliusEnv(t)

	now := time.Date(2025, time.January, 2, 15, 4, 0, 0, time.UTC)
	stub := &stubSolanaClient{
		validatorPerformances: []ValidatorPerformance{
			{
				Name:        "Atlas Nodes",
				VoteAccount: "Vote111",
				XIRR:        5.5,
				UpdatedAt:   now,
			},
		},
	}

	rec := performRequest(t, NewServer(WithSolanaClient(stub)), http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Recent validators") {
		t.Fatalf("expected recent validators section in body: %q", body)
	}
	if !strings.Contains(body, "Atlas Nodes") {
		t.Fatalf("expected validator name to be rendered: %q", body)
	}
	if !strings.Contains(body, "5.50%") {
		t.Fatalf("expected formatted XIRR in body: %q", body)
	}
	if !strings.Contains(body, now.Format("02 Jan 15:04 UTC")) {
		t.Fatalf("expected timestamp in body: %q", body)
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
	rewardEndTime := time.Now().UTC().Add(-72 * time.Hour)
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
		epochInfo: &EpochInfo{
			Epoch:        previousEpoch + 1,
			AbsoluteSlot: 1_000_000,
			SlotIndex:    10,
			SlotsInEpoch: 400,
		},
		solPriceUSD: 155.5,
	}

	handler := NewServer(
		WithSolanaClient(stubClient),
		WithLogger(newTestLogger()),
	)

	rec := performRequest(t, handler, http.MethodGet, "/wallet/4Nd1mYFHGQMiZ1ZkZZgwyUrKvYzUKGwEuUXXSb9Qe7CG")
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()

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
	if !strings.Contains(body, expectedBalance) {
		t.Fatalf("wallet view missing balance marker %q: body=%q", expectedBalance, body)
	}

	expectedDate := rewardEndTime.UTC().Format(rewardDateFormat) + " UTC"
	if !strings.Contains(body, expectedDate) {
		t.Fatalf("wallet view missing reward date %q: body=%q", expectedDate, body)
	}

	expectedAmount := formatNumber(float64(rewardLamports) / lamportsPerSOL)
	if !strings.Contains(body, expectedAmount) {
		t.Fatalf("wallet view missing reward amount %q: body=%q", expectedAmount, body)
	}

	if !strings.Contains(body, "Vote111") {
		t.Fatalf("wallet view missing validator Vote111: %q", body)
	}
	if !strings.Contains(body, "Rewards (SOL)") {
		t.Fatalf("wallet view missing reward chart payload: %q", body)
	}

	if !strings.Contains(body, "Staked Balance") {
		t.Fatalf("wallet view missing staked balance metric label: %q", body)
	}

	expectedStakedMetric := fmt.Sprintf("%s SOL", formatNumber(float64(delegatedLamports)/lamportsPerSOL))
	if !strings.Contains(body, expectedStakedMetric) {
		t.Fatalf("wallet view missing staked balance metric value %q: body=%q", expectedStakedMetric, body)
	}

	if !strings.Contains(body, "28d Rewards") {
		t.Fatalf("wallet view missing 28d rewards metric label: %q", body)
	}

	expectedTwentyEightDayRewards := fmt.Sprintf("%s SOL", formatNumber(float64(rewardLamports)/lamportsPerSOL))
	if !strings.Contains(body, expectedTwentyEightDayRewards) {
		t.Fatalf("wallet view missing 28d rewards metric value %q: body=%q", expectedTwentyEightDayRewards, body)
	}
	metricIdx := strings.Index(body, "28d Rewards")
	if metricIdx == -1 {
		t.Fatalf("wallet view missing 28d rewards marker")
	}
	metricSliceEnd := metricIdx + 400
	if metricSliceEnd > len(body) {
		metricSliceEnd = len(body)
	}
	metricSnippet := body[metricIdx:metricSliceEnd]

	annualIdx := strings.Index(body, "Annual Return")
	if annualIdx == -1 {
		t.Fatalf("wallet view missing annual return metric label: %q", body)
	}
	valueMarker := `<div class="metric-value fs-5 fw-semibold mt-1">`
	valueStart := strings.Index(body[annualIdx:], valueMarker)
	if valueStart == -1 {
		t.Fatalf("wallet view missing annual return value block: %q", body)
	}
	valueStart += annualIdx + len(valueMarker)
	valueEnd := strings.Index(body[valueStart:], "</div>")
	if valueEnd == -1 {
		t.Fatalf("wallet view missing annual return closing tag: %q", body)
	}
	annualValue := strings.TrimSpace(body[valueStart : valueStart+valueEnd])
	defaultAnnual := fmt.Sprintf("%.2f%%", defaultAnnualReturnPercent)
	if annualValue == defaultAnnual {
		t.Fatalf("annual return fell back to default %s: body=%q", defaultAnnual, body)
	}
	if !strings.HasSuffix(annualValue, "%") {
		t.Fatalf("annual return missing percentage suffix: %q", annualValue)
	}

	expectedFiat := formatFiatUSD(float64(lamports+delegatedLamports) / lamportsPerSOL * stubClient.solPriceUSD)
	if !strings.Contains(body, expectedFiat) {
		t.Fatalf("wallet view missing fiat balance %q: body=%q", expectedFiat, body)
	}

	expectedRewardUSD := formatFiatUSD(float64(rewardLamports) / lamportsPerSOL * stubClient.solPriceUSD)
	if !strings.Contains(body, expectedRewardUSD) {
		t.Fatalf("reward table missing fiat value %q: body=%q", expectedRewardUSD, body)
	}
	if !strings.Contains(metricSnippet, expectedRewardUSD) {
		t.Fatalf("28d rewards metric missing fiat subtext %q: snippet=%q", expectedRewardUSD, metricSnippet)
	}
}

func TestWalletHidesFiatValueWhenPriceUnavailable(t *testing.T) {
	ensureHeliusEnv(t)

	stubClient := &stubSolanaClient{
		balance: 2 * lamportsPerSOL,
		epochBoundaries: []EpochBoundary{
			{Epoch: 1, EndTime: time.Now().UTC()},
		},
		epochInfo: &EpochInfo{
			Epoch:        2,
			AbsoluteSlot: 10,
			SlotIndex:    5,
			SlotsInEpoch: 100,
		},
		solPriceErr: fmt.Errorf("price unavailable"),
	}

	handler := NewServer(
		WithSolanaClient(stubClient),
		WithLogger(newTestLogger()),
	)

	rec := performRequest(t, handler, http.MethodGet, "/wallet/4Nd1mYFHGQMiZ1ZkZZgwyUrKvYzUKGwEuUXXSb9Qe7CG")
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if strings.Contains(body, "≈ $") {
		t.Fatalf("fiat estimate should be hidden when price missing: body=%q", body)
	}
	if !strings.Contains(body, "Last 28 days") {
		t.Fatalf("expected fallback metric subtext when price missing: body=%q", body)
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
	now := time.Date(2025, time.November, 10, 0, 0, 0, 0, time.UTC) // Monday
	rewards := []RewardRow{
		{Timestamp: time.Date(2025, time.November, 6, 9, 0, 0, 0, time.UTC), AmountSOLValue: 0.5},
		{Timestamp: time.Date(2025, time.October, 29, 12, 0, 0, 0, time.UTC), AmountSOLValue: 0.3},
		{Timestamp: time.Date(2025, time.October, 20, 7, 0, 0, 0, time.UTC), AmountSOLValue: 0.1},
		{Timestamp: time.Date(2025, time.October, 14, 23, 0, 0, 0, time.UTC), AmountSOLValue: 0.2},
	}

	payload := rewardChartPayload(now, rewards)

	expectedLabels := []string{"Oct 13", "Oct 20", "Oct 27", "Nov 03", "Nov 10"}
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
	expectedData := []float64{0.2, 0.1, 0.3, 0.5, 0.0}
	if len(payload.Series[0].Data) != len(expectedData) {
		t.Fatalf("unexpected data points: %#v", payload.Series[0].Data)
	}
	for i, got := range payload.Series[0].Data {
		if math.Abs(got-expectedData[i]) > 1e-9 {
			t.Fatalf("unexpected data at %d: got %.9f want %.9f", i, got, expectedData[i])
		}
	}
}

func TestXIRRBasic(t *testing.T) {
	base := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	flows := []cashFlow{
		{when: base, amount: -1000},
		{when: base.AddDate(1, 0, 0), amount: 1100},
	}
	rate, ok := xirr(flows)
	if !ok {
		t.Fatalf("xirr failed to converge")
	}
	if math.Abs(rate-0.1) > 1e-6 {
		t.Fatalf("unexpected rate: %.9f", rate)
	}
}

func TestComputeAnnualReturnPercent(t *testing.T) {
	now := time.Date(2025, time.March, 1, 0, 0, 0, 0, time.UTC)
	rewardTime := now.Add(-15 * 24 * time.Hour)
	rewards := []RewardRow{
		{Epoch: 42, Timestamp: rewardTime, AmountSOLValue: 2},
		{Epoch: 43, Timestamp: rewardTime.Add(3 * 24 * time.Hour), AmountSOLValue: 2},
	}
	boundaries := []EpochBoundary{
		{Epoch: 42, EndTime: rewardTime},
		{Epoch: 41, EndTime: rewardTime.Add(-3 * 24 * time.Hour)},
	}
	info := &EpochInfo{Epoch: 50, AbsoluteSlot: 1_000_000, SlotIndex: 100, SlotsInEpoch: 400}

	apy, ok := computeAnnualReturnPercent(now, 100, rewards, boundaries, info)
	if !ok {
		t.Fatalf("computeAnnualReturnPercent returned false")
	}
	if apy <= 0 {
		t.Fatalf("expected positive APY, got %.2f", apy)
	}
}

func TestServerRecordValidatorXIRR(t *testing.T) {
	now := time.Date(2025, time.March, 1, 0, 0, 0, 0, time.UTC)
	rewardTime := now.Add(-15 * 24 * time.Hour)
	rewards := []RewardRow{
		{Epoch: 42, Timestamp: rewardTime, AmountSOLValue: 2, ValidatorVoteAccount: "Vote111"},
		{Epoch: 43, Timestamp: rewardTime.Add(3 * 24 * time.Hour), AmountSOLValue: 2, ValidatorVoteAccount: "Vote111"},
	}
	boundaries := []EpochBoundary{
		{Epoch: 42, EndTime: rewardTime},
		{Epoch: 41, EndTime: rewardTime.Add(-3 * 24 * time.Hour)},
	}
	epochInfo := &EpochInfo{Epoch: 50, AbsoluteSlot: 1_000_000, SlotIndex: 100, SlotsInEpoch: 400}
	stakeAccounts := []StakeAccount{
		{VoteAccount: "Vote111", DelegatedLamports: uint64(lamportsPerSOL * 100)},
	}

	stub := &stubSolanaClient{}
	srv := &server{solanaClient: stub}
	srv.recordValidatorXIRR(context.Background(), now, stakeAccounts, rewards, boundaries, epochInfo)

	if len(stub.recordedValidatorXIRR) != 1 {
		t.Fatalf("expected single validator record, got %d", len(stub.recordedValidatorXIRR))
	}
	call := stub.recordedValidatorXIRR[0]
	if call.vote != "Vote111" {
		t.Fatalf("unexpected validator recorded: %s", call.vote)
	}
	expected, ok := computeAnnualReturnPercent(now, 100, rewards, boundaries, epochInfo)
	if !ok {
		t.Fatalf("expected annual return percent")
	}
	if math.Abs(call.value-expected) > 1e-6 {
		t.Fatalf("unexpected recorded XIRR: got %.6f want %.6f", call.value, expected)
	}
}

type stubSolanaClient struct {
	balance               uint64
	err                   error
	calledWith            string
	stakeAccounts         []StakeAccount
	stakeErr              error
	listStakeCalled       bool
	signaturesByAddress   map[string][]SignatureInfo
	signaturesErr         error
	signaturesRequested   []string
	transactions          map[string]*TransactionDetail
	transactionErr        error
	transactionsReq       []string
	eventsErr             error
	eventsByAuthority     map[string][]Event
	eventRequests         []GetEventsRequest
	epochInfo             *EpochInfo
	epochErr              error
	epochBoundaries       []EpochBoundary
	epochBoundariesErr    error
	epochBoundaryReqs     []time.Time
	currentEpochEnd       time.Time
	currentEpochEndErr    error
	inflationRewards      map[uint64]map[string]*InflationReward
	inflationErr          error
	inflationRequests     [][]string
	inflationEpochs       []uint64
	voteAccounts          map[string]VoteAccount
	voteErr               error
	validatorNames        map[string]string
	validatorLookupErr    error
	validatorPerformances []ValidatorPerformance
	recordedValidatorXIRR []validatorXIRRCall
	solPriceUSD           float64
	solPriceErr           error
	solPriceCalls         int
}

type validatorXIRRCall struct {
	vote  string
	value float64
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

func (s *stubSolanaClient) GetSignaturesForAddress(_ context.Context, address string, limit int, before string) ([]SignatureInfo, error) {
	s.signaturesRequested = append(s.signaturesRequested, address)
	if s.signaturesErr != nil {
		return nil, s.signaturesErr
	}
	entries := s.signaturesByAddress[address]
	if before != "" {
		if idx := indexOfSignature(entries, before); idx >= 0 && idx+1 < len(entries) {
			entries = entries[idx+1:]
		} else if idx >= 0 {
			entries = nil
		}
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return append([]SignatureInfo(nil), entries...), nil
}

func indexOfSignature(entries []SignatureInfo, signature string) int {
	for i, entry := range entries {
		if entry.Signature == signature {
			return i
		}
	}
	return -1
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
	copyTx.Meta.PreBalances = append([]uint64(nil), tx.Meta.PreBalances...)
	copyTx.Meta.PostBalances = append([]uint64(nil), tx.Meta.PostBalances...)
	copyTx.AccountKeys = append([]string(nil), tx.AccountKeys...)
	copyTx.Instructions = append([]TransactionInstruction(nil), tx.Instructions...)
	return &copyTx, nil
}

func (s *stubSolanaClient) GetTransactions(ctx context.Context, signatures []string) (map[string]*TransactionDetail, error) {
	results := make(map[string]*TransactionDetail, len(signatures))
	for _, sig := range signatures {
		tx, err := s.GetTransaction(ctx, sig)
		if err != nil {
			return nil, err
		}
		results[sig] = tx
	}
	return results, nil
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

func (s *stubSolanaClient) GetCurrentEpochEnd(_ context.Context) (time.Time, error) {
	if s.currentEpochEndErr != nil {
		return time.Time{}, s.currentEpochEndErr
	}
	if !s.currentEpochEnd.IsZero() {
		return s.currentEpochEnd, nil
	}
	if s.epochInfo == nil {
		return time.Time{}, fmt.Errorf("epoch info not set")
	}
	return estimateCurrentEpochEnd(time.Now(), s.epochInfo), nil
}

func (s *stubSolanaClient) GetEvents(_ context.Context, req GetEventsRequest) (*EventsPage, error) {
	if s.eventsErr != nil {
		return nil, s.eventsErr
	}
	s.eventRequests = append(s.eventRequests, req)
	var authority string
	if req.Query != nil {
		if accounts, ok := req.Query["accounts"]; ok {
			switch v := accounts.(type) {
			case []string:
				if len(v) > 0 {
					authority = v[0]
				}
			case []any:
				if len(v) > 0 {
					if val, ok := v[0].(string); ok {
						authority = val
					}
				}
			}
		}
	}
	events := s.eventsByAuthority[authority]
	page := &EventsPage{
		Events: make([]Event, len(events)),
	}
	copy(page.Events, events)
	return page, nil
}

func (s *stubSolanaClient) GetVoteAccounts(_ context.Context, votePubkey string) ([]VoteAccount, error) {
	if s.voteErr != nil {
		return nil, s.voteErr
	}
	if len(s.voteAccounts) == 0 {
		return nil, nil
	}
	if votePubkey != "" {
		if acct, ok := s.voteAccounts[votePubkey]; ok {
			return []VoteAccount{acct}, nil
		}
		return nil, nil
	}
	results := make([]VoteAccount, 0, len(s.voteAccounts))
	for _, acct := range s.voteAccounts {
		results = append(results, acct)
	}
	return results, nil
}

func (s *stubSolanaClient) LookupValidatorName(_ context.Context, votePubkey string) (string, error) {
	if s.validatorLookupErr != nil {
		return "", s.validatorLookupErr
	}
	if s.validatorNames == nil {
		return "", nil
	}
	return s.validatorNames[votePubkey], nil
}

func (s *stubSolanaClient) RecordValidatorXIRR(_ context.Context, votePubkey string, xirr float64) {
	s.recordedValidatorXIRR = append(s.recordedValidatorXIRR, validatorXIRRCall{
		vote:  votePubkey,
		value: xirr,
	})
}

func (s *stubSolanaClient) RecentValidatorPerformances(_ context.Context, limit int) []ValidatorPerformance {
	if limit <= 0 || len(s.validatorPerformances) == 0 {
		return nil
	}
	if limit > len(s.validatorPerformances) {
		limit = len(s.validatorPerformances)
	}
	out := make([]ValidatorPerformance, limit)
	copy(out, s.validatorPerformances[:limit])
	return out
}

func (s *stubSolanaClient) GetSOLPrice(_ context.Context) (float64, error) {
	s.solPriceCalls++
	if s.solPriceErr != nil {
		return 0, s.solPriceErr
	}
	price := s.solPriceUSD
	if price == 0 {
		price = 180.0
	}
	return price, nil
}

func newTestLogger() Logger {
	return NewDiscardLogger()
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
	t.Setenv(HeliusAPIKeyEnv, "test-key")
	t.Setenv(ValidatorsAPIKeyEnv, "validators-test-key")
}
