package repository

import "hash/fnv"

// ShardCount is the number of shards we conceptually map merchant_ids onto.
// Even though all data lives in a single MySQL instance during early phases,
// indexes/queries use the modulo to keep us prepared for horizontal sharding.
const ShardCount = 8

// ShardOf returns shard index in [0, ShardCount).
func ShardOf(merchantID string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(merchantID))
	return h.Sum32() % ShardCount
}
