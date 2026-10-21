package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	DefaultPort              = "8080"
	DefaultTimeWindowSeconds = 3600
	DefaultAllowedClockSkew  = 1
	DefaultMaxBodyBytes      = 1 << 20
)

type Config struct {
	Port            string
	JWTSecret       string
	TimeTokenSecret string
	TimeWindow      int
	AllowedSkew     int
	MaxBodyBytes    int64
	AllowedOrigins  []string
	PprofAddr       string
}

func Load(getenv func(string) string) (*Config, error) {
	cfg := &Config{
		Port:            valueOr(getenv("PORT"), DefaultPort),
		JWTSecret:       getenv("JWT_SECRET"),
		TimeTokenSecret: getenv("TIME_TOKEN_SECRET"),
		AllowedOrigins:  splitList(getenv("ALLOWED_ORIGINS")),
		PprofAddr:       getenv("PPROF_ADDR"),
	}

	var errs []error
	if cfg.JWTSecret == "" {
		errs = append(errs, errors.New("JWT_SECRET is required"))
	}
	if cfg.TimeTokenSecret == "" {
		errs = append(errs, errors.New("TIME_TOKEN_SECRET is required"))
	}

	var err error
	if cfg.TimeWindow, err = parseInt(getenv, "TIME_WINDOW_SECONDS", DefaultTimeWindowSeconds, 1); err != nil {
		errs = append(errs, err)
	}
	if cfg.AllowedSkew, err = parseInt(getenv, "ALLOWED_CLOCK_SKEW", DefaultAllowedClockSkew, 0); err != nil {
		errs = append(errs, err)
	}
	maxBody, err := parseInt(getenv, "MAX_BODY_BYTES", DefaultMaxBodyBytes, 1)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.MaxBodyBytes = int64(maxBody)

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return cfg, nil
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func parseInt(getenv func(string) string, key string, fallback, minimum int) (int, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", key, raw)
	}
	if value < minimum {
		return 0, fmt.Errorf("%s must be >= %d, got %d", key, minimum, value)
	}
	return value, nil
}

func splitList(raw string) []string {
	var items []string
	for item := range strings.SplitSeq(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}
