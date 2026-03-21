package normalizer

import (
	"fmt"
	"sync/atomic"
	"testing"
)

func BenchmarkDedup(b *testing.B) {
	b.ReportAllocs()

	b.Run("single_source_parallel", func(b *testing.B) {
		benchmarkDedupParallel(b, []string{"binance"})
	})

	b.Run("multi_source_parallel", func(b *testing.B) {
		benchmarkDedupParallel(b, []string{"binance", "coinbase", "kraken"})
	})
}

func benchmarkDedupParallel(b *testing.B, sources []string) {
	const keySpace = 1 << 14

	n := &Normalizer{
		logger:  testLogger(),
		maxSeen: keySpace * 2,
	}

	keyPools := make([][]string, len(sources))
	for i, source := range sources {
		n.shards.Store(source, newDedupShard(n.maxSeen))
		pool := make([]string, keySpace)
		for j := range pool {
			pool[j] = fmt.Sprintf("%s:trade-%05d", source, j)
		}
		keyPools[i] = pool
	}

	var counter uint64

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		localSource := 0
		for pb.Next() {
			idx := atomic.AddUint64(&counter, 1) - 1
			pool := keyPools[localSource%len(keyPools)]
			_ = n.isDuplicate(pool[int(idx)%len(pool)])
			localSource++
		}
	})
}
