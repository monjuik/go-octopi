package gooctopi

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestCollectJitoTips(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	epoch := uint64(880)
	timestamp := time.Date(2024, time.January, 10, 12, 0, 0, 0, time.UTC)

	stub := &stubSolanaClient{
		signaturesByAddress: map[string][]SignatureInfo{
			"Stake111": {
				{Signature: "sig-tip", Slot: 1200},
			},
		},
		transactions: map[string]*TransactionDetail{
			"sig-tip": {
				Slot:        1200,
				BlockTime:   ptrInt64(timestamp.Unix()),
				AccountKeys: []string{"Stake111", "Vote111"},
				Instructions: []TransactionInstruction{
					{ProgramID: "RouterBmuRBkPUbgEDMtdvTZ75GBdSREZR5uGUxxxpb"},
				},
				Meta: TransactionMeta{
					PreBalances:  []uint64{1_000_000_000, 0},
					PostBalances: []uint64{1_500_000_000, 0},
				},
			},
		},
	}

	collector := newJitoTipCollector(stub, newTestLogger())
	stakeAccounts := []StakeAccount{
		{
			Address:           "Stake111",
			WithdrawAuthority: "Withdraw111",
			VoteAccount:       "Vote111",
		},
	}

	epochDates := map[uint64]time.Time{
		epoch: timestamp,
	}
	targetEpochs := []uint64{epoch, epoch - 1, epoch - 2}
	boundaries := []EpochBoundary{
		{Epoch: epoch, EndSlot: 1500},
		{Epoch: epoch - 1, EndSlot: 1000},
		{Epoch: epoch - 2, EndSlot: 500},
	}

	rows, err := collectJitoTips(ctx, collector, stakeAccounts, epochDates, targetEpochs, boundaries)
	if err != nil {
		t.Fatalf("collectJitoTips returned error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 reward row, got %d", len(rows))
	}

	row := rows[0]
	if row.Type != "Jito Tip" {
		t.Fatalf("unexpected row type: %s", row.Type)
	}
	if row.Validator != "Vote111" {
		t.Fatalf("unexpected validator: %s", row.Validator)
	}
	expectedAmount := float64(500_000_000) / lamportsPerSOL
	if math.Abs(row.AmountSOLValue-expectedAmount) > 1e-12 {
		t.Fatalf("unexpected amount: got %.12f want %.12f", row.AmountSOLValue, expectedAmount)
	}
	if row.Epoch != int(epoch) {
		t.Fatalf("unexpected epoch: got %d want %d", row.Epoch, epoch)
	}

	rows, err = collectJitoTips(ctx, collector, stakeAccounts, epochDates, targetEpochs, boundaries)
	if err != nil {
		t.Fatalf("second collectJitoTips returned error: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no new rows on second run, got %d", len(rows))
	}
}

func TestCollectJitoTipsSkipsNonTipTransactions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stub := &stubSolanaClient{
		signaturesByAddress: map[string][]SignatureInfo{
			"Stake111": {
				{Signature: "sig-other", Slot: 900},
			},
		},
		transactions: map[string]*TransactionDetail{
			"sig-other": {
				Slot:        900,
				AccountKeys: []string{"Stake111"},
				Instructions: []TransactionInstruction{
					{ProgramID: "OtherProgram"},
				},
				Meta: TransactionMeta{
					PreBalances:  []uint64{1_000_000_000},
					PostBalances: []uint64{1_100_000_000},
				},
			},
		},
	}

	collector := newJitoTipCollector(stub, newTestLogger())
	stakeAccounts := []StakeAccount{
		{Address: "Stake111", WithdrawAuthority: "Withdraw111", VoteAccount: "Vote111"},
	}
	targetEpochs := []uint64{880, 879, 878}
	boundaries := []EpochBoundary{
		{Epoch: 880, EndSlot: 1500},
		{Epoch: 879, EndSlot: 1200},
		{Epoch: 878, EndSlot: 1000},
	}

	rows, err := collectJitoTips(ctx, collector, stakeAccounts, map[uint64]time.Time{}, targetEpochs, boundaries)
	if err != nil {
		t.Fatalf("collectJitoTips returned error: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no rows for non-tip transaction, got %d", len(rows))
	}
}

func ptrInt64(v int64) *int64 {
	return &v
}
