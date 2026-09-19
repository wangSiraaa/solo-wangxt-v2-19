// Package cache 封装 Redis。按需求 Redis 仅承担查询缓存：
// 缓存池视图等只读接口结果，任何写操作（借/还/调额/回收）后立即失效。
// Redis 不可用时自动降级为直查数据库，绝不影响核心交易。
package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

type Cache struct {
	client *redis.Client
	ttl    time.Duration
}

func New(addr, password string, db int, ttl time.Duration) (*Cache, error) {
	c := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, err
	}
	return &Cache{client: c, ttl: ttl}, nil
}

// Get 命中返回 (value,true)；未命中或 Redis 异常返回 ("",false)。
func (c *Cache) Get(ctx context.Context, key string) (string, bool) {
	if c == nil {
		return "", false
	}
	v, err := c.client.Get(ctx, key).Result()
	if err != nil {
		return "", false
	}
	return v, true
}

// Set 写入失败仅被调用方忽略（缓存不影响正确性）。
func (c *Cache) Set(ctx context.Context, key, val string) {
	if c == nil {
		return
	}
	_ = c.client.Set(ctx, key, val, c.ttl).Err()
}

// Invalidate 写操作后删除相关缓存。
func (c *Cache) Invalidate(ctx context.Context, keys ...string) {
	if c == nil {
		return
	}
	_ = c.client.Del(ctx, keys...).Err()
}

func (c *Cache) Close() error {
	if c == nil {
		return nil
	}
	return errors.Unwrap(c.client.Close())
}
