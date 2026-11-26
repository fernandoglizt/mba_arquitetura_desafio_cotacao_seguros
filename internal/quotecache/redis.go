package quotecache

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/quotation"
)

const operationTimeout = 75 * time.Millisecond

type Redis struct {
	client *redis.Client
}

func NewRedis(address string) *Redis {
	return &Redis{client: redis.NewClient(&redis.Options{
		Addr:                  address,
		DialTimeout:           operationTimeout,
		ReadTimeout:           operationTimeout,
		WriteTimeout:          operationTimeout,
		PoolTimeout:           operationTimeout,
		ContextTimeoutEnabled: true,
		MaxRetries:            -1,
	})}
}

func (r *Redis) Get(ctx context.Context, key string) (quotation.CachedQuote, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	value, err := r.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return quotation.CachedQuote{}, false, nil
	}
	if err != nil {
		return quotation.CachedQuote{}, false, err
	}
	var entry quotation.CachedQuote
	if err := json.Unmarshal(value, &entry); err != nil {
		return quotation.CachedQuote{}, false, err
	}
	return entry, true, nil
}

func (r *Redis) Put(ctx context.Context, key string, entry quotation.CachedQuote, ttl time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	value, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return r.client.Set(ctx, key, value, ttl).Err()
}

func (r *Redis) Close() error {
	return r.client.Close()
}
