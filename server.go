package gooctopi

import (
	"embed"
	"encoding/base64"
	"html/template"
	"net/http"
	"sync"
)

var (
	//go:embed assets/templates/*.html
	templateFS embed.FS

	//go:embed assets/logo.png
	logoBytes []byte

	templates   *template.Template
	logoDataURI template.URL
	loadOnce    sync.Once
)

// TemplateData carries information passed into layout/home templates.
type TemplateData struct {
	Title       string
	Footer      string
	LogoDataURI template.URL
}

// NewServer constructs the HTTP handler serving the OctoPi index page.
func NewServer() http.Handler {
	ensureTemplates()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data := TemplateData{
			Title:       "OctoPi — Home",
			Footer:      "© 2025 OctoPi · Made with ❤️ in Cyprus using Bootstrap & ApexCharts",
			LogoDataURI: logoDataURI,
		}

		if err := templates.ExecuteTemplate(w, "layout", data); err != nil {
			http.Error(w, "template rendering error", http.StatusInternalServerError)
		}
	})

	return mux
}

func ensureTemplates() {
	loadOnce.Do(func() {
		tmpl, err := template.ParseFS(templateFS, "assets/templates/layout.html", "assets/templates/home.html")
		if err != nil {
			panic(err)
		}
		templates = tmpl
		logoDataURI = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(logoBytes))
	})
}
