package main

import (
	"fmt"
	"net/http"
	"os"

	gooctopi "github.com/monjuik/go-octopi"
)

func main() {
	handler := gooctopi.NewServer()

	port := 8080
	addr := fmt.Sprintf(":%d", port)

	logger := gooctopi.NewLogger("web")
	logger.Printf("Listening at http://localhost:%d", port)
	if err := http.ListenAndServe(addr, handler); err != nil {
		logger.Printf("server stopped: %v", err)
		os.Exit(1)
	}
}
