package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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

// rawConfig mirrors Config but uses YAML-friendly field types (string for
// durations, int for sizes that we later clamp). It exists so the public
// Config keeps native Go types (time.Duration, uint8) while the YAML file
// stays human-readable ("5m", "24h").
type rawConfig struct {
	App        rawAppConfig        `yaml:"app"`
	DB         rawDBConfig         `yaml:"db"`
	Redis      rawRedisConfig      `yaml:"redis"`
	Kafka      rawKafkaConfig      `yaml:"kafka"`
	Stripe     rawStripeConfig     `yaml:"stripe"`
	Blockchain rawBlockchainConfig `yaml:"blockchain"`
	Security   rawSecurityConfig   `yaml:"security"`
}

type rawAppConfig struct {
	Env                       string `yaml:"env"`
	Port                      int    `yaml:"port"`
	LogLevel                  string `yaml:"log_level"`
	PublicBaseURL             string `yaml:"public_base_url"`
	LazyCheckout              bool   `yaml:"lazy_checkout"`
	CheckoutTokenSecret       string `yaml:"checkout_token_secret"`
	CheckoutTokenTTL          string `yaml:"checkout_token_ttl"`
	StripeRateLimit           int    `yaml:"stripe_rate_limit"`
	CheckoutDefaultSuccessURL string `yaml:"checkout_default_success_url"`
	CheckoutDefaultCancelURL  string `yaml:"checkout_default_cancel_url"`
	LogicalShardCount         int    `yaml:"logical_shard_count"`
}

type rawDBConfig struct {
	DSN          string `yaml:"dsn"`
	MaxOpenConns int    `yaml:"max_open_conns"`
	MaxIdleConns int    `yaml:"max_idle_conns"`
}

type rawRedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type rawKafkaConfig struct {
	Brokers        []string `yaml:"brokers"`
	TopicConfirmed string   `yaml:"topic_confirmed"`
	TopicDLQ       string   `yaml:"topic_dlq"`
	ConsumerGroup  string   `yaml:"consumer_group"`
}

type rawStripeConfig struct {
	SecretKey       string `yaml:"secret_key"`
	PublishableKey  string `yaml:"publishable_key"`
	WebhookSecret   string `yaml:"webhook_secret"`
	APIVersion      string `yaml:"api_version"`
	DefaultCurrency string `yaml:"default_currency"`
	Mode            string `yaml:"mode"`
}

type rawBlockchainConfig struct {
	RPCWebsocket          string `yaml:"rpc_websocket"`
	RPCHTTP               string `yaml:"rpc_http"`
	ContractAddress       string `yaml:"contract_address"`
	ChainID               int64  `yaml:"chain_id"`
	RequiredConfirmations int64  `yaml:"required_confirmations"`
	StartBlock            int64  `yaml:"start_block"`
}

type rawSecurityConfig struct {
	HMACSecret        string `yaml:"hmac_secret"`
	HMACTimestampSkew string `yaml:"hmac_timestamp_skew"`
	AdminAPIKey       string `yaml:"admin_api_key"`
}

// Load reads and parses the YAML config at path. Unknown fields are
// rejected so typos surface immediately. All defaults applied here match
// the previous .env-based loader; if the YAML omits a field, the default
// kicks in.
func Load(path string) (*Config, error) {
	if path == "" {
		return nil, fmt.Errorf("config path is empty")
	}
	// path is operator-supplied via the --config flag; reading it is the
	// whole point of this function.
	data, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var raw rawConfig
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	cfg, err := raw.toConfig()
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if err := validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (r *rawConfig) toConfig() (*Config, error) {
	publicBase := strings.TrimRight(stringDefault(r.App.PublicBaseURL, "http://localhost:8080"), "/")

	tokenTTL, err := parseDurationDefault(r.App.CheckoutTokenTTL, 24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("app.checkout_token_ttl: %w", err)
	}
	hmacSkew, err := parseDurationDefault(r.Security.HMACTimestampSkew, 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("security.hmac_timestamp_skew: %w", err)
	}

	cfg := &Config{
		App: AppConfig{
			Env:                       stringDefault(r.App.Env, "development"),
			Port:                      intDefault(r.App.Port, 8080),
			LogLevel:                  stringDefault(r.App.LogLevel, "info"),
			PublicBaseURL:             publicBase,
			LazyCheckout:              r.App.LazyCheckout,
			CheckoutTokenSecret:       r.App.CheckoutTokenSecret,
			CheckoutTokenTTL:          tokenTTL,
			StripeRateLimit:           intDefault(r.App.StripeRateLimit, 80),
			CheckoutDefaultSuccessURL: stringDefault(r.App.CheckoutDefaultSuccessURL, publicBase+"/checkout/success"),
			CheckoutDefaultCancelURL:  stringDefault(r.App.CheckoutDefaultCancelURL, publicBase+"/checkout/cancel"),
			LogicalShardCount:         shardCount(intDefault(r.App.LogicalShardCount, 16)),
		},
		DB: DBConfig{
			DSN:          r.DB.DSN,
			MaxOpenConns: intDefault(r.DB.MaxOpenConns, 100),
			MaxIdleConns: intDefault(r.DB.MaxIdleConns, 25),
		},
		Redis: RedisConfig{
			Addr:     stringDefault(r.Redis.Addr, "localhost:6379"),
			Password: r.Redis.Password,
			DB:       r.Redis.DB,
		},
		Kafka: KafkaConfig{
			Brokers:        sliceDefault(r.Kafka.Brokers, []string{"localhost:9092"}),
			TopicConfirmed: stringDefault(r.Kafka.TopicConfirmed, "payment.confirmed"),
			TopicDLQ:       stringDefault(r.Kafka.TopicDLQ, "payment.confirmed.dlq"),
			ConsumerGroup:  stringDefault(r.Kafka.ConsumerGroup, "payment-engine"),
		},
		Stripe: StripeConfig{
			SecretKey:       r.Stripe.SecretKey,
			PublishableKey:  r.Stripe.PublishableKey,
			WebhookSecret:   r.Stripe.WebhookSecret,
			APIVersion:      stringDefault(r.Stripe.APIVersion, "2024-06-20"),
			DefaultCurrency: stringDefault(r.Stripe.DefaultCurrency, "USD"),
			Mode:            stringDefault(r.Stripe.Mode, "live"),
		},
		Blockchain: BlockchainConfig{
			RPCWebsocket:          r.Blockchain.RPCWebsocket,
			RPCHTTP:               r.Blockchain.RPCHTTP,
			ContractAddress:       r.Blockchain.ContractAddress,
			ChainID:               int64Default(r.Blockchain.ChainID, 11155111),
			RequiredConfirmations: nonNegUint64(int64Default(r.Blockchain.RequiredConfirmations, 12)),
			StartBlock:            nonNegUint64(r.Blockchain.StartBlock),
		},
		Security: SecurityConfig{
			HMACSecret:        r.Security.HMACSecret,
			HMACTimestampSkew: hmacSkew,
			AdminAPIKey:       r.Security.AdminAPIKey,
		},
	}
	return cfg, nil
}

func validate(c *Config) error {
	if c.DB.DSN == "" {
		return fmt.Errorf("db.dsn is required")
	}
	if c.App.Port <= 0 || c.App.Port > 65535 {
		return fmt.Errorf("invalid app.port: %d", c.App.Port)
	}
	if len(c.Security.HMACSecret) < 16 {
		return fmt.Errorf("security.hmac_secret must be at least 16 chars")
	}
	switch c.Stripe.Mode {
	case "live":
		if c.Stripe.SecretKey == "" {
			return fmt.Errorf("stripe.secret_key required when stripe.mode=live")
		}
		if c.Stripe.WebhookSecret == "" {
			return fmt.Errorf("stripe.webhook_secret required when stripe.mode=live")
		}
	case "fake":
		// no requirements
	default:
		return fmt.Errorf("invalid stripe.mode: %s (allowed: live, fake)", c.Stripe.Mode)
	}
	return nil
}

func stringDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func intDefault(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

func int64Default(v, def int64) int64 {
	if v == 0 {
		return def
	}
	return v
}

func sliceDefault(v, def []string) []string {
	if len(v) == 0 {
		return def
	}
	return v
}

func parseDurationDefault(v string, def time.Duration) (time.Duration, error) {
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, err
	}
	return d, nil
}

func nonNegUint64(v int64) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}

// shardCount clamps logical_shard_count into the valid uint8 range
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
