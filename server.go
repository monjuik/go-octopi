package gooctopi

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	//go:embed assets/templates/*.html
	templateFS embed.FS

	//go:embed assets/logo.png
	logoBytes []byte

	pageTemplates map[string]*template.Template
	logoDataURI   template.URL
	loadOnce      sync.Once
)

const (
	defaultFooter                = "© 2025 OctoPi · Made with ❤️ using Bootstrap, ApexCharts, Helius, Validators.app, and Go"
	lamportsPerSOL               = 1_000_000_000
	heliusMainnetTemplate        = "https://mainnet.helius-rpc.com/?api-key=%s"
	rewardLookbackWeeks          = 4
	rewardDateFormat             = "2006-01-02 15:04"
	rewardChartWeeks             = rewardLookbackWeeks + 1
	walletRewardMetricWindowDays = 28
	defaultAnnualReturnPercent   = 8.26
	walletAnnualReturnTooltip    = "Calcuated as XIRR over the current week plus the previous 4 weeks based on staking rewards and deposits."
	debugXIRREnv                 = "OCTOPI_DEBUG_XIRR"
)

var (
	defaultFiatRates     = FiatRates{EUR: 160.0, USD: 180.0}
	defaultServerLogger  = NewLogger("octopi")
	solanaAddressPattern = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{32,44}$`)
	debugXIRREnabled     = os.Getenv(debugXIRREnv) == "1"
)

type server struct {
	solanaClient SolanaClient
	logger       Logger
	tipCollector *jitoTipCollector
}

type serverConfig struct {
	solanaClient SolanaClient
	logger       Logger
}

func newDefaultSolanaClient() SolanaClient {
	key := os.Getenv(HeliusAPIKeyEnv)
	if key == "" {
		panic(fmt.Sprintf("environment variable %s is required for Helius RPC access", HeliusAPIKeyEnv))
	}
	if os.Getenv(ValidatorsAPIKeyEnv) == "" {
		panic(fmt.Sprintf("environment variable %s is required for validators.app access", ValidatorsAPIKeyEnv))
	}
	validatorKey := os.Getenv(ValidatorsAPIKeyEnv)

	endpoint := fmt.Sprintf(heliusMainnetTemplate, key)
	defaultServerLogger.Printf("using Helius Solana RPC endpoint")
	return &RPCSolanaClient{
		Endpoint:        endpoint,
		HTTPClient:      newRateLimitedHTTPClient(endpoint),
		Logger:          defaultServerLogger,
		validatorAPIKey: validatorKey,
	}
}

// ServerOption customizes the HTTP server wiring.
type ServerOption func(*serverConfig)

// WithSolanaClient injects a custom Solana RPC client (useful for tests).
func WithSolanaClient(client SolanaClient) ServerOption {
	return func(cfg *serverConfig) {
		cfg.solanaClient = client
	}
}

// WithLogger overrides the server logger.
func WithLogger(l Logger) ServerOption {
	return func(cfg *serverConfig) {
		cfg.logger = l
	}
}

// TemplateData carries information passed into the HTML layout.
type TemplateData struct {
	Title       string
	Footer      string
	LogoDataURI template.URL
	BodyClass   string
	Wallet      *WalletData
}

// WalletData models the mock wallet view.
type WalletData struct {
	Network       string
	Updated       string
	Address       string
	BalanceCrypto string
	BalanceFiat   string
	RateNote      string
	BalanceSOL    float64
	DelegatedSOL  float64
	FiatRates     FiatRates
	Metrics       []WalletMetric
	Rewards       []RewardRow
	ChartJSON     template.JS
}

// FiatRates holds conversion values for SOL.
type FiatRates struct {
	EUR float64
	USD float64
}

// WalletMetric describes a summary card for the wallet page.
type WalletMetric struct {
	Label   string
	Value   string
	Subtext string
	Tooltip string
}

// RewardRow represents a historical reward entry.
type RewardRow struct {
	Date           string
	Type           string
	AmountSOL      string
	AmountSOLValue float64
	Validator      string
	Epoch          int
	Timestamp      time.Time
}

type walletChart struct {
	Labels []string      `json:"labels"`
	Series []chartSeries `json:"series"`
}

type chartSeries struct {
	Name string    `json:"name"`
	Data []float64 `json:"data"`
}

// NewServer constructs the HTTP handler serving the OctoPi web UI.
func NewServer(opts ...ServerOption) http.Handler {
	ensureTemplates()

	cfg := serverConfig{
		solanaClient: newDefaultSolanaClient(),
		logger:       defaultServerLogger,
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	if cfg.solanaClient == nil {
		cfg.solanaClient = newDefaultSolanaClient()
	}
	if cfg.logger == nil {
		cfg.logger = defaultServerLogger
	}
	srv := &server{
		solanaClient: cfg.solanaClient,
		logger:       cfg.logger,
	}
	srv.tipCollector = newJitoTipCollector(cfg.solanaClient, cfg.logger)

	srv.warmupEpochs()

	mux := http.NewServeMux()

	mux.HandleFunc("/", srv.handleHome)
	mux.HandleFunc("/wallet/demo", srv.handleWalletDemo)
	mux.HandleFunc("/wallet/", srv.handleWallet)

	return mux
}

func ensureTemplates() {
	loadOnce.Do(func() {
		base, err := template.ParseFS(templateFS, "assets/templates/layout.html")
		if err != nil {
			panic(err)
		}

		makePageTemplate := func(filename string) *template.Template {
			cloned, err := base.Clone()
			if err != nil {
				panic(err)
			}
			if _, err := cloned.ParseFS(templateFS, filename); err != nil {
				panic(err)
			}
			return cloned
		}

		pageTemplates = map[string]*template.Template{
			"home":   makePageTemplate("assets/templates/home.html"),
			"wallet": makePageTemplate("assets/templates/wallet.html"),
		}

		logoDataURI = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(logoBytes))
	})
}

func (s *server) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	renderTemplate(w, "home", TemplateData{
		Title:       "OctoPi — Home",
		Footer:      defaultFooter,
		LogoDataURI: logoDataURI,
		BodyClass:   "home",
	})
}

func (s *server) handleWalletDemo(w http.ResponseWriter, _ *http.Request) {
	renderTemplate(w, "wallet", TemplateData{
		Title:       "OctoPi — Demo Wallet",
		Footer:      defaultFooter,
		LogoDataURI: logoDataURI,
		BodyClass:   "wallet",
		Wallet:      demoWalletData(),
	})
}

func (s *server) handleWallet(w http.ResponseWriter, r *http.Request) {
	address := strings.TrimPrefix(r.URL.Path, "/wallet/")
	if address == "" {
		http.NotFound(w, r)
		return
	}

	if address == "demo" {
		s.handleWalletDemo(w, r)
		return
	}

	if !solanaAddressPattern.MatchString(address) {
		http.Error(w, "invalid Solana address", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	now := time.Now().UTC()
	lookbackStart := rewardWindowStart(now)
	s.logger.Printf("wallet request start address=%s", address)

	balanceLamports, err := s.solanaClient.GetBalance(ctx, address)
	if err != nil {
		s.logger.Printf("wallet request address=%s error=%v", address, err)
		http.Error(w, "failed to fetch wallet data", http.StatusBadGateway)
		return
	}

	stakeAccounts, err := s.solanaClient.ListStakeAccounts(ctx, address)
	if err != nil {
		s.logger.Printf("stake accounts fetch failed address=%s error=%v", address, err)
	}

	var delegatedLamports uint64
	for _, account := range stakeAccounts {
		delegatedLamports += account.DelegatedLamports
		s.logger.Printf(
			"stake account owner=%s stakeAddress=%s lamports=%d sol=%.9f state=%s vote=%s",
			address,
			account.Address,
			account.DelegatedLamports,
			float64(account.DelegatedLamports)/lamportsPerSOL,
			account.State,
			account.VoteAccount,
		)
	}

	boundaries, boundaryErr := s.solanaClient.GetEpochBoundaries(ctx, lookbackStart)
	if boundaryErr != nil {
		s.logger.Printf("epoch boundaries fetch warning address=%s error=%v", address, boundaryErr)
	}

	recentRewards, rewardsErr := s.collectRecentRewards(ctx, now, stakeAccounts, boundaries)
	if rewardsErr != nil {
		s.logger.Printf("stake rewards fetch warning address=%s error=%v", address, rewardsErr)
	}

	walletView := s.buildWalletData(address, balanceLamports, delegatedLamports)
	var epochInfo *EpochInfo
	if info, err := s.solanaClient.GetEpochInfo(ctx); err != nil {
		s.logger.Printf("epoch info fetch warning address=%s error=%v", address, err)
	} else {
		epochInfo = info
	}

	walletView.Metrics = buildWalletMetrics(now, walletView.DelegatedSOL, recentRewards, boundaries, epochInfo)
	if len(recentRewards) > 0 {
		walletView.Rewards = recentRewards
	}
	if chartJSON, err := buildRewardChartJSON(now, recentRewards); err != nil {
		s.logger.Printf("reward chart build warning address=%s error=%v", address, err)
	} else {
		walletView.ChartJSON = chartJSON
	}

	s.logger.Printf("wallet request success address=%s lamports=%d balanceSOL=%.9f", address, balanceLamports, walletView.BalanceSOL)

	renderTemplate(w, "wallet", TemplateData{
		Title:       fmt.Sprintf("OctoPi — Wallet %s", address),
		Footer:      defaultFooter,
		LogoDataURI: logoDataURI,
		BodyClass:   "wallet",
		Wallet:      walletView,
	})
}

func (s *server) buildWalletData(address string, lamports uint64, delegatedLamports uint64) *WalletData {
	updated := time.Now().UTC().Format("02 Jan 2006 15:04 UTC")
	liquidSOL := float64(lamports) / lamportsPerSOL
	delegatedSOL := float64(delegatedLamports) / lamportsPerSOL
	totalSOL := liquidSOL + delegatedSOL

	return &WalletData{
		Network:       "Solana Mainnet",
		Updated:       updated,
		Address:       address,
		BalanceSOL:    totalSOL,
		DelegatedSOL:  delegatedSOL,
		FiatRates:     defaultFiatRates,
		BalanceCrypto: fmt.Sprintf("%s SOL", formatNumber(totalSOL)),
		BalanceFiat:   fmt.Sprintf("≈ €%s", formatNumber(totalSOL*defaultFiatRates.EUR)),
		RateNote:      "Rates are indicative demo data.",
	}
}

func buildWalletMetrics(now time.Time, delegatedSOL float64, rewards []RewardRow, boundaries []EpochBoundary, epochInfo *EpochInfo) []WalletMetric {
	rewardCutoff := now.AddDate(0, 0, -walletRewardMetricWindowDays)
	rewardSum := sumRewardsSince(rewards, rewardCutoff)
	annualReturn := defaultAnnualReturnPercent

	if delegatedSOL > 0 {
		if computed, ok := computeAnnualReturnPercent(now, delegatedSOL, rewards, boundaries, epochInfo); ok && !math.IsNaN(computed) {
			annualReturn = computed
		}
	}

	return []WalletMetric{
		{
			Label:   "Staked Balance",
			Value:   fmt.Sprintf("%s SOL", formatNumber(delegatedSOL)),
			Subtext: "Currently delegated",
		},
		{
			Label:   "28d Rewards",
			Value:   fmt.Sprintf("%s SOL", formatNumber(rewardSum)),
			Subtext: "Last 28 days",
		},
		{
			Label:   "Annual Return",
			Value:   fmt.Sprintf("%.2f%%", annualReturn),
			Subtext: "Projected APY",
			Tooltip: walletAnnualReturnTooltip,
		},
	}
}

func sumRewardsSince(rewards []RewardRow, cutoff time.Time) float64 {
	var total float64
	for _, reward := range rewards {
		if !reward.Timestamp.IsZero() && reward.Timestamp.Before(cutoff) {
			continue
		}
		total += reward.AmountSOLValue
	}
	return total
}

func computeAnnualReturnPercent(now time.Time, delegatedSOL float64, rewards []RewardRow, boundaries []EpochBoundary, epochInfo *EpochInfo) (float64, bool) {
	if delegatedSOL <= 0 || len(rewards) == 0 {
		return 0, false
	}

	flows, err := buildCashFlows(now, delegatedSOL, rewards, boundaries, epochInfo)
	if err != nil || len(flows) < 2 {
		return 0, false
	}

	logXIRRFlows(flows)
	rate, ok := xirr(flows)
	if !ok {
		return 0, false
	}
	logXIRRRate(rate)
	return rate * 100, true
}

type cashFlow struct {
	when   time.Time
	amount float64
}

func buildCashFlows(now time.Time, delegatedSOL float64, rewards []RewardRow, boundaries []EpochBoundary, epochInfo *EpochInfo) ([]cashFlow, error) {
	earliest := earliestReward(rewards)
	if earliest == nil {
		return nil, fmt.Errorf("no reward timestamps available")
	}

	start, ok := epochStartTime(uint64(earliest.Epoch), boundaries)
	if !ok {
		duration := estimateEpochDuration(boundaries)
		if duration <= 0 {
			duration = 72 * time.Hour
		}
		start = earliest.Timestamp.Add(-duration)
	}

	flows := []cashFlow{
		{when: start, amount: -delegatedSOL},
	}

	for _, reward := range rewards {
		if reward.AmountSOLValue == 0 || reward.Timestamp.IsZero() {
			continue
		}
		flows = append(flows, cashFlow{
			when:   reward.Timestamp,
			amount: reward.AmountSOLValue,
		})
	}

	end := estimateCurrentEpochEnd(now, epochInfo)
	if end.Before(flows[len(flows)-1].when) || end.Equal(flows[len(flows)-1].when) {
		end = flows[len(flows)-1].when.Add(time.Hour)
	}
	flows = append(flows, cashFlow{when: end, amount: delegatedSOL})

	sort.Slice(flows, func(i, j int) bool {
		return flows[i].when.Before(flows[j].when)
	})
	return flows, nil
}

func earliestReward(rewards []RewardRow) *RewardRow {
	var selected *RewardRow
	for i := range rewards {
		reward := &rewards[i]
		if reward.Timestamp.IsZero() || reward.Epoch <= 0 {
			continue
		}
		if selected == nil || reward.Timestamp.Before(selected.Timestamp) {
			selected = reward
		}
	}
	return selected
}

func epochStartTime(epoch uint64, boundaries []EpochBoundary) (time.Time, bool) {
	if epoch == 0 {
		return time.Time{}, false
	}
	target := epoch - 1
	var start time.Time
	for _, boundary := range boundaries {
		if boundary.Epoch != target {
			continue
		}
		if start.IsZero() || boundary.EndTime.After(start) {
			start = boundary.EndTime
		}
	}
	if start.IsZero() {
		return time.Time{}, false
	}
	return start, true
}

func estimateEpochDuration(boundaries []EpochBoundary) time.Duration {
	const fallback = 72 * time.Hour
	if len(boundaries) < 2 {
		return fallback
	}
	sorted := append([]EpochBoundary(nil), boundaries...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].EndTime.After(sorted[j].EndTime)
	})

	var total time.Duration
	count := 0
	for i := 0; i < len(sorted)-1; i++ {
		diff := sorted[i].EndTime.Sub(sorted[i+1].EndTime)
		if diff <= 0 {
			continue
		}
		total += diff
		count++
	}
	if count == 0 {
		return fallback
	}
	return total / time.Duration(count)
}

func estimateCurrentEpochEnd(now time.Time, info *EpochInfo) time.Time {
	const fallback = 24 * time.Hour
	if info == nil || info.SlotsInEpoch == 0 {
		return now.Add(fallback)
	}
	slotsRemaining := info.SlotsInEpoch
	if info.SlotIndex < info.SlotsInEpoch {
		slotsRemaining = info.SlotsInEpoch - info.SlotIndex
	}
	if slotsRemaining <= 0 {
		return now.Add(fallback)
	}
	duration := max(time.Duration(slotsRemaining)*approxSlotDuration, time.Hour)
	return now.Add(duration)
}

func xirr(flows []cashFlow) (float64, bool) {
	if len(flows) < 2 {
		return 0, false
	}
	if rate, ok := xirrNewton(flows, 0.1); ok {
		return rate, true
	}
	low, high, ok := bracketXIRR(flows)
	if !ok {
		return 0, false
	}
	return bisectXIRR(flows, low, high)
}

func xirrNewton(flows []cashFlow, guess float64) (float64, bool) {
	rate := guess
	for range 100 {
		npv := xnpv(rate, flows)
		if math.Abs(npv) < 1e-8 {
			return rate, true
		}
		derivative := xnpvDerivative(rate, flows)
		if derivative == 0 || math.IsNaN(derivative) {
			break
		}
		next := rate - npv/derivative
		if math.IsNaN(next) || math.IsInf(next, 0) || next <= -0.999999 {
			break
		}
		if math.Abs(next-rate) < 1e-8 {
			return next, true
		}
		rate = next
	}
	return 0, false
}

func bracketXIRR(flows []cashFlow) (float64, float64, bool) {
	points := []float64{-0.9, -0.5, -0.2, 0.0, 0.05, 0.1, 0.2, 0.5, 1.0, 2.0, 5.0, 7.5, 10.0}
	prev := points[0]
	prevNPV := xnpv(prev, flows)
	for _, point := range points[1:] {
		currentNPV := xnpv(point, flows)
		if math.IsNaN(prevNPV) || math.IsNaN(currentNPV) {
			prev = point
			prevNPV = currentNPV
			continue
		}
		if prevNPV == 0 {
			return prev, prev, true
		}
		if prevNPV*currentNPV < 0 {
			return prev, point, true
		}
		prev = point
		prevNPV = currentNPV
	}
	return 0, 0, false
}

func bisectXIRR(flows []cashFlow, low, high float64) (float64, bool) {
	npvLow := xnpv(low, flows)
	npvHigh := xnpv(high, flows)
	if math.IsNaN(npvLow) || math.IsNaN(npvHigh) || npvLow*npvHigh > 0 {
		return 0, false
	}
	for i := 0; i < 100; i++ {
		mid := (low + high) / 2
		npvMid := xnpv(mid, flows)
		if math.IsNaN(npvMid) {
			return 0, false
		}
		if math.Abs(npvMid) < 1e-8 || math.Abs(high-low) < 1e-7 {
			return mid, true
		}
		if npvLow*npvMid < 0 {
			high = mid
			npvHigh = npvMid
		} else {
			low = mid
			npvLow = npvMid
		}
	}
	return (low + high) / 2, true
}

func xnpv(rate float64, flows []cashFlow) float64 {
	factor := 1 + rate
	if factor <= 0 {
		return math.NaN()
	}
	base := flows[0].when
	var total float64
	for _, flow := range flows {
		years := flow.when.Sub(base).Hours() / (24 * 365.0)
		discount := math.Pow(factor, years)
		if discount == 0 {
			return math.NaN()
		}
		total += flow.amount / discount
	}
	return total
}

func xnpvDerivative(rate float64, flows []cashFlow) float64 {
	factor := 1 + rate
	if factor <= 0 {
		return math.NaN()
	}
	base := flows[0].when
	var total float64
	for _, flow := range flows {
		years := flow.when.Sub(base).Hours() / (24 * 365.0)
		discount := math.Pow(factor, years+1)
		if discount == 0 {
			return math.NaN()
		}
		total += -years * flow.amount / discount
	}
	return total
}

func logXIRRFlows(flows []cashFlow) {
	if !debugXIRREnabled {
		return
	}
	defaultServerLogger.Printf("xirr debug flows=%d (timestamp,amount)", len(flows))
	for _, flow := range flows {
		defaultServerLogger.Printf("xirr-flow,%s,%s", formatXIRRTimestamp(flow.when), formatXIRRAmount(flow.amount))
	}
}

func logXIRRRate(rate float64) {
	if !debugXIRREnabled {
		return
	}
	defaultServerLogger.Printf("xirr result rate=%s (%.2f%%)", formatXIRRAmount(rate), rate*100)
}

func formatXIRRTimestamp(ts time.Time) string {
	return ts.Format("02.01.2006 15:04:05")
}

func formatXIRRAmount(value float64) string {
	amount := fmt.Sprintf("%.9f", value)
	return strings.Replace(amount, ".", ",", 1)
}

func renderTemplate(w http.ResponseWriter, page string, data TemplateData) {
	tmpl, ok := pageTemplates[page]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		defaultServerLogger.Printf("template %s render error: %v", page, err)
		http.Error(w, "template rendering error", http.StatusInternalServerError)
	}
}

func demoWalletData() *WalletData {
	const (
		liquidSOL  = 340.456647705
		stakedSOL  = 340.454186848
		rewards28d = 2.30228288
		apy        = defaultAnnualReturnPercent
	)

	rates := FiatRates{EUR: 160.0, USD: 180.0}

	totalSOL := liquidSOL + stakedSOL
	balanceCrypto := fmt.Sprintf("%s SOL", formatNumber(totalSOL))
	balanceFiat := fmt.Sprintf("≈ €%s", formatNumber(totalSOL*rates.EUR))

	chartPayload := walletChart{
		Labels: []string{"Oct 13", "Oct 20", "Oct 27", "Nov 03", "Nov 10"},
		Series: []chartSeries{
			{Name: "Rewards (SOL)", Data: []float64{0.45, 0.52, 0.50, 0.63, 0.18}},
		},
	}
	chartJSON, err := json.Marshal(chartPayload)
	if err != nil {
		panic(err)
	}

	rewardEntries := []RewardRow{
		{
			Date:           formatRewardDate(time.Date(2025, time.November, 7, 13, 40, 0, 0, time.UTC), 562),
			Type:           "Reward",
			AmountSOL:      "0.17",
			AmountSOLValue: 0.17,
			Validator:      "Atlas Nodes",
			Epoch:          562,
			Timestamp:      time.Date(2025, time.November, 7, 13, 40, 0, 0, time.UTC),
		},
		{
			Date:           formatRewardDate(time.Date(2025, time.November, 5, 8, 15, 0, 0, time.UTC), 561),
			Type:           "Reward",
			AmountSOL:      "0.16",
			AmountSOLValue: 0.16,
			Validator:      "North Star",
			Epoch:          561,
			Timestamp:      time.Date(2025, time.November, 5, 8, 15, 0, 0, time.UTC),
		},
		{
			Date:           formatRewardDate(time.Date(2025, time.November, 1, 11, 5, 0, 0, time.UTC), 560),
			Type:           "Reward",
			AmountSOL:      "0.16",
			AmountSOLValue: 0.16,
			Validator:      "Atlas Nodes",
			Epoch:          560,
			Timestamp:      time.Date(2025, time.November, 1, 11, 5, 0, 0, time.UTC),
		},
		{
			Date:           formatRewardDate(time.Date(2025, time.October, 30, 6, 50, 0, 0, time.UTC), 559),
			Type:           "Reward",
			AmountSOL:      "0.15",
			AmountSOLValue: 0.15,
			Validator:      "SolGuard",
			Epoch:          559,
			Timestamp:      time.Date(2025, time.October, 30, 6, 50, 0, 0, time.UTC),
		},
	}

	return &WalletData{
		Network:       "Solana Mainnet",
		Updated:       "09 Nov 2025 13:40 UTC",
		Address:       "3k6...tG8Z",
		BalanceCrypto: balanceCrypto,
		BalanceFiat:   balanceFiat,
		BalanceSOL:    totalSOL,
		DelegatedSOL:  stakedSOL,
		FiatRates:     rates,
		Metrics: []WalletMetric{
			{Label: "Staked Balance", Value: fmt.Sprintf("%s SOL", formatNumber(stakedSOL)), Subtext: "Across 12 validators"},
			{Label: "28d Rewards", Value: fmt.Sprintf("%s SOL", formatNumber(rewards28d))},
			{
				Label:   "Annual Return",
				Value:   fmt.Sprintf("%.2f%%", apy),
				Subtext: "Projected APY",
				Tooltip: walletAnnualReturnTooltip,
			},
		},
		Rewards:   rewardEntries,
		ChartJSON: template.JS(chartJSON),
	}
}

func formatNumber(value float64) string {
	s := fmt.Sprintf("%.9f", value)
	return string(s)
}

func (s *server) collectRecentRewards(ctx context.Context, now time.Time, stakeAccounts []StakeAccount, boundaries []EpochBoundary) ([]RewardRow, error) {
	if len(stakeAccounts) == 0 || len(boundaries) == 0 {
		return nil, nil
	}

	windowStart := rewardWindowStart(now)
	windowEnd := weekFloor(now).AddDate(0, 0, 7)

	relevantBoundaries := make([]EpochBoundary, 0, len(boundaries))
	for _, boundary := range boundaries {
		if boundary.EndTime.IsZero() || !boundary.EndTime.Before(windowStart) {
			relevantBoundaries = append(relevantBoundaries, boundary)
		}
	}
	if len(relevantBoundaries) == 0 {
		return nil, nil
	}

	sortedBoundaries := append([]EpochBoundary(nil), relevantBoundaries...)
	sort.Slice(sortedBoundaries, func(i, j int) bool {
		return sortedBoundaries[i].Epoch > sortedBoundaries[j].Epoch
	})

	targetEpochs := make([]uint64, 0, len(sortedBoundaries))
	epochDates := make(map[uint64]time.Time, len(sortedBoundaries))
	for _, boundary := range sortedBoundaries {
		targetEpochs = append(targetEpochs, boundary.Epoch)
		epochDates[boundary.Epoch] = boundary.EndTime
	}

	addresses := make([]string, 0, len(stakeAccounts))
	for _, account := range stakeAccounts {
		addresses = append(addresses, account.Address)
	}

	rewardRows := make([]RewardRow, 0, len(stakeAccounts)*len(targetEpochs))
	for _, epoch := range targetEpochs {
		rewards, err := s.solanaClient.GetInflationReward(ctx, addresses, &epoch)
		if err != nil {
			return nil, fmt.Errorf("inflation reward fetch epoch %d: %w", epoch, err)
		}

		for i, reward := range rewards {
			if reward == nil || reward.Amount == 0 {
				continue
			}

			account := stakeAccounts[i]
			amountSOL := float64(reward.Amount) / float64(lamportsPerSOL)

			s.logger.Printf(
				"inflation reward stake=%s epoch=%d lamports=%d sol=%.9f postBalance=%d commission=%v vote=%s effectiveSlot=%d",
				account.Address,
				reward.Epoch,
				reward.Amount,
				amountSOL,
				reward.PostBalance,
				reward.Commission,
				account.VoteAccount,
				reward.EffectiveSlot,
			)

			rewardRows = append(rewardRows, RewardRow{
				Date:           formatRewardDate(epochDates[reward.Epoch], reward.Epoch),
				Type:           "Staking Reward",
				AmountSOL:      formatNumber(amountSOL),
				AmountSOLValue: amountSOL,
				Validator:      account.VoteAccount,
				Epoch:          int(reward.Epoch),
				Timestamp:      epochDates[reward.Epoch],
			})
		}
	}

	if tips, err := collectJitoTips(ctx, s.ensureTipCollector(), stakeAccounts, epochDates, targetEpochs, sortedBoundaries); err != nil {
		return nil, fmt.Errorf("jito tips: %w", err)
	} else if len(tips) > 0 {
		rewardRows = append(rewardRows, tips...)
	}

	filtered := rewardRows[:0]
	for _, reward := range rewardRows {
		ts := reward.Timestamp
		if ts.IsZero() {
			continue
		}
		if ts.Before(windowStart) || !ts.Before(windowEnd) {
			continue
		}
		filtered = append(filtered, reward)
	}
	if len(filtered) == 0 {
		return nil, nil
	}

	sort.Slice(filtered, func(i, j int) bool {
		ti := filtered[i].Timestamp
		tj := filtered[j].Timestamp
		if !ti.IsZero() && !tj.IsZero() && !ti.Equal(tj) {
			return ti.After(tj)
		}
		return filtered[i].Epoch > filtered[j].Epoch
	})

	s.decorateRewardValidators(ctx, filtered)

	return filtered, nil
}

func (s *server) decorateRewardValidators(ctx context.Context, rewards []RewardRow) {
	if s == nil || s.solanaClient == nil || len(rewards) == 0 {
		return
	}

	cache := make(map[string]string)
	for i := range rewards {
		id := strings.TrimSpace(rewards[i].Validator)
		if id == "" {
			continue
		}

		name, cached := cache[id]
		if !cached {
			resolved, err := s.solanaClient.LookupValidatorName(ctx, id)
			if err != nil && s.logger != nil {
				s.logger.Printf("validator lookup warning id=%s error=%v", id, err)
			}
			name = resolved
			cache[id] = resolved
		}

		if name != "" {
			rewards[i].Validator = name
		}
	}
}

func rewardWindowStart(now time.Time) time.Time {
	currentWeekStart := weekFloor(now)
	return currentWeekStart.AddDate(0, 0, -7*rewardLookbackWeeks)
}

func formatRewardDate(ts time.Time, epoch uint64) string {
	if ts.IsZero() {
		return fmt.Sprintf("Epoch %d", epoch)
	}
	return ts.UTC().Format(rewardDateFormat) + " UTC"
}

func buildRewardChartJSON(now time.Time, rewards []RewardRow) (template.JS, error) {
	payload := rewardChartPayload(now, rewards)
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return template.JS(data), nil
}

func rewardChartPayload(now time.Time, rewards []RewardRow) walletChart {
	start := rewardWindowStart(now)
	labels := make([]string, rewardChartWeeks)
	data := make([]float64, rewardChartWeeks)

	for i := 0; i < rewardChartWeeks; i++ {
		week := start.AddDate(0, 0, i*7)
		labels[i] = week.Format("Jan 02")
	}

	for _, reward := range rewards {
		if reward.Timestamp.IsZero() {
			continue
		}
		week := weekFloor(reward.Timestamp)
		index := weeksBetween(start, week)
		if index < 0 || index >= rewardChartWeeks {
			continue
		}
		data[index] += reward.AmountSOLValue
	}

	return walletChart{
		Labels: labels,
		Series: []chartSeries{
			{
				Name: "Rewards (SOL)",
				Data: data,
			},
		},
	}
}

func weekFloor(t time.Time) time.Time {
	t = t.UTC()
	weekday := int(t.Weekday())
	// Align to Monday as the start of the week.
	daysSinceMonday := (weekday + 6) % 7
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -daysSinceMonday)
}

func weeksBetween(start, target time.Time) int {
	const week = 7 * 24 * time.Hour
	return int(target.Sub(start) / week)
}

func (s *server) warmupEpochs() {
	if s.solanaClient == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	windowStart := rewardWindowStart(time.Now().UTC())
	if s.logger != nil {
		s.logger.Printf("epoch warmup start windowStart=%s", windowStart.Format(time.RFC3339))
	}

	boundaries, err := s.solanaClient.GetEpochBoundaries(ctx, windowStart)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("epoch warmup warning windowStart=%s error=%v", windowStart.Format(time.RFC3339), err)
		}
		return
	}

	if s.logger != nil {
		s.logger.Printf("epoch warmup complete windowStart=%s epochs=%d", windowStart.Format(time.RFC3339), len(boundaries))
	}
}

func (s *server) ensureTipCollector() *jitoTipCollector {
	if s.tipCollector != nil {
		return s.tipCollector
	}
	if s.solanaClient == nil {
		return nil
	}
	s.tipCollector = newJitoTipCollector(s.solanaClient, s.logger)
	return s.tipCollector
}
