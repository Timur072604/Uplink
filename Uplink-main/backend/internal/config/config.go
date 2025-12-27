package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port           string
	DatabaseURL    string
	JWTSecret      string
	DBMaxConns     int32
	AllowedOrigins []string
}

func Load() *Config {
	return &Config{
		Port:           getEnv("PORT", ":8080"),
		DatabaseURL:    getEnv("DATABASE_URL", "postgres://user:pass@db:5432/uplink?sslmode=disable"),
		JWTSecret:      getEnv("JWT_SECRET", "secret"),
		DBMaxConns:     getEnvInt("DB_MAX_CONNS", 25),
		AllowedOrigins: strings.Split(getEnv("ALLOWED_ORIGINS", "*"), ","),
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int32 {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return int32(i)
		}
	}
	return int32(def)
}
