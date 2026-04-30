package cache

import (
	"sync"
	"testing"
	"time"
)

func TestURLCache_PutGet(t *testing.T) {
	c := NewURLCache(8, time.Second)
	c.Put("ord-1", "https://x")
	got, ok := c.Get("ord-1")
	if !ok || got != "https://x" {
		t.Fatalf("got=%q ok=%v", got, ok)
	}
}

func TestURLCache_MissOnUnknown(t *testing.T) {
	c := NewURLCache(8, time.Second)
	if _, ok := c.Get("missing"); ok {
		t.Fatal("expected miss")
	}
}

func TestURLCache_TTLExpiry(t *testing.T) {
	c := NewURLCache(8, 10*time.Millisecond)
	c.Put("ord-1", "https://x")
	time.Sleep(25 * time.Millisecond)
	if _, ok := c.Get("ord-1"); ok {
		t.Fatal("expected expiry")
	}
}

func TestURLCache_Invalidate(t *testing.T) {
	c := NewURLCache(8, time.Second)
	c.Put("ord-1", "https://x")
	c.Invalidate("ord-1")
	if _, ok := c.Get("ord-1"); ok {
		t.Fatal("expected invalidated")
	}
}

func TestURLCache_EvictsAtCapacity(t *testing.T) {
	c := NewURLCache(2, time.Second)
	c.Put("a", "1")
	c.Put("b", "2")
	c.Put("c", "3")
	// Implementation evicts an arbitrary entry; we only assert the cap.
	count := 0
	for _, k := range []string{"a", "b", "c"} {
		if _, ok := c.Get(k); ok {
			count++
		}
	}
	if count > 2 {
		t.Fatalf("cap exceeded: %d entries visible", count)
	}
}

func TestURLCache_ConcurrentSafe(t *testing.T) {
	c := NewURLCache(64, time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			c.Put("k", "v")
		}(i)
		go func() {
			defer wg.Done()
			_, _ = c.Get("k")
		}()
	}
	wg.Wait()
}
