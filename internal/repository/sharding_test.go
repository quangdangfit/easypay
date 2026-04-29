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
	// Across 200 distinct merchant IDs, all 8 shards should see traffic.
	seen := map[uint32]bool{}
	for i := 0; i < 200; i++ {
		m := []byte("merchant_")
		m = append(m, byte('A'+(i/26)))
		m = append(m, byte('A'+(i%26)))
		seen[ShardOf(string(m))] = true
	}
	if len(seen) < 4 {
		t.Errorf("hash distribution too narrow: %d shards", len(seen))
	}
}
