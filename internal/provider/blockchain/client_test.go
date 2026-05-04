package blockchain

import (
	"context"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

func TestNewClient_NoEndpoints(t *testing.T) {
	t.Parallel()
	_, err := NewClient(context.Background(), "", "")
	if err == nil {
		t.Fatal("expected error when both ws/http are empty")
	}
	if !strings.Contains(err.Error(), "no rpc endpoint configured") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewClient_InvalidURLs(t *testing.T) {
	t.Parallel()

	if _, err := NewClient(context.Background(), "://bad", ""); err == nil || !strings.Contains(err.Error(), "dial ws") {
		t.Fatalf("want dial ws error, got %v", err)
	}
	if _, err := NewClient(context.Background(), "", "://bad"); err == nil || !strings.Contains(err.Error(), "dial http") {
		t.Fatalf("want dial http error, got %v", err)
	}
}

func TestEthClient_NoTransport(t *testing.T) {
	t.Parallel()

	c := &ethClient{}

	if _, err := c.BlockNumber(context.Background()); err == nil || !strings.Contains(err.Error(), "no client available") {
		t.Fatalf("want no-client error from BlockNumber, got %v", err)
	}
	if _, err := c.FilterLogs(context.Background(), ethereum.FilterQuery{}); err == nil || !strings.Contains(err.Error(), "no client available") {
		t.Fatalf("want no-client error from FilterLogs, got %v", err)
	}
	if _, err := c.TransactionReceipt(context.Background(), common.Hash{}); err == nil || !strings.Contains(err.Error(), "no client available") {
		t.Fatalf("want no-client error from TransactionReceipt, got %v", err)
	}
	if _, err := c.SubscribeFilterLogs(context.Background(), ethereum.FilterQuery{}, make(chan<- types.Log)); err == nil || !strings.Contains(err.Error(), "websocket endpoint not configured") {
		t.Fatalf("want ws-not-configured error, got %v", err)
	}

	// no panic on nil transports
	c.Close()
}

func TestEthClientPreferHTTP(t *testing.T) {
	t.Parallel()

	ws := new(ethclient.Client)
	http := new(ethclient.Client)

	c := &ethClient{ws: ws, http: http}
	if got := c.preferHTTP(); got != http {
		t.Fatal("preferHTTP should return http when available")
	}

	c.http = nil
	if got := c.preferHTTP(); got != ws {
		t.Fatal("preferHTTP should fall back to ws when http is nil")
	}

	c.ws = nil
	if got := c.preferHTTP(); got != nil {
		t.Fatal("preferHTTP should return nil when both transports are nil")
	}
}
