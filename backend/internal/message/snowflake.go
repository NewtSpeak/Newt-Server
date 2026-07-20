package message

import (
	"sync"
	"time"
)

// 自实现的简单雪花 ID（docs 13 AP.1）：
//
//		63          22          12          0
//		+-----------+-----------+-----------+
//		| 41bit 毫秒 | 10bit 机器 | 12bit 序列 |
//		+-----------+-----------+-----------+
//
//	  - 41bit 毫秒时间戳：相对自定义纪元（2026-01-01 UTC），可用约 69 年；
//	  - 10bit 机器位：Owl-Server 控制面当前单实例部署，固定为 0；
//	    未来水平扩容时改为从配置注入即可，布局不变；
//	  - 12bit 序列：同一毫秒内最多 4096 个，溢出时自旋等待下一毫秒。
//
// 生成的 ID 严格单调递增（进程内），天然可按时间排序，用作消息游标分页。
const (
	snowflakeEpochMs = int64(1767225600000) // 2026-01-01 00:00:00 UTC
	snowflakeMachine = int64(0)             // 单机部署固定 0
	machineBits      = 10
	sequenceBits     = 12
	sequenceMask     = int64(1<<sequenceBits - 1)
	timestampShift   = machineBits + sequenceBits
	machineShift     = sequenceBits
)

// snowflakeGen 并发安全的雪花 ID 生成器。
type snowflakeGen struct {
	mu       sync.Mutex
	lastMs   int64
	sequence int64
	// now 可注入的时钟（毫秒），便于单元测试。
	now func() int64
}

func newSnowflake() *snowflakeGen {
	return &snowflakeGen{now: func() int64 { return time.Now().UnixMilli() }}
}

// Next 生成下一个 ID。
// 时钟回拨时不后退：沿用 lastMs 继续消耗序列号，保证单调性优先于时间精确性。
func (g *snowflakeGen) Next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	nowMs := g.now()
	if nowMs < g.lastMs {
		nowMs = g.lastMs
	}
	if nowMs == g.lastMs {
		g.sequence = (g.sequence + 1) & sequenceMask
		if g.sequence == 0 {
			// 序列溢出：自旋到下一毫秒。
			for nowMs <= g.lastMs {
				nowMs = g.now()
				if nowMs < g.lastMs {
					nowMs = g.lastMs + 1
				}
			}
		}
	} else {
		g.sequence = 0
	}
	g.lastMs = nowMs
	return (nowMs-snowflakeEpochMs)<<timestampShift | snowflakeMachine<<machineShift | g.sequence
}

// snowflakeTime 从雪花 ID 还原创建时间（保留策略与调试用）。
func snowflakeTime(id int64) time.Time {
	return time.UnixMilli(id>>timestampShift + snowflakeEpochMs)
}
