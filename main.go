package main 

import (
	"log"
	"net/http"
	"os"
)
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK\n"))
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("Hello from sre-go-scratch!\n"))
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	
	mux := http.NewServeMux()
	mux.HandleFunc("/", rootHandler)
	mux.HandleFunc("/healthz", healthHandler)

	addr := ":" + port
	log.Printf("Starting server on %s\n", addr)

	err := http.ListenAndServe(addr, mux)
	if err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}