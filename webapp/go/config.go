package isureal

import (
	"net"
	"net/url"
	"os"
	"strconv"
)

type Config struct {
	Region     string
	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	LogLevel   string
	// DB 台のアプリはセッションを変更しないので、無効化を受け取れずキャッシュできない。
	SessionCache  bool
	DBConnections int
}

func ConfigFromEnv() *Config {
	return &Config{
		Region:        envOrDefault("ISUREAL_REGION", "asia"),
		DBHost:        envOrDefault("ISUREAL_DB_HOST", "127.0.0.1"),
		DBPort:        envOrDefault("ISUREAL_DB_PORT", "5432"),
		DBUser:        envOrDefault("ISUREAL_DB_USER", "isureal"),
		DBPassword:    envOrDefault("ISUREAL_DB_PASSWORD", "isureal"),
		DBName:        envOrDefault("ISUREAL_DB_NAME", "isureal"),
		LogLevel:      envOrDefault("ISUREAL_LOG_LEVEL", "debug"),
		SessionCache:  envOrDefault("ISUREAL_SESSION_CACHE", "on") == "on",
		DBConnections: envInt("ISUREAL_DB_CONNECTIONS", 25),
	}
}

func (c *Config) DatabaseDSN() string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.DBUser, c.DBPassword),
		Host:   net.JoinHostPort(c.DBHost, c.DBPort),
		Path:   "/" + c.DBName,
	}
	return u.String()
}

// LocalDatabaseDSN は同じ台の初期画像ストアへの接続先。
func (c *Config) LocalDatabaseDSN() string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.DBUser, c.DBPassword),
		Host:   net.JoinHostPort("127.0.0.1", "5432"),
		Path:   "/" + c.DBName,
	}
	return u.String()
}

func envInt(key string, defaultValue int) int {
	if value, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return value
	}
	return defaultValue
}

func envOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
