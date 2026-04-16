package config

import "os"

type Config struct {
	Address   string
	UserAgent string
}

func Load() Config {
	return Config{
		Address:   envOrDefault("ATLAS_ADDR", ":8080"),
		UserAgent: envOrDefault("ATLAS_USER_AGENT", "AtlasSearchBot/0.1"),
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
