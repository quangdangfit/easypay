package blockchain

import (
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// PaymentReceived event layout we expect:
//
//	event PaymentReceived(bytes32 indexed orderId, address indexed payer, address token, uint256 amount)
//
// topic[0] = keccak256(signature), topic[1] = orderId, topic[2] = payer
// data[0:32]  = token (left-padded address)
// data[32:64] = amount
type ParsedPaymentEvent struct {
	OrderID string
	Payer   common.Address
	Token   common.Address
	Amount  *big.Int
}

var ErrUnexpectedLog = errors.New("log shape does not match PaymentReceived")

func ParsePaymentEvent(log types.Log) (*ParsedPaymentEvent, error) {
	if len(log.Topics) < 3 {
		return nil, ErrUnexpectedLog
	}
	if len(log.Data) < 64 {
		return nil, ErrUnexpectedLog
	}
	out := &ParsedPaymentEvent{
		OrderID: string(trimZero(log.Topics[1].Bytes())),
		Payer:   common.BytesToAddress(log.Topics[2].Bytes()),
		Token:   common.BytesToAddress(log.Data[12:32]),
		Amount:  new(big.Int).SetBytes(log.Data[32:64]),
	}
	return out, nil
}

func trimZero(b []byte) []byte {
	for i, c := range b {
		if c != 0 {
			return b[i:]
		}
	}
	return nil
}
