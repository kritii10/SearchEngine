package config

import "os"

type Config struct {
	Address       string
	UserAgent     string
	AIBaseURL     string
	DatabaseURL   string
	StorageDriver string
	RedisAddr     string
	CacheDriver   string
}

func Load() Config {
	return Config{
		Address:       envOrDefault("ATLAS_ADDR", ":8080"),
		UserAgent:     envOrDefault("ATLAS_USER_AGENT", "AtlasSearchBot/0.1"),
		AIBaseURL:     envOrDefault("ATLAS_AI_BASE_URL", "http://127.0.0.1:8001"),
		DatabaseURL:   os.Getenv("ATLAS_DATABASE_URL"),
		StorageDriver: envOrDefault("ATLAS_STORAGE_DRIVER", "memory"),
		RedisAddr:     envOrDefault("ATLAS_REDIS_ADDR", "127.0.0.1:6379"),
		CacheDriver:   envOrDefault("ATLAS_CACHE_DRIVER", "memory"),
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
