package config

import (
	"os"
	"time"
)

type Config struct {
	Port            string
	ShutdownTimeout time.Duration
	DGraphURL       string
}

func New() *Config {
	dgraphURL := os.Getenv("DGRAPH_URL")
	if dgraphURL == "" {
		dgraphURL = "http://localhost:8080/graphql"
	}
	return &Config{
		Port:            "8001",
		ShutdownTimeout: 10 * time.Second,
		DGraphURL:       dgraphURL,
	}
}
