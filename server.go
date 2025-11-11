package gooctopi

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sync"
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

const defaultFooter = "© 2025 OctoPi · Made with ❤️ in Cyprus using Bootstrap & ApexCharts"

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
func NewServer() http.Handler {
	ensureTemplates()

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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
	})

	mux.HandleFunc("/wallet/demo", func(w http.ResponseWriter, r *http.Request) {
		renderTemplate(w, "wallet", TemplateData{
			Title:       "OctoPi — Demo Wallet",
			Footer:      defaultFooter,
			LogoDataURI: logoDataURI,
			BodyClass:   "wallet",
			Wallet:      demoWalletData(),
		})
	})

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

func renderTemplate(w http.ResponseWriter, page string, data TemplateData) {
	tmpl, ok := pageTemplates[page]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, "template rendering error", http.StatusInternalServerError)
	}
}

func demoWalletData() *WalletData {
	const (
		balanceSOL = 340.456647705
		stakedSOL  = 340.454186848
		rewards30d = 2.30228288
		apy        = 8.26
	)

	rates := FiatRates{EUR: 160.0, USD: 180.0}

	balanceCrypto := fmt.Sprintf("%s SOL", formatNumber(balanceSOL))
	balanceFiat := fmt.Sprintf("≈ €%s", formatNumber(balanceSOL*rates.EUR))

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
		BalanceSOL:    balanceSOL,
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
