package gooctopi

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
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
	defaultFooter         = "© 2025 OctoPi · Made with ❤️ in Cyprus using Bootstrap & ApexCharts"
	lamportsPerSOL        = 1_000_000_000
	heliusAPIKeyEnv       = "HELIUS_API_KEY"
	heliusMainnetTemplate = "https://mainnet.helius-rpc.com/?api-key=%s"
)

var (
	defaultFiatRates     = FiatRates{EUR: 160.0, USD: 180.0}
	defaultServerLogger  = log.New(os.Stdout, "[octopi] ", log.LstdFlags|log.Lmicroseconds)
	solanaAddressPattern = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{32,44}$`)
)

type server struct {
	solanaClient SolanaClient
	logger       *log.Logger
}

type serverConfig struct {
	solanaClient SolanaClient
	logger       *log.Logger
}

func newDefaultSolanaClient() SolanaClient {
	key := os.Getenv(heliusAPIKeyEnv)
	if key == "" {
		log.Fatalf("environment variable %s is required for Helius RPC access", heliusAPIKeyEnv)
	}

	endpoint := fmt.Sprintf(heliusMainnetTemplate, key)
	log.Printf("using Helius Solana RPC endpoint")
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
func WithLogger(l *log.Logger) ServerOption {
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
	Date      string
	Type      string
	AmountSOL string
	Validator string
	Epoch     int
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
		log.Printf("template %s render error: %v", page, err)
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
		Rewards: []RewardRow{
			{Date: "2025-11-07", Type: "Reward", AmountSOL: "0.17", Validator: "Atlas Nodes", Epoch: 562},
			{Date: "2025-11-05", Type: "Reward", AmountSOL: "0.16", Validator: "North Star", Epoch: 561},
			{Date: "2025-11-01", Type: "Reward", AmountSOL: "0.16", Validator: "Atlas Nodes", Epoch: 560},
			{Date: "2025-10-30", Type: "Reward", AmountSOL: "0.15", Validator: "SolGuard", Epoch: 559},
		},
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

	epochInfo, err := s.solanaClient.GetEpochInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("epoch info: %w", err)
	}
	if epochInfo == nil {
		return nil, fmt.Errorf("epoch info: empty response")
	}

	var targetEpoch uint64
	if epochInfo.Epoch == 0 {
		targetEpoch = epochInfo.Epoch
	} else {
		targetEpoch = epochInfo.Epoch - 1
	}

	addresses := make([]string, 0, len(stakeAccounts))
	for _, account := range stakeAccounts {
		addresses = append(addresses, account.Address)
	}

	rewards, err := s.solanaClient.GetInflationReward(ctx, addresses, &targetEpoch)
	if err != nil {
		return nil, fmt.Errorf("inflation reward fetch: %w", err)
	}

	rewardRows := make([]RewardRow, 0, len(rewards))
	for i, reward := range rewards {
		if reward == nil {
			continue
		}
		if reward.Amount == 0 {
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
			Date:      fmt.Sprintf("Epoch %d", reward.Epoch),
			Type:      "Inflation Reward",
			AmountSOL: formatNumber(amountSOL),
			Validator: account.VoteAccount,
			Epoch:     int(reward.Epoch),
		})
	}

	sort.Slice(rewardRows, func(i, j int) bool {
		return rewardRows[i].Epoch > rewardRows[j].Epoch
	})

	return rewardRows, nil
}
