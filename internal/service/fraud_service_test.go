package service

import (
	"context"
	"testing"
)

func TestFraud_NoUserIDReturnsZero(t *testing.T) {
	rc := newRedis(t)
	f := NewVelocityChecker(rc)
	score, err := f.Check(context.Background(), "M1", "", 1500)
	if err != nil || score != 0 {
		t.Fatalf("score=%d err=%v", score, err)
	}
}

func TestFraud_ScoreScalesWithVelocity(t *testing.T) {
	rc := newRedis(t)
	f := NewVelocityChecker(rc)
	ctx := context.Background()

	// Below threshold (default 20) — score should be modest.
	for i := 0; i < 5; i++ {
		_, _ = f.Check(ctx, "M1", "user-1", 100)
	}
	score, err := f.Check(ctx, "M1", "user-1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if score >= 100 {
		t.Fatalf("score too high: %d", score)
	}
}

func TestFraud_ScoreHits100AtThreshold(t *testing.T) {
	rc := newRedis(t)
	f := NewVelocityChecker(rc)
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		_, _ = f.Check(ctx, "M2", "user-x", 100)
	}
	score, err := f.Check(ctx, "M2", "user-x", 100)
	if err != nil {
		t.Fatal(err)
	}
	if score != 100 {
		t.Fatalf("score: %d", score)
	}
}
