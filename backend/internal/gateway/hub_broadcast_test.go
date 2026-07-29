package gateway

// 全站广播白名单测试：无 GuildID/UserIDs 的事件仅当在白名单内（平台级事件，
// 如装扮目录更新）才广播到全部会话；其余无路由事件保持丢弃。

import (
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
)

func TestDispatchGlobalBroadcastWhitelist(t *testing.T) {
	alice := model.User{ID: uuid.New(), Username: "alice"}
	bob := model.User{ID: uuid.New(), Username: "bob"}
	auth := &stubAuth{users: map[string]model.User{"tok-a": alice, "tok-b": bob}}
	h := newHandler(auth, &stubDirectory{}, testOptions())
	server := newTestServer(t, h)

	wsAlice, _ := handshake(t, server, "tok-a")
	wsBob, _ := handshake(t, server, "tok-b")

	// 白名单事件（COSMETIC_CATALOG_UPDATE）无 GuildID/UserIDs → 全部会话收到。
	h.hub.dispatch(eventbus.Event{
		Type:    eventbus.EventCosmeticCatalogUpdate,
		Payload: map[string]string{"op": "item_update", "item_id": "1"},
	})
	for _, ws := range []*websocket.Conn{wsAlice, wsBob} {
		f := readFrame(t, ws)
		if f.Op != opDispatch || f.T != eventbus.EventCosmeticCatalogUpdate {
			t.Fatalf("收到 op=%s t=%s，期待 DISPATCH COSMETIC_CATALOG_UPDATE", f.Op, f.T)
		}
	}

	// 非白名单的无路由事件 → 无人收到（保持丢弃语义）。
	h.hub.dispatch(eventbus.Event{Type: eventbus.EventUserUpdate, Payload: map[string]int{"n": 1}})
	assertNoDispatch(t, wsAlice)
	assertNoDispatch(t, wsBob)
}
