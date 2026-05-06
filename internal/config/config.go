package config

import (
	"log/slog"
	"os"
	"time"
)

type Config struct {
	Port            string
	ShutdownTimeout time.Duration
	DGraphURL       string
	DGraphAdminURL  string
	RatelURL        string
	Dev             bool
	LogLevel        slog.Level
}

func New() *Config {
	dgraphURL := os.Getenv("DGRAPH_URL")
	if dgraphURL == "" {
		dgraphURL = "http://localhost:8080/graphql"
	}
	dgraphAdminURL := os.Getenv("DGRAPH_ADMIN_URL")
	if dgraphAdminURL == "" {
		dgraphAdminURL = "http://localhost:8080/admin"
	}
	ratelURL := os.Getenv("RATEL_URL")
	if ratelURL == "" {
		ratelURL = "http://localhost:8000"
	}
	logLevel := slog.LevelInfo
	if os.Getenv("ORBITAL_LOG_LEVEL") == "debug" {
		logLevel = slog.LevelDebug
	}

	return &Config{
		Port:            "8001",
		ShutdownTimeout: 10 * time.Second,
		DGraphURL:       dgraphURL,
		DGraphAdminURL:  dgraphAdminURL,
		RatelURL:        ratelURL,
		Dev:             os.Getenv("ORBITAL_DEV") == "true",
		LogLevel:        logLevel,
	}
}
