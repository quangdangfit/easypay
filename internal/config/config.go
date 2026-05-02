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
	// PublicBaseURL is the externally-reachable origin of this service. It is
	// used to mint hosted-checkout URLs when LazyCheckout is enabled.
	PublicBaseURL string
	// LazyCheckout, when true, skips the Stripe call on POST /api/payments and
	// returns a self-hosted checkout_url. The actual Stripe Session is created
	// on-demand the first time the URL is opened.
	LazyCheckout bool
	// CheckoutTokenSecret signs /pay/:id?t=<token> URLs to prevent enumeration.
	// When empty, /pay/:id is unauthenticated (dev-only).
	CheckoutTokenSecret string
	// CheckoutTokenTTL bounds how long a hosted-checkout URL stays valid.
	CheckoutTokenTTL time.Duration
	// StripeRateLimit caps Stripe SDK calls fleet-wide via a Redis token
	// bucket. Keep below your Stripe account quota with a safety margin.
	StripeRateLimit int
	// CheckoutDefaultSuccessURL / CancelURL are used when the merchant
	// doesn't supply per-payment success_url/cancel_url. Stripe requires
	// these for `mode=payment` Checkout Sessions.
	CheckoutDefaultSuccessURL string
	CheckoutDefaultCancelURL  string
	// LogicalShardCount caps the number of logical shards merchants are
	// distributed across via `merchants.shard_index`. Today every shard
	// lives on the single `transactions` table; future deployments can
	// partition by this column without changing application code.
	LogicalShardCount uint8
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
	// Mode controls which Stripe client implementation is used:
	//   "live" (default) — real Stripe SDK, hits api.stripe.com
	//   "fake"           — in-process synthetic responses, no network
	// "fake" is intended for load tests and local e2e flows where Stripe's
	// rate limits would dominate the measurement.
	Mode string
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
	HMACSecret        string
	HMACTimestampSkew time.Duration
	// AdminAPIKey gates the /admin/* HTTP routes (e.g. POST /admin/merchants).
	// Compared against the X-Admin-Key request header in constant time.
	// When empty, admin routes are not mounted.
	AdminAPIKey string
}

func Load() (*Config, error) {
	publicBase := strings.TrimRight(getenv("PUBLIC_BASE_URL", "http://localhost:8080"), "/")
	cfg := &Config{
		App: AppConfig{
			Env:                       getenv("APP_ENV", "development"),
			Port:                      getenvInt("APP_PORT", 8080),
			LogLevel:                  getenv("LOG_LEVEL", "info"),
			PublicBaseURL:             publicBase,
			LazyCheckout:              getenvBool("LAZY_CHECKOUT", false),
			CheckoutTokenSecret:       getenv("CHECKOUT_TOKEN_SECRET", ""),
			CheckoutTokenTTL:          time.Duration(getenvInt("CHECKOUT_TOKEN_TTL_SECONDS", 86400)) * time.Second,
			StripeRateLimit:           getenvInt("STRIPE_RATE_LIMIT", 80),
			CheckoutDefaultSuccessURL: getenv("CHECKOUT_DEFAULT_SUCCESS_URL", publicBase+"/checkout/success"),
			CheckoutDefaultCancelURL:  getenv("CHECKOUT_DEFAULT_CANCEL_URL", publicBase+"/checkout/cancel"),
			LogicalShardCount:         shardCount(getenvInt("LOGICAL_SHARD_COUNT", 16)),
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
			TopicConfirmed: getenv("KAFKA_TOPIC_CONFIRMED", "payment.confirmed"),
			TopicDLQ:       getenv("KAFKA_TOPIC_DLQ", "payment.confirmed.dlq"),
			ConsumerGroup:  getenv("KAFKA_CONSUMER_GROUP", "payment-engine"),
		},
		Stripe: StripeConfig{
			SecretKey:       getenv("STRIPE_SECRET_KEY", ""),
			PublishableKey:  getenv("STRIPE_PUBLISHABLE_KEY", ""),
			WebhookSecret:   getenv("STRIPE_WEBHOOK_SECRET", ""),
			APIVersion:      getenv("STRIPE_API_VERSION", "2024-06-20"),
			DefaultCurrency: getenv("STRIPE_DEFAULT_CURRENCY", "USD"),
			Mode:            getenv("STRIPE_MODE", "live"),
		},
		Blockchain: BlockchainConfig{
			RPCWebsocket:          getenv("ETH_RPC_WS", ""),
			RPCHTTP:               getenv("ETH_RPC_HTTP", ""),
			ContractAddress:       getenv("ETH_CONTRACT_ADDRESS", ""),
			ChainID:               int64(getenvInt("ETH_CHAIN_ID", 11155111)),
			RequiredConfirmations: nonNegUint64(getenvInt("ETH_REQUIRED_CONFIRMATIONS", 12)),
			StartBlock:            nonNegUint64(getenvInt("ETH_START_BLOCK", 0)),
		},
		Security: SecurityConfig{
			HMACSecret:        mustGetenv("HMAC_SECRET"),
			HMACTimestampSkew: time.Duration(getenvInt("HMAC_TIMESTAMP_SKEW_SECONDS", 300)) * time.Second,
			AdminAPIKey:       getenv("ADMIN_API_KEY", ""),
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
	switch c.Stripe.Mode {
	case "live":
		if c.Stripe.SecretKey == "" {
			return fmt.Errorf("STRIPE_SECRET_KEY required when STRIPE_MODE=live")
		}
		if c.Stripe.WebhookSecret == "" {
			return fmt.Errorf("STRIPE_WEBHOOK_SECRET required when STRIPE_MODE=live")
		}
	case "fake":
		// no requirements
	default:
		return fmt.Errorf("invalid STRIPE_MODE: %s (allowed: live, fake)", c.Stripe.Mode)
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

func nonNegUint64(v int) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}

// shardCount clamps LOGICAL_SHARD_COUNT into the valid uint8 range
// [1, 255]. The default is applied by the caller.
func shardCount(v int) uint8 {
	if v < 1 {
		return 1
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func getenvBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
