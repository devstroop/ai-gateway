package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/devstroop/ai-gateway-go/internal/handlers"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handlers.Health)

	addr := ":" + port
	log.Printf("ai-gateway-go listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
	_ = fmt.Sprint
}
