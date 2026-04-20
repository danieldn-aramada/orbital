package main

import (
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
)

func graphqlHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		dgraphURL := os.Getenv("DGRAPH_URL")
		if dgraphURL == "" {
			dgraphURL = "http://localhost:8080/graphql"
		}

		// Proxy POST requests to Dgraph GraphQL endpoint
		resp, err := http.Post(dgraphURL, "application/json", r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		defer resp.Body.Close()

		w.Header().Set("Content-Type", "application/json")
		io.Copy(w, resp.Body)
		return
	}
	slog.Info("GET /graphql")

	// Serve GraphiQL HTML for GET requests
	http.ServeFile(w, r, "./static/index.html")
}

func main() {
	// Static files (JS/CSS) served at /
	fs := http.FileServer(http.Dir("./static"))
	http.Handle("/", fs)

	// GraphQL proxy
	http.HandleFunc("/graphql", graphqlHandler)

	log.Println("Listening on :8001")
	log.Fatal(http.ListenAndServe(":8001", nil))
}
