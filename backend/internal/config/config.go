package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Address          string
	DatabaseURL      string
	JWTSecret        string
	AccessTokenTTL   time.Duration
	RefreshTokenTTL  time.Duration
	FrontendDevURL   string
	FrontendDistPath string
}

func Load() (Config, error) {
	cfg := Config{
		Address:          env("APP_ADDRESS", ":8080"),
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		JWTSecret:        os.Getenv("JWT_SECRET"),
		AccessTokenTTL:   durationEnv("ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTokenTTL:  durationEnv("REFRESH_TOKEN_TTL", 30*24*time.Hour),
		FrontendDevURL:   env("FRONTEND_DEV_URL", "http://127.0.0.1:5173"),
		FrontendDistPath: env("FRONTEND_DIST_PATH", "web/dist"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL 不能为空，Owl-Server 仅支持 PostgreSQL")
	}
	if len(cfg.JWTSecret) < 32 {
		return Config{}, fmt.Errorf("JWT_SECRET 至少需要 32 个字符")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if value, err := time.ParseDuration(value); err == nil {
		return value
	}
	return fallback
}
