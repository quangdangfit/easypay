package repository

import "testing"

func TestShardOf_RangeAndDeterminism(t *testing.T) {
	for _, m := range []string{"M1", "merchant_42", "abc-xyz", ""} {
		s1 := ShardOf(m)
		s2 := ShardOf(m)
		if s1 != s2 {
			t.Errorf("non-deterministic shard for %q: %d vs %d", m, s1, s2)
		}
		if s1 >= ShardCount {
			t.Errorf("shard %d out of range for %q (ShardCount=%d)", s1, m, ShardCount)
		}
	}
}

func TestShardOf_DistributesAcrossShards(t *testing.T) {
	// Across 200 distinct merchant IDs, the FNV-32a hash should cover most
	// of the 16 shards. Threshold is conservative (>=8) to keep the test
	// stable across hash-function tweaks.
	seen := map[uint8]bool{}
	for i := 0; i < 200; i++ {
		m := []byte("merchant_")
		m = append(m, byte('A'+(i/26)))
		m = append(m, byte('A'+(i%26)))
		seen[ShardOf(string(m))] = true
	}
	if len(seen) < 8 {
		t.Errorf("hash distribution too narrow: %d/%d shards", len(seen), ShardCount)
	}
}

func TestShardTable(t *testing.T) {
	cases := map[uint8]string{
		0:  "orders_00",
		1:  "orders_01",
		9:  "orders_09",
		10: "orders_0a",
		15: "orders_0f",
	}
	for in, want := range cases {
		if got := ShardTable(in); got != want {
			t.Errorf("ShardTable(%d) = %q, want %q", in, got, want)
		}
	}
}
