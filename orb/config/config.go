package config

import "time"

type Config struct {
	Port            string
	ShutdownTimeout time.Duration
	DGraphURL       string
}

func New() *Config {
	return &Config{
		Port:            "8001",
		ShutdownTimeout: 10 * time.Second,
		DGraphURL:       "http://localhost:8080/graphql",
	}
}
