package config

import (
	"os"
)

// Config 运行配置。凭证签名密钥仅用于本地模拟,集中保存在服务端。
type Config struct {
	HTTPAddr       string
	MySQLDSN       string
	RedisAddr      string
	CredentialHMAC string
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func Load() Config {
	return Config{
		HTTPAddr:       getenv("HTTP_ADDR", ":8080"),
		MySQLDSN:       getenv("MYSQL_DSN", "app:apppass@tcp(127.0.0.1:3306)/license?parseTime=true&charset=utf8mb4&loc=Local"),
		RedisAddr:      getenv("REDIS_ADDR", "127.0.0.1:6379"),
		CredentialHMAC: getenv("CREDENTIAL_HMAC", "demo-local-hmac-secret-do-not-use-in-prod"),
	}
}
