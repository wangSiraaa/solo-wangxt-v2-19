package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache 仅做查询缓存,绝不参与席位扣减等一致性决策。
// 写操作后主动失效相关键;查询未命中回源 MySQL。
type Cache struct {
	rdb *redis.Client
	ttl time.Duration
}

func NewCache(addr string) *Cache {
	return &Cache{
		rdb: redis.NewClient(&redis.Options{Addr: addr}),
		ttl: 10 * time.Second,
	}
}

func (c *Cache) Ping(ctx context.Context) error { return c.rdb.Ping(ctx).Err() }

// GetJSON 未命中返回 false,不视为错误。
func (c *Cache) GetJSON(ctx context.Context, key string, dst any) (bool, error) {
	b, err := c.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(b, dst)
}

func (c *Cache) SetJSON(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, b, c.ttl).Err()
}

// Invalidate 删除给定前缀的所有缓存键。
func (c *Cache) Invalidate(ctx context.Context, prefixes ...string) {
	for _, p := range prefixes {
		var cursor uint64
		for {
			keys, next, err := c.rdb.Scan(ctx, cursor, p+"*", 100).Result()
			if err != nil {
				return
			}
			if len(keys) > 0 {
				c.rdb.Del(ctx, keys...)
			}
			if next == 0 {
				break
			}
			cursor = next
		}
	}
}
