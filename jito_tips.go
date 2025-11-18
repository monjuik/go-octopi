package gooctopi

import (
	"container/list"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	jitoSignaturePageSize  = 25
	jitoMaxPagesPerStake   = 10
	jitoProcessedCacheSize = 256
	jitoEpochLookback      = 3
	jitoEpochChunkRuns     = 4 // increasing number of runs leads to 429 responses
	jitoEpochChunkInterval = time.Second
)

var jitoProgramIDs = map[string]struct{}{
	"JitoTipDistribution11111111111111111111111111111": {},
	"RouterBmuRBkPUbgEDMtdvTZ75GBdSREZR5uGUxxxpb":      {},
	"4R3gSG8BpU4t19KYj8CfnbtRpnT8gtk4dvTHxVRwc2r7":     {},
}

type jitoTipCollector struct {
	client SolanaClient
	logger Logger

	mu                  sync.Mutex
	processedSignatures *stringLRU[struct{}]
}

func newJitoTipCollector(client SolanaClient, logger Logger) *jitoTipCollector {
	if client == nil {
		return nil
	}
	return &jitoTipCollector{
		client:              client,
		logger:              logger,
		processedSignatures: newStringLRU[struct{}](jitoProcessedCacheSize),
	}
}

func collectJitoTips(
	ctx context.Context,
	collector *jitoTipCollector,
	stakeAccounts []StakeAccount,
	epochDates map[uint64]time.Time,
	targetEpochs []uint64,
	boundaries []EpochBoundary,
) ([]RewardRow, error) {
	if collector == nil || len(stakeAccounts) == 0 {
		return nil, nil
	}

	if len(targetEpochs) == 0 {
		return nil, nil
	}

	maxEpochs := jitoEpochLookback * jitoEpochChunkRuns
	if len(targetEpochs) > maxEpochs {
		targetEpochs = targetEpochs[:maxEpochs]
	}

	var rewardRows []RewardRow
	for offset := 0; offset < len(targetEpochs); offset += jitoEpochLookback {
		end := min(offset+jitoEpochLookback, len(targetEpochs))

		chunkRows, err := collectJitoTipsWindow(ctx, collector, stakeAccounts, epochDates, targetEpochs[offset:end], boundaries)
		if err != nil {
			return nil, err
		}
		rewardRows = append(rewardRows, chunkRows...)

		if end >= len(targetEpochs) {
			break
		}

		select {
		case <-ctx.Done():
			return rewardRows, ctx.Err()
		default:
		}

		time.Sleep(jitoEpochChunkInterval)
	}

	return rewardRows, nil
}

func collectJitoTipsWindow(
	ctx context.Context,
	collector *jitoTipCollector,
	stakeAccounts []StakeAccount,
	epochDates map[uint64]time.Time,
	targetEpochs []uint64,
	boundaries []EpochBoundary,
) ([]RewardRow, error) {
	if collector == nil || len(stakeAccounts) == 0 || len(targetEpochs) == 0 {
		return nil, nil
	}

	allowedEpochs := make(map[uint64]struct{})
	for i, epoch := range targetEpochs {
		if i >= jitoEpochLookback {
			break
		}
		allowedEpochs[epoch] = struct{}{}
	}
	if len(allowedEpochs) == 0 {
		return nil, nil
	}

	stakeInfos := buildStakeAccountIndex(stakeAccounts)
	if len(stakeInfos) == 0 {
		return nil, nil
	}

	epochIntervals := buildEpochIntervals(boundaries)
	if len(epochIntervals) == 0 {
		return nil, nil
	}

	minAllowedSlot := ^uint64(0)
	for epoch := range allowedEpochs {
		if interval, ok := epochIntervals[epoch]; ok && interval.start < minAllowedSlot {
			minAllowedSlot = interval.start
		}
	}
	if minAllowedSlot == ^uint64(0) {
		minAllowedSlot = 0
	}

	rewardRows := make([]RewardRow, 0)
	for _, info := range stakeInfos {
		rows, err := collector.collectStakeAccountTips(ctx, info, allowedEpochs, epochIntervals, epochDates, minAllowedSlot)
		if err != nil {
			return nil, err
		}
		rewardRows = append(rewardRows, rows...)
	}

	return rewardRows, nil
}

func (c *jitoTipCollector) collectStakeAccountTips(
	ctx context.Context,
	account trackedStakeAccount,
	allowedEpochs map[uint64]struct{},
	epochIntervals map[uint64]slotInterval,
	epochDates map[uint64]time.Time,
	minAllowedSlot uint64,
) ([]RewardRow, error) {
	var rows []RewardRow
	before := ""
	pages := 0

	for pages < jitoMaxPagesPerStake {
		signatures, err := c.client.GetSignaturesForAddress(ctx, account.address, jitoSignaturePageSize, before)
		if err != nil {
			return nil, fmt.Errorf("signatures for %s: %w", account.address, err)
		}
		if len(signatures) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return rows, ctx.Err()
		default:
		}

		firstSlot := signatures[0].Slot
		lastSlot := signatures[len(signatures)-1].Slot
		c.logf(
			"jito tips stake=%s page=%d signatures=%d firstSlot=%d lastSlot=%d before=%s",
			account.address,
			pages,
			len(signatures),
			firstSlot,
			lastSlot,
			before,
		)

		newSignatures := make([]string, 0, len(signatures))
		reachedOldSlot := false
		for _, sig := range signatures {
			if sig.Slot < minAllowedSlot {
				reachedOldSlot = true
				continue
			}
			if c.isSignatureProcessed(sig.Signature) {
				continue
			}
			newSignatures = append(newSignatures, sig.Signature)
		}

		if len(newSignatures) > 0 {
			txs, err := fetchTransactions(ctx, c.client, newSignatures)
			if err != nil {
				c.logf("jito tips fetch warning stake=%s err=%v", account.address, err)
				return rows, nil
			}
			for _, signature := range newSignatures {
				detail := txs[signature]
				if detail == nil || len(detail.Meta.Err) > 0 {
					c.logf("jito tips skip signature=%s reason=noDetailOrErr", signature)
					continue
				}
				if detail.Slot < minAllowedSlot {
					c.logf("jito tips skip signature=%s reason=oldSlot slot=%d minSlot=%d", signature, detail.Slot, minAllowedSlot)
					continue
				}
				if !transactionHasJitoInstruction(detail) {
					c.logf("jito tips skip signature=%s reason=noJitoInstruction slot=%d", signature, detail.Slot)
					continue
				}

				epoch, ok := epochForSlot(detail.Slot, epochIntervals)
				if !ok {
					c.logf("jito tips skip signature=%s reason=noEpochMapping slot=%d", signature, detail.Slot)
					continue
				}
				if _, ok := allowedEpochs[epoch]; !ok {
					continue
				}

				lamports := computeTipLamports(detail, account.address)
				if lamports == 0 {
					c.logf("jito tips skip signature=%s reason=zeroDelta slot=%d", signature, detail.Slot)
					continue
				}

				votePubkey := account.validator
				validator := votePubkey
				if validator == "" {
					validator = "Unknown"
				}

				timestamp := timestampFromDetail(detail)
				if timestamp.IsZero() {
					if epochDate, ok := epochDates[epoch]; ok {
						timestamp = epochDate
					}
				}

				amountSOL := float64(lamports) / lamportsPerSOL
				rows = append(rows, RewardRow{
					Date:                 formatRewardDate(timestamp, epoch),
					Type:                 "Jito Tip",
					AmountSOL:            formatNumber(amountSOL),
					AmountSOLValue:       amountSOL,
					Validator:            validator,
					ValidatorVoteAccount: votePubkey,
					Epoch:                int(epoch),
					Timestamp:            timestamp,
				})

				c.markSignatureProcessed(signature)
				c.logf("jito tip signature=%s stake=%s epoch=%d lamports=%d validator=%s", signature, account.address, epoch, lamports, validator)
			}
		}

		before = signatures[len(signatures)-1].Signature
		pages++

		if len(signatures) < jitoSignaturePageSize || reachedOldSlot {
			break
		}
	}

	if len(rows) == 0 {
		c.logf("jito tips stake=%s completed with no tips", account.address)
	} else {
		c.logf("jito tips stake=%s collected=%d", account.address, len(rows))
	}

	return rows, nil
}

func transactionHasJitoInstruction(detail *TransactionDetail) bool {
	for _, inst := range detail.Instructions {
		if isJitoProgram(inst.ProgramID) {
			return true
		}
	}
	for _, inner := range detail.Meta.InnerInstructions {
		for _, inst := range inner.Instructions {
			if isJitoProgram(inst.ProgramID) {
				return true
			}
		}
	}
	return false
}

func isJitoProgram(programID string) bool {
	_, ok := jitoProgramIDs[programID]
	return ok
}

func timestampFromDetail(detail *TransactionDetail) time.Time {
	if detail.BlockTime != nil && *detail.BlockTime > 0 {
		return time.Unix(*detail.BlockTime, 0).UTC()
	}
	return time.Time{}
}

func (c *jitoTipCollector) isSignatureProcessed(signature string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.processedSignatures == nil {
		return false
	}
	_, ok := c.processedSignatures.Get(signature)
	return ok
}

func (c *jitoTipCollector) markSignatureProcessed(signature string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.processedSignatures == nil {
		return
	}
	c.processedSignatures.Add(signature, struct{}{})
}

func (c *jitoTipCollector) logf(format string, args ...any) {
	if c.logger == nil {
		return
	}
	c.logger.Printf(format, args...)
}

type trackedStakeAccount struct {
	address   string
	validator string
}

func buildStakeAccountIndex(accounts []StakeAccount) []trackedStakeAccount {
	result := make([]trackedStakeAccount, 0, len(accounts))
	seen := make(map[string]struct{})
	for _, account := range accounts {
		if account.Address == "" {
			continue
		}
		if _, ok := seen[account.Address]; ok {
			continue
		}
		seen[account.Address] = struct{}{}
		result = append(result, trackedStakeAccount{
			address:   account.Address,
			validator: account.VoteAccount,
		})
	}
	return result
}

type slotInterval struct {
	start uint64
	end   uint64
}

func buildEpochIntervals(boundaries []EpochBoundary) map[uint64]slotInterval {
	if len(boundaries) == 0 {
		return nil
	}
	sorted := make([]EpochBoundary, len(boundaries))
	copy(sorted, boundaries)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Epoch < sorted[j].Epoch
	})

	result := make(map[uint64]slotInterval, len(sorted))
	var lastEnd uint64
	for i, entry := range sorted {
		start := uint64(0)
		if i > 0 {
			start = lastEnd + 1
		}
		result[entry.Epoch] = slotInterval{
			start: start,
			end:   entry.EndSlot,
		}
		lastEnd = entry.EndSlot
	}
	return result
}

func epochForSlot(slot uint64, intervals map[uint64]slotInterval) (uint64, bool) {
	for epoch, interval := range intervals {
		if slot >= interval.start && slot <= interval.end {
			return epoch, true
		}
	}
	return 0, false
}

func computeTipLamports(detail *TransactionDetail, authority string) uint64 {
	if detail == nil || len(detail.AccountKeys) == 0 {
		return 0
	}
	for idx, key := range detail.AccountKeys {
		if key != authority {
			continue
		}
		if idx >= len(detail.Meta.PostBalances) || idx >= len(detail.Meta.PreBalances) {
			continue
		}
		post := detail.Meta.PostBalances[idx]
		pre := detail.Meta.PreBalances[idx]
		if post > pre {
			return post - pre
		}
	}
	return 0
}

func fetchTransactions(ctx context.Context, client SolanaClient, signatures []string) (map[string]*TransactionDetail, error) {
	if len(signatures) == 0 {
		return nil, nil
	}
	results, err := client.GetTransactions(ctx, signatures)
	if err == nil {
		return results, nil
	}
	if !isBatchUnavailableError(err) {
		return nil, err
	}
	fallback := make(map[string]*TransactionDetail, len(signatures))
	for _, sig := range signatures {
		solanaRPCLogger.Printf("getTransaction signature=%s (fallback)", sig)
		detail, txErr := client.GetTransaction(ctx, sig)
		if txErr != nil {
			return nil, fmt.Errorf("getTransaction %s: %w", sig, txErr)
		}
		fallback[sig] = detail
	}
	return fallback, nil
}

func isBatchUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "Batch requests are only available for paid plans")
}

type stringLRU[V any] struct {
	capacity int
	items    map[string]*list.Element
	order    *list.List
}

type lruEntry[V any] struct {
	key   string
	value V
}

func newStringLRU[V any](capacity int) *stringLRU[V] {
	if capacity <= 0 {
		capacity = 1
	}
	return &stringLRU[V]{
		capacity: capacity,
		items:    make(map[string]*list.Element),
		order:    list.New(),
	}
}

func (c *stringLRU[V]) Get(key string) (V, bool) {
	var zero V
	if c == nil {
		return zero, false
	}
	if elem, ok := c.items[key]; ok {
		c.order.MoveToFront(elem)
		return elem.Value.(*lruEntry[V]).value, true
	}
	return zero, false
}

func (c *stringLRU[V]) Add(key string, value V) {
	if c == nil {
		return
	}
	if elem, ok := c.items[key]; ok {
		elem.Value.(*lruEntry[V]).value = value
		c.order.MoveToFront(elem)
		return
	}
	elem := c.order.PushFront(&lruEntry[V]{key: key, value: value})
	c.items[key] = elem
	if c.order.Len() > c.capacity {
		tail := c.order.Back()
		if tail != nil {
			c.order.Remove(tail)
			entry := tail.Value.(*lruEntry[V])
			delete(c.items, entry.key)
		}
	}
}
