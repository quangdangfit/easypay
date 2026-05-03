package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/quangdangfit/easypay/internal/domain"
	repomock "github.com/quangdangfit/easypay/internal/mocks/repo"
)

func TestMerchantService_Create_HappyPath(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := repomock.NewMockMerchantRepository(ctrl)

	repo.EXPECT().
		Insert(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, m *domain.Merchant) error {
			// Simulate the repo stamping the picked shard + an autoincrement ID.
			m.ID = 42
			m.ShardIndex = 3
			return nil
		})

	svc := NewMerchantService(repo)
	res, err := svc.Create(context.Background(), CreateMerchantInput{
		MerchantID: "M-x", Name: "Acme", CallbackURL: "https://cb", RateLimit: 200,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.MerchantID != "M-x" || res.Name != "Acme" || res.CallbackURL != "https://cb" {
		t.Fatalf("got: %+v", res)
	}
	if res.RateLimit != 200 || res.ShardIndex != 3 {
		t.Fatalf("got: %+v", res)
	}
	// Generated keys are 64 hex chars (32 bytes).
	if len(res.APIKey) != 64 || len(res.SecretKey) != 64 {
		t.Fatalf("api_key/secret_key length: %d / %d", len(res.APIKey), len(res.SecretKey))
	}
	if res.APIKey == res.SecretKey {
		t.Fatal("api_key and secret_key must be distinct")
	}
}

func TestMerchantService_Create_RateLimitFallsBackToDefault(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := repomock.NewMockMerchantRepository(ctrl)
	repo.EXPECT().Insert(gomock.Any(), gomock.Any()).Return(nil)

	svc := NewMerchantService(repo)
	res, err := svc.Create(context.Background(), CreateMerchantInput{
		MerchantID: "M-x", Name: "Acme", RateLimit: 0,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.RateLimit != 1000 {
		t.Fatalf("expected default 1000, got %d", res.RateLimit)
	}
}

func TestMerchantService_Create_RejectsBadInput(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := repomock.NewMockMerchantRepository(ctrl)
	svc := NewMerchantService(repo)

	cases := []struct {
		name string
		in   CreateMerchantInput
	}{
		{"empty merchant_id", CreateMerchantInput{Name: "n"}},
		{"too long merchant_id", CreateMerchantInput{MerchantID: strings.Repeat("a", 65), Name: "n"}},
		{"empty name", CreateMerchantInput{MerchantID: "m"}},
		{"too long name", CreateMerchantInput{MerchantID: "m", Name: strings.Repeat("a", 256)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.Create(context.Background(), c.in)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("want ErrInvalidRequest, got %v", err)
			}
		})
	}
}

func TestMerchantService_Create_PropagatesRepoError(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := repomock.NewMockMerchantRepository(ctrl)
	repo.EXPECT().Insert(gomock.Any(), gomock.Any()).Return(errors.New("dup"))

	svc := NewMerchantService(repo)
	if _, err := svc.Create(context.Background(), CreateMerchantInput{MerchantID: "m", Name: "n"}); err == nil {
		t.Fatal("expected repo error")
	}
}

func TestRandomHex_LengthAndDistinct(t *testing.T) {
	a, err := randomHex(16)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(a) != 32 {
		t.Fatalf("hex len = %d, want 32", len(a))
	}
	b, _ := randomHex(16)
	if a == b {
		t.Fatal("two random invocations produced the same value")
	}
	if _, err := randomHex(0); err == nil {
		t.Fatal("expected error for n<=0")
	}
}
