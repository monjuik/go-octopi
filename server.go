package gooctopi

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
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
	defaultFooter         = "© 2025 OctoPi · Made with ❤️ using Bootstrap, ApexCharts, Helius, and Go"
	lamportsPerSOL        = 1_000_000_000
	heliusAPIKeyEnv       = "HELIUS_API_KEY"
	heliusMainnetTemplate = "https://mainnet.helius-rpc.com/?api-key=%s"
	rewardLookbackMonths  = 3
	rewardDateFormat      = "2006-01-02 15:04"
	rewardChartMonths     = rewardLookbackMonths + 1
)

var (
	defaultFiatRates     = FiatRates{EUR: 160.0, USD: 180.0}
	defaultServerLogger  = NewLogger("octopi")
	solanaAddressPattern = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{32,44}$`)
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
	key := os.Getenv(heliusAPIKeyEnv)
	if key == "" {
		panic(fmt.Sprintf("environment variable %s is required for Helius RPC access", heliusAPIKeyEnv))
	}

	endpoint := fmt.Sprintf(heliusMainnetTemplate, key)
	defaultServerLogger.Printf("using Helius Solana RPC endpoint")
	return &RPCSolanaClient{
		Endpoint:   endpoint,
		HTTPClient: newRateLimitedHTTPClient(endpoint),
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

	recentRewards, rewardsErr := s.collectRecentRewards(ctx, stakeAccounts)
	if rewardsErr != nil {
		s.logger.Printf("stake rewards fetch warning address=%s error=%v", address, rewardsErr)
	}

	walletView := s.buildWalletData(address, balanceLamports, delegatedLamports)
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
		rewards30d = 2.30228288
		apy        = 8.26
	)

	rates := FiatRates{EUR: 160.0, USD: 180.0}

	totalSOL := liquidSOL + stakedSOL
	balanceCrypto := fmt.Sprintf("%s SOL", formatNumber(totalSOL))
	balanceFiat := fmt.Sprintf("≈ €%s", formatNumber(totalSOL*rates.EUR))

	chartPayload := walletChart{
		Labels: []string{"Aug", "Sep", "Oct", "Nov"},
		Series: []chartSeries{
			{Name: "Rewards (SOL)", Data: []float64{2.05, 2.18, 2.19, 2.40}},
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
			{Label: "30d Rewards", Value: fmt.Sprintf("%s SOL", formatNumber(rewards30d))},
			{
				Label:   "Annual Return",
				Value:   fmt.Sprintf("%.2f%%", apy),
				Subtext: "Projected APY",
				Tooltip: "Calcuated as XIRR over the last 4 months based on staking rewards and deposits.",
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

func (s *server) collectRecentRewards(ctx context.Context, stakeAccounts []StakeAccount) ([]RewardRow, error) {
	if len(stakeAccounts) == 0 {
		return nil, nil
	}

	lookbackStart := rewardWindowStart(time.Now().UTC())
	boundaries, err := s.solanaClient.GetEpochBoundaries(ctx, lookbackStart)
	if err != nil {
		return nil, fmt.Errorf("epoch boundaries: %w", err)
	}
	if len(boundaries) == 0 {
		return nil, nil
	}
	sort.Slice(boundaries, func(i, j int) bool {
		return boundaries[i].Epoch > boundaries[j].Epoch
	})

	targetEpochs := make([]uint64, 0, len(boundaries))
	epochDates := make(map[uint64]time.Time, len(boundaries))
	for _, boundary := range boundaries {
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

	if tips, err := collectJitoTips(ctx, s.ensureTipCollector(), stakeAccounts, epochDates, targetEpochs, boundaries); err != nil {
		return nil, fmt.Errorf("jito tips: %w", err)
	} else if len(tips) > 0 {
		rewardRows = append(rewardRows, tips...)
	}

	sort.Slice(rewardRows, func(i, j int) bool {
		ti := rewardRows[i].Timestamp
		tj := rewardRows[j].Timestamp
		if !ti.IsZero() && !tj.IsZero() && !ti.Equal(tj) {
			return ti.After(tj)
		}
		return rewardRows[i].Epoch > rewardRows[j].Epoch
	})

	return rewardRows, nil
}

func rewardWindowStart(now time.Time) time.Time {
	currentMonthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return currentMonthStart.AddDate(0, -rewardLookbackMonths, 0)
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
	labels := make([]string, rewardChartMonths)
	data := make([]float64, rewardChartMonths)

	for i := 0; i < rewardChartMonths; i++ {
		month := start.AddDate(0, i, 0)
		labels[i] = month.Format("Jan")
	}

	for _, reward := range rewards {
		if reward.Timestamp.IsZero() {
			continue
		}
		month := monthFloor(reward.Timestamp)
		index := monthsBetween(start, month)
		if index < 0 || index >= rewardChartMonths {
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

func monthFloor(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func monthsBetween(start, target time.Time) int {
	return (target.Year()-start.Year())*12 + int(target.Month()) - int(start.Month())
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
