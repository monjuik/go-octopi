package main

import (
	"fmt"
	"log"
	"net/http"

	gooctopi "github.com/monjuik/go-octopi"
)

func main() {
	handler := gooctopi.NewServer()

	port := 8080
	addr := fmt.Sprintf(":%d", port)

	log.Printf("Listening at http://localhost:%d", port)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}
