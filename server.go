package gooctopi

import (
	"fmt"
	"net/http"
)

// NewServer constructs the HTTP handler serving the OctoPi index page.
func NewServer() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "OctoPi")
	})

	return mux
}
