package blockchain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestParsePaymentEvent_HappyPath(t *testing.T) {
	// orderId = "ord-1234" packed into bytes32 (left-padded zeros, ascii at the end).
	orderHex := common.LeftPadBytes([]byte("ord-1234"), 32)
	payer := common.HexToAddress("0xdEAD000000000000000000000000000000000001")
	token := common.HexToAddress("0xC0FFEE0000000000000000000000000000000002")

	// data = 32 bytes (token padded) + 32 bytes (amount big-endian).
	data := make([]byte, 64)
	copy(data[12:32], token.Bytes())
	amt := new(big.Int).SetUint64(123456)
	amtBytes := amt.Bytes()
	copy(data[64-len(amtBytes):], amtBytes)

	lg := types.Log{
		Topics: []common.Hash{
			common.HexToHash("0xeventsig"),
			common.BytesToHash(orderHex),
			common.BytesToHash(payer.Bytes()),
		},
		Data: data,
	}
	parsed, err := ParsePaymentEvent(lg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.OrderID != "ord-1234" {
		t.Fatalf("orderID: got %q want ord-1234", parsed.OrderID)
	}
	if parsed.Payer != payer {
		t.Fatalf("payer mismatch: %s vs %s", parsed.Payer.Hex(), payer.Hex())
	}
	if parsed.Token != token {
		t.Fatalf("token mismatch: %s vs %s", parsed.Token.Hex(), token.Hex())
	}
	if parsed.Amount.Cmp(amt) != 0 {
		t.Fatalf("amount: got %s want %s", parsed.Amount.String(), amt.String())
	}
}

func TestParsePaymentEvent_RejectsShortLog(t *testing.T) {
	if _, err := ParsePaymentEvent(types.Log{}); err == nil {
		t.Fatal("expected error for empty log")
	}
}

// Topics present but data shorter than 64 bytes — must reject.
func TestParsePaymentEvent_RejectsShortData(t *testing.T) {
	lg := types.Log{
		Topics: []common.Hash{{}, {}, {}},
		Data:   make([]byte, 32), // < 64
	}
	if _, err := ParsePaymentEvent(lg); err == nil {
		t.Fatal("expected ErrUnexpectedLog for short data")
	}
}

// trimZero on an all-zero slice returns nil, producing an empty OrderID.
func TestParsePaymentEvent_AllZeroOrderID(t *testing.T) {
	lg := types.Log{
		Topics: []common.Hash{{}, {}, {}}, // topic[1] all zeros
		Data:   make([]byte, 64),
	}
	parsed, err := ParsePaymentEvent(lg)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if parsed.OrderID != "" {
		t.Fatalf("expected empty OrderID, got %q", parsed.OrderID)
	}
}
