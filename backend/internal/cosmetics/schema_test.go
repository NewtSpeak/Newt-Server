package cosmetics

import "testing"

func TestValidateCategorySchema(t *testing.T) {
	s := CategorySchema{
		RenderHint: "avatar_frame",
		AssetSlots: []AssetSlotDef{
			{Key: "primary", Required: true, MIMEGroups: []string{"image", "animated_image"}},
		},
	}
	if err := validateCategorySchema(s); err != nil {
		t.Fatal(err)
	}
	s.AssetSlots = append(s.AssetSlots, AssetSlotDef{Key: "primary", MIMEGroups: []string{"image"}})
	if err := validateCategorySchema(s); err == nil {
		t.Fatal("expected duplicate slot error")
	}
}

func TestMimeBelongsGroup(t *testing.T) {
	if !mimeBelongsGroup("image/png", []string{"image"}) {
		t.Fatal("png should be image")
	}
	if !mimeBelongsGroup("image/gif", []string{"animated_image"}) {
		t.Fatal("gif should be animated_image")
	}
	if !mimeBelongsGroup("video/mp4", []string{"video"}) {
		t.Fatal("mp4 should be video")
	}
	if mimeBelongsGroup("audio/ogg", []string{"image"}) {
		t.Fatal("ogg should not be image")
	}
}

func TestValidateItemAgainstSchema(t *testing.T) {
	s := CategorySchema{
		AssetSlots: []AssetSlotDef{
			{Key: "compact", Required: true},
			{Key: "full", Required: true},
			{Key: "audio", Required: false},
		},
	}
	if err := validateItemAgainstSchema(s, map[string]int64{"compact": 1}, nil); err == nil {
		t.Fatal("expected missing full")
	}
	if err := validateItemAgainstSchema(s, map[string]int64{"compact": 1, "full": 2}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestEncodeParseAssetsMap(t *testing.T) {
	raw := encodeAssetsMap(map[string]int64{"primary": 12345})
	m, err := parseAssetsMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m["primary"] != 12345 {
		t.Fatalf("got %v", m)
	}
}
