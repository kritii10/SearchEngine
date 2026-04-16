package config

import "os"

type Config struct {
	Address   string
	UserAgent string
	AIBaseURL string
}

func Load() Config {
	return Config{
		Address:   envOrDefault("ATLAS_ADDR", ":8080"),
		UserAgent: envOrDefault("ATLAS_USER_AGENT", "AtlasSearchBot/0.1"),
		AIBaseURL: envOrDefault("ATLAS_AI_BASE_URL", "http://127.0.0.1:8001"),
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
