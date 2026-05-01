package repository

import "hash/fnv"

// ShardCount is the number of merchant-id shards we route writes onto.
// Power of 2 so doubling (split each shard k → k, k+N) is mechanical.
const ShardCount = 16

// ShardOf returns the shard index in [0, ShardCount) for a merchant_id.
// FNV-32a is a stable, fast, well-distributing hash with no
// platform-dependent surprises (unlike maphash).
func ShardOf(merchantID string) uint8 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(merchantID))
	return uint8(h.Sum32() % ShardCount)
}

// ShardTable returns the physical table name for a shard index.
// Names are zero-padded 2-hex digits (`orders_00`..`orders_0f`) so the
// width survives if we double to 256 shards down the road.
func ShardTable(shard uint8) string {
	const hex = "0123456789abcdef"
	return "orders_0" + string(hex[shard&0x0f])
}
