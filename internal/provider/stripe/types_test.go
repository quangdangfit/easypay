package stripe

import (
	"testing"
)

func TestBreakerClient_State(t *testing.T) {
	c := NewBreakerClient(NewFake(), "test")
	bc, ok := c.(*breakerClient)
	if !ok {
		t.Fatal("not a breakerClient")
	}
	if bc.State() != "closed" {
		t.Fatalf("initial: %s", bc.State())
	}
}
