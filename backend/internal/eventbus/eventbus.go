// Package eventbus 提供进程内发布订阅，用于解耦各领域模块与 Gateway 推送。
// 领域模块只负责 Publish；Gateway/迁移引擎等消费者 Subscribe 后自行过滤。
package eventbus

import (
	"log"
	"sync"

	"github.com/google/uuid"
)

// Event 单条事件。
//   - Type 使用本包常量（Event* / Internal*）。
//   - UserIDs 非空时表示定向推送给这些用户；为空时按 GuildID（必要时叠加 ChannelID 可见性过滤）广播。
//   - Internal* 事件仅供服务内部消费，Gateway 不得下发给客户端。
type Event struct {
	Type      string
	GuildID   *uuid.UUID
	ChannelID *uuid.UUID
	UserIDs   []uuid.UUID
	Payload   any
}

// Handler 订阅回调。同一订阅者的事件串行有序送达，不同订阅者相互独立。
type Handler func(Event)

type subscriber struct {
	ch chan Event
}

// Bus 进程内事件总线。
type Bus struct {
	mu   sync.RWMutex
	subs []*subscriber
}

const subscriberBuffer = 1024

func New() *Bus { return &Bus{} }

// Subscribe 注册订阅者；回调在独立 goroutine 内按发布顺序串行执行。
func (b *Bus) Subscribe(handler Handler) {
	sub := &subscriber{ch: make(chan Event, subscriberBuffer)}
	go func() {
		for event := range sub.ch {
			handler(event)
		}
	}()
	b.mu.Lock()
	b.subs = append(b.subs, sub)
	b.mu.Unlock()
}

// Publish 异步分发事件；订阅者积压超过缓冲上限时丢弃并记录日志（避免拖垮业务主路径）。
func (b *Bus) Publish(event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, sub := range b.subs {
		select {
		case sub.ch <- event:
		default:
			log.Printf("eventbus: 订阅者积压，丢弃事件 %s", event.Type)
		}
	}
}
