package conf

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

func Get(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func MustGet(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("missing required env %s", key))
	}
	return v
}

func GetInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func GetDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// Kafka returns the shared broker/client settings.
func KafkaBrokers() string { return Get("KAFKA_BROKERS", "localhost:9092") }
