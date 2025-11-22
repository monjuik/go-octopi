package main

import (
	"fmt"
	"net/http"
	"os"
	"strconv"

	gooctopi "github.com/monjuik/go-octopi"
)

const serverPortEnv = "OCTOPI_SERVER_PORT"

func main() {
	logger := gooctopi.NewLogger("web")
	handler := gooctopi.NewServer()

	port := 8080
	if envPort := os.Getenv(serverPortEnv); envPort != "" {
		if parsed, err := strconv.Atoi(envPort); err == nil {
			port = parsed
		} else {
			logger.Printf("invalid %s %q, using default %d: %v", serverPortEnv, envPort, port, err)
		}
	}
	addr := fmt.Sprintf(":%d", port)

	logger.Printf("Listening at http://localhost:%d", port)
	if err := http.ListenAndServe(addr, handler); err != nil {
		logger.Printf("server stopped: %v", err)
		os.Exit(1)
	}
}
