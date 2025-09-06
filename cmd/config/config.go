package config

import (
	"os"
	"strconv"
	"sync"

	"github.com/joho/godotenv"
)

// Configuration constants
const (
	DefaultTimeWindowSeconds = 3600 // 1 hour
	DefaultAllowedClockSkew  = 1    // Allow 1 window of clock skew
)

type Config struct {
	TimeTokenSecret string
	Port            string
	TimeWindow      int
	AllowedSkew     int
	JWTSecret       string
}

var (
	config *Config
	once   sync.Once
)

func Load() *Config {
	once.Do(func() {
		// Load .env file in development
		_ = godotenv.Load()

		timeWindow := getEnvAsInt("TIME_WINDOW_SECONDS", DefaultTimeWindowSeconds)
		allowedSkew := getEnvAsInt("ALLOWED_CLOCK_SKEW", DefaultAllowedClockSkew)

		jwtSecret := getEnv("JWT_SECRET", "")
		if jwtSecret == "" {
			panic("JWT_SECRET environment variable is required")
		}

		config = &Config{
			TimeTokenSecret: getEnv("TIME_TOKEN_SECRET", ""),
			Port:            getEnv("PORT", "8080"),
			TimeWindow:      timeWindow,
			AllowedSkew:     allowedSkew,
			JWTSecret:       jwtSecret,
		}

		if config.TimeTokenSecret == "" {
			panic("TIME_TOKEN_SECRET environment variable is required")
		}
	})
	return config
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvAsInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}
