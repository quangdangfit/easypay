package blockchain

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

// ChainClient is the slim chain port we depend on. Wraps go-ethereum so we can
// inject a fake during tests.
type ChainClient interface {
	BlockNumber(ctx context.Context) (uint64, error)
	SubscribeFilterLogs(ctx context.Context, q ethereum.FilterQuery, ch chan<- types.Log) (ethereum.Subscription, error)
	FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error)
	TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error)
	Close()
}

// ChainConfig holds per-chain values; never hardcode chain-specific
// parameters in business logic.
type ChainConfig struct {
	ChainID               int64
	ContractAddress       common.Address
	RequiredConfirmations uint64
	StartBlock            uint64
	// EventTopic is keccak256(eventSignature). Override per ABI; zero value
	// means "subscribe to every log from contract".
	EventTopic common.Hash
}

type ethClient struct {
	ws   *ethclient.Client
	http *ethclient.Client
}

// NewClient dials both WS (for SubscribeFilterLogs) and HTTP (for backfill).
// Either may be empty — methods that need the missing transport will fail.
func NewClient(ctx context.Context, ws, httpURL string) (ChainClient, error) {
	c := &ethClient{}
	if ws != "" {
		w, err := ethclient.DialContext(ctx, ws)
		if err != nil {
			return nil, fmt.Errorf("dial ws: %w", err)
		}
		c.ws = w
	}
	if httpURL != "" {
		h, err := ethclient.DialContext(ctx, httpURL)
		if err != nil {
			if c.ws != nil {
				c.ws.Close()
			}
			return nil, fmt.Errorf("dial http: %w", err)
		}
		c.http = h
	}
	if c.ws == nil && c.http == nil {
		return nil, fmt.Errorf("no rpc endpoint configured")
	}
	return c, nil
}

func (c *ethClient) BlockNumber(ctx context.Context) (uint64, error) {
	cl := c.preferHTTP()
	if cl == nil {
		return 0, fmt.Errorf("no client available")
	}
	return cl.BlockNumber(ctx)
}

func (c *ethClient) SubscribeFilterLogs(ctx context.Context, q ethereum.FilterQuery, ch chan<- types.Log) (ethereum.Subscription, error) {
	if c.ws == nil {
		return nil, fmt.Errorf("websocket endpoint not configured")
	}
	return c.ws.SubscribeFilterLogs(ctx, q, ch)
}

func (c *ethClient) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	cl := c.preferHTTP()
	if cl == nil {
		return nil, fmt.Errorf("no client available")
	}
	return cl.FilterLogs(ctx, q)
}

func (c *ethClient) TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error) {
	cl := c.preferHTTP()
	if cl == nil {
		return nil, fmt.Errorf("no client available")
	}
	return cl.TransactionReceipt(ctx, hash)
}

func (c *ethClient) Close() {
	if c.ws != nil {
		c.ws.Close()
	}
	if c.http != nil {
		c.http.Close()
	}
}

func (c *ethClient) preferHTTP() *ethclient.Client {
	if c.http != nil {
		return c.http
	}
	return c.ws
}

// helpers
func bigOrZero(v *big.Int) *big.Int {
	if v == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(v)
}
