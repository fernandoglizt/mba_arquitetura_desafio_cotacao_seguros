package platform

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Resilience struct {
	PartnerTimeout time.Duration
	FailureLimit   int
	FailureWindow  time.Duration
	OpenTime       time.Duration
	CacheTTL       time.Duration
	RedisAddress   string
	CacheKeySecret string
}

func parseResilience(env func(string) string) (Resilience, error) {
	var cfg Resilience
	durations := []struct {
		name     string
		fallback string
		target   *time.Duration
	}{
		{"PARTNER_TIMEOUT", "2s", &cfg.PartnerTimeout},
		{"BREAKER_FAILURE_WINDOW", "12s", &cfg.FailureWindow},
		{"BREAKER_OPEN_TIME", "3s", &cfg.OpenTime},
		{"CACHE_TTL", "4m", &cfg.CacheTTL},
	}
	for _, item := range durations {
		value, err := time.ParseDuration(text(env, item.name, item.fallback))
		if err != nil || value <= 0 {
			return cfg, fmt.Errorf("%s: expected a positive duration", item.name)
		}
		*item.target = value
	}
	if cfg.CacheTTL > 24*time.Hour {
		return cfg, fmt.Errorf("CACHE_TTL: cannot exceed 24h")
	}

	limit, err := strconv.Atoi(text(env, "BREAKER_FAILURE_LIMIT", "3"))
	if err != nil || limit < 1 {
		return cfg, fmt.Errorf("BREAKER_FAILURE_LIMIT: expected a positive integer")
	}
	cfg.FailureLimit = limit
	cfg.RedisAddress = strings.TrimSpace(text(env, "REDIS_ADDR", "localhost:6379"))
	cfg.CacheKeySecret = env("CACHE_KEY_SECRET")
	if cfg.CacheKeySecret != "" && len(cfg.CacheKeySecret) < 32 {
		return cfg, fmt.Errorf("CACHE_KEY_SECRET: expected at least 32 bytes")
	}
	return cfg, nil
}
