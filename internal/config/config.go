package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	App        AppConfig
	DB         DBConfig
	Redis      RedisConfig
	Kafka      KafkaConfig
	Stripe     StripeConfig
	Blockchain BlockchainConfig
	Security   SecurityConfig
}

type AppConfig struct {
	Env      string
	Port     int
	LogLevel string
}

type DBConfig struct {
	DSN          string
	MaxOpenConns int
	MaxIdleConns int
}

type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

type KafkaConfig struct {
	Brokers        []string
	TopicEvents    string
	TopicConfirmed string
	TopicDLQ       string
	ConsumerGroup  string
}

type StripeConfig struct {
	SecretKey       string
	PublishableKey  string
	WebhookSecret   string
	APIVersion      string
	DefaultCurrency string
}

type BlockchainConfig struct {
	RPCWebsocket          string
	RPCHTTP               string
	ContractAddress       string
	ChainID               int64
	RequiredConfirmations uint64
	StartBlock            uint64
}

type SecurityConfig struct {
	HMACSecret           string
	HMACTimestampSkew    time.Duration
}

func Load() (*Config, error) {
	cfg := &Config{
		App: AppConfig{
			Env:      getenv("APP_ENV", "development"),
			Port:     getenvInt("APP_PORT", 8080),
			LogLevel: getenv("LOG_LEVEL", "info"),
		},
		DB: DBConfig{
			DSN:          mustGetenv("DB_DSN"),
			MaxOpenConns: getenvInt("DB_MAX_OPEN_CONNS", 100),
			MaxIdleConns: getenvInt("DB_MAX_IDLE_CONNS", 25),
		},
		Redis: RedisConfig{
			Addr:     getenv("REDIS_ADDR", "localhost:6379"),
			Password: getenv("REDIS_PASSWORD", ""),
			DB:       getenvInt("REDIS_DB", 0),
		},
		Kafka: KafkaConfig{
			Brokers:        strings.Split(getenv("KAFKA_BROKERS", "localhost:9092"), ","),
			TopicEvents:    getenv("KAFKA_TOPIC_EVENTS", "payment.events"),
			TopicConfirmed: getenv("KAFKA_TOPIC_CONFIRMED", "payment.confirmed"),
			TopicDLQ:       getenv("KAFKA_TOPIC_DLQ", "payment.events.dlq"),
			ConsumerGroup:  getenv("KAFKA_CONSUMER_GROUP", "payment-engine"),
		},
		Stripe: StripeConfig{
			SecretKey:       mustGetenv("STRIPE_SECRET_KEY"),
			PublishableKey:  getenv("STRIPE_PUBLISHABLE_KEY", ""),
			WebhookSecret:   mustGetenv("STRIPE_WEBHOOK_SECRET"),
			APIVersion:      getenv("STRIPE_API_VERSION", "2024-06-20"),
			DefaultCurrency: getenv("STRIPE_DEFAULT_CURRENCY", "USD"),
		},
		Blockchain: BlockchainConfig{
			RPCWebsocket:          getenv("ETH_RPC_WS", ""),
			RPCHTTP:               getenv("ETH_RPC_HTTP", ""),
			ContractAddress:       getenv("ETH_CONTRACT_ADDRESS", ""),
			ChainID:               int64(getenvInt("ETH_CHAIN_ID", 11155111)),
			RequiredConfirmations: uint64(getenvInt("ETH_REQUIRED_CONFIRMATIONS", 12)),
			StartBlock:            uint64(getenvInt("ETH_START_BLOCK", 0)),
		},
		Security: SecurityConfig{
			HMACSecret:        mustGetenv("HMAC_SECRET"),
			HMACTimestampSkew: time.Duration(getenvInt("HMAC_TIMESTAMP_SKEW_SECONDS", 300)) * time.Second,
		},
	}

	if err := validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func validate(c *Config) error {
	if c.App.Port <= 0 || c.App.Port > 65535 {
		return fmt.Errorf("invalid APP_PORT: %d", c.App.Port)
	}
	if len(c.Security.HMACSecret) < 16 {
		return fmt.Errorf("HMAC_SECRET must be at least 16 chars")
	}
	return nil
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func mustGetenv(key string) string {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		panic(fmt.Sprintf("required env var %s is not set", key))
	}
	return v
}

func getenvInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
