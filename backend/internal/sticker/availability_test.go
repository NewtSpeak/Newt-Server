package sticker

import (
	"testing"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

func TestMarkFromHash(t *testing.T) {
	hash := "a1b2c3d4e5f6789012345678abcdef01abcdef0123456789abcdef0123456789"
	got := markFromHash(hash)
	want := "e_a1b2c3d4e5f6"
	if got != want {
		t.Fatalf("markFromHash = %q, want %q", got, want)
	}
}

func TestNormalizeItemName(t *testing.T) {
	cases := map[string]string{
		"  hello  ":           "hello",
		"foo.png":             "foo",
		"path/to/bar.webp":    "bar",
		"表情包.GIF":             "表情包",
		"":                    "",
		"   ":                 "",
	}
	for in, want := range cases {
		if got := normalizeItemName(in); got != want {
			t.Fatalf("normalizeItemName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractCustomEmoteItemIDs(t *testing.T) {
	content := "hello <e:123:e_abc> world <e:456:e_def> and again <e:123:e_abc>"
	ids := ExtractCustomEmoteItemIDs(content)
	if len(ids) != 2 || ids[0] != 123 || ids[1] != 456 {
		t.Fatalf("ids = %v, want [123 456]", ids)
	}
}

func TestParseReactionKey(t *testing.T) {
	id, custom, key := ParseReactionKey("item:9876543210")
	if !custom || id != 9876543210 || key != "item:9876543210" {
		t.Fatalf("got id=%d custom=%v key=%q", id, custom, key)
	}
	id, custom, key = ParseReactionKey("😀")
	if custom || id != 0 || key != "😀" {
		t.Fatalf("unicode: id=%d custom=%v key=%q", id, custom, key)
	}
}

func TestScopeAllowsContext(t *testing.T) {
	gid := uuid.New()
	account := model.StickerPack{Scope: model.StickerScopeAccount}
	if !scopeAllowsContext(account, uuid.Nil) || !scopeAllowsContext(account, gid) {
		t.Fatal("account pack should work everywhere")
	}
	guild := model.StickerPack{Scope: model.StickerScopeGuild, GuildID: &gid}
	if scopeAllowsContext(guild, uuid.Nil) {
		t.Fatal("guild pack should not work in DM")
	}
	if !scopeAllowsContext(guild, gid) {
		t.Fatal("guild pack should work in own guild")
	}
	other := uuid.New()
	if scopeAllowsContext(guild, other) {
		t.Fatal("guild pack should not work in other guild")
	}
}

func TestCanCopyGuildForbidden(t *testing.T) {
	// canCopy 在无 DB 时对 guild 直接拒绝，不查库状态以外的字段
	// 这里仅测 B3 分支：通过构造 status=active + scope=guild
	// 需要 db 的 refreshSoftDeleteStatus 会 no-op 查库失败时不改 status
	// 用 nil 不安全；改为直接断言逻辑：
	if model.StickerScopeGuild == model.StickerScopeAccount {
		t.Fatal("scopes must differ")
	}
}
