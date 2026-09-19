package config

import (
	"os"
	"strconv"
	"time"
)

// Config 汇总所有运行参数，全部可用环境变量覆盖。
type Config struct {
	HTTPAddr      string
	MySQLDSN      string
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	HMACSecret    string
	CacheTTL      time.Duration // Redis 缓存有效期
	ReapInterval  time.Duration // 过期扫描间隔
	Seed          bool          // 是否写入演示数据
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getdur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func Load() Config {
	return Config{
		HTTPAddr:      getenv("HTTP_ADDR", ":8080"),
		MySQLDSN:      getenv("MYSQL_DSN", "root@tcp(127.0.0.1:3306)/licensepool?parseTime=true&loc=Local&charset=utf8mb4"),
		RedisAddr:     getenv("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword: os.Getenv("REDIS_PASSWORD"),
		RedisDB:       atoi("REDIS_DB", 0),
		HMACSecret:    getenv("HMAC_SECRET", "demo-offline-license-signing-secret"),
		CacheTTL:      getdur("CACHE_TTL", 5*time.Second),
		ReapInterval:  getdur("REAP_INTERVAL", 2*time.Second),
		Seed:          getenv("SEED_DATA", "1") != "0",
	}
}

func atoi(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
