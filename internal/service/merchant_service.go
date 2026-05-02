package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/repository"
)

// CreateMerchantInput is what the admin handler passes to the service.
type CreateMerchantInput struct {
	MerchantID  string
	Name        string
	CallbackURL string
	RateLimit   int
}

// CreateMerchantResult is what the admin handler echoes back. The api_key
// and secret_key are returned plaintext exactly once at creation time —
// the operator is responsible for handing them to the merchant securely.
type CreateMerchantResult struct {
	MerchantID  string `json:"merchant_id"`
	Name        string `json:"name"`
	APIKey      string `json:"api_key"`
	SecretKey   string `json:"secret_key"`
	CallbackURL string `json:"callback_url,omitempty"`
	RateLimit   int    `json:"rate_limit"`
	ShardIndex  uint8  `json:"shard_index"`
}

type merchantService struct {
	repo repository.MerchantRepository
}

func NewMerchantService(repo repository.MerchantRepository) Merchants {
	return &merchantService{repo: repo}
}

func (s *merchantService) Create(ctx context.Context, in CreateMerchantInput) (*CreateMerchantResult, error) {
	mID := strings.TrimSpace(in.MerchantID)
	if mID == "" || len(mID) > 64 {
		return nil, fmt.Errorf("%w: merchant_id length must be 1..64", ErrInvalidRequest)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 255 {
		return nil, fmt.Errorf("%w: name length must be 1..255", ErrInvalidRequest)
	}

	apiKey, err := randomHex(32)
	if err != nil {
		return nil, fmt.Errorf("gen api_key: %w", err)
	}
	secretKey, err := randomHex(32)
	if err != nil {
		return nil, fmt.Errorf("gen secret_key: %w", err)
	}

	rateLimit := in.RateLimit
	if rateLimit <= 0 {
		rateLimit = 1000
	}

	m := &domain.Merchant{
		MerchantID:  mID,
		Name:        name,
		APIKey:      apiKey,
		SecretKey:   secretKey,
		CallbackURL: strings.TrimSpace(in.CallbackURL),
		RateLimit:   rateLimit,
		Status:      domain.MerchantStatusActive,
	}
	if err := s.repo.Insert(ctx, m); err != nil {
		return nil, err
	}

	return &CreateMerchantResult{
		MerchantID:  m.MerchantID,
		Name:        m.Name,
		APIKey:      m.APIKey,
		SecretKey:   m.SecretKey,
		CallbackURL: m.CallbackURL,
		RateLimit:   m.RateLimit,
		ShardIndex:  m.ShardIndex,
	}, nil
}

func randomHex(n int) (string, error) {
	if n <= 0 {
		return "", errors.New("randomHex: n must be > 0")
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
