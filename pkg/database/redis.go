package database

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"depin-backend/internal/config"
)

// NewRedisClient initializes a connection to Redis.
func NewRedisClient(cfg *config.Config) (*redis.Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%s", cfg.RedisHost, cfg.RedisPort),
		Password: cfg.RedisPassword,
		DB:       0, // Default DB
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("failed to ping redis: %w", err)
	}

	return rdb, nil
}
