package message

import (
	"sync"
	"testing"
	"time"
)

// TestSnowflakeMonotonic 真实时钟下连续生成必须严格单调递增。
func TestSnowflakeMonotonic(t *testing.T) {
	gen := newSnowflake()
	prev := int64(0)
	for i := 0; i < 100000; i++ {
		id := gen.Next()
		if id <= prev {
			t.Fatalf("第 %d 个 ID 不单调：prev=%d cur=%d", i, prev, id)
		}
		prev = id
	}
}

// TestSnowflakeSameMillisecond 同一毫秒内靠序列号区分且递增，溢出后借下一毫秒。
func TestSnowflakeSameMillisecond(t *testing.T) {
	fake := snowflakeEpochMs + 1_000_000
	calls := 0
	gen := &snowflakeGen{now: func() int64 {
		calls++
		// 前 4096+1 次调用返回同一毫秒，之后时钟前进，模拟序列溢出等待。
		if calls > 5000 {
			return fake + 1
		}
		return fake
	}}
	prev := int64(0)
	for i := 0; i < 4200; i++ {
		id := gen.Next()
		if id <= prev {
			t.Fatalf("序列溢出场景下第 %d 个 ID 不单调：prev=%d cur=%d", i, prev, id)
		}
		prev = id
	}
}

// TestSnowflakeClockRegression 时钟回拨时沿用上次时间戳，仍保持单调。
func TestSnowflakeClockRegression(t *testing.T) {
	base := snowflakeEpochMs
	times := []int64{base + 2000, base + 2000, base + 1500, base + 1500, base + 2001}
	idx := 0
	gen := &snowflakeGen{now: func() int64 {
		v := times[idx]
		if idx < len(times)-1 {
			idx++
		}
		return v
	}}
	prev := int64(0)
	for i := 0; i < len(times); i++ {
		id := gen.Next()
		if id <= prev {
			t.Fatalf("时钟回拨场景下第 %d 个 ID 不单调：prev=%d cur=%d", i, prev, id)
		}
		prev = id
	}
}

// TestSnowflakeConcurrent 并发生成不重复。
func TestSnowflakeConcurrent(t *testing.T) {
	gen := newSnowflake()
	const workers = 8
	const perWorker = 2000
	results := make([][]int64, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			ids := make([]int64, 0, perWorker)
			for i := 0; i < perWorker; i++ {
				ids = append(ids, gen.Next())
			}
			results[slot] = ids
		}(w)
	}
	wg.Wait()
	seen := make(map[int64]struct{}, workers*perWorker)
	for _, ids := range results {
		for _, id := range ids {
			if _, dup := seen[id]; dup {
				t.Fatalf("并发生成出现重复 ID：%d", id)
			}
			seen[id] = struct{}{}
		}
	}
}

// TestSnowflakeTime ID 中的时间戳可还原为毫秒级创建时间。
func TestSnowflakeTime(t *testing.T) {
	gen := newSnowflake()
	before := time.Now().Add(-time.Second)
	id := gen.Next()
	after := time.Now().Add(time.Second)
	got := snowflakeTime(id)
	if got.Before(before) || got.After(after) {
		t.Fatalf("snowflakeTime 还原时间不合理：%v 不在 [%v, %v]", got, before, after)
	}
}
