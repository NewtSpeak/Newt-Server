package customization

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRoleStyleValidateClear(t *testing.T) {
	s := RoleStyle{Type: ""}
	out, err := s.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if out != "{}" {
		t.Fatalf("got %q", out)
	}
}

func TestRoleStyleValidateSolid(t *testing.T) {
	s := RoleStyle{Type: "solid", Colors: []string{"#ff0000"}, Animated: true, Speed: ptrF(2)}
	out, err := s.Validate()
	if err != nil {
		t.Fatal(err)
	}
	var parsed RoleStyle
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Animated || parsed.Speed != nil {
		t.Fatalf("solid should strip animation: %+v", parsed)
	}
}

func TestRoleStyleValidateLinearSpeed(t *testing.T) {
	s := RoleStyle{
		Type:     "linear",
		Colors:   []string{"#ff0000", "#00ff00"},
		Animated: true,
	}
	out, err := s.Validate()
	if err != nil {
		t.Fatal(err)
	}
	var parsed RoleStyle
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Speed == nil || *parsed.Speed != 4 {
		t.Fatalf("default speed: %+v", parsed.Speed)
	}
	if parsed.Angle == nil || *parsed.Angle != 90 {
		t.Fatalf("default angle: %+v", parsed.Angle)
	}
}

func TestRoleStyleValidateColorsDark(t *testing.T) {
	// 渐变：暗色配色 2–8 个，随主表面持久化
	s := RoleStyle{
		Type:       "linear",
		Colors:     []string{"#ff0000", "#00ff00"},
		ColorsDark: []string{"#220000", "#002200"},
	}
	out, err := s.Validate()
	if err != nil {
		t.Fatal(err)
	}
	var parsed RoleStyle
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.ColorsDark) != 2 || parsed.ColorsDark[0] != "#220000" {
		t.Fatalf("colors_dark should persist: %+v", parsed.ColorsDark)
	}

	// 渐变：暗色配色只给 1 个 → 报错
	bad := RoleStyle{
		Type:       "linear",
		Colors:     []string{"#ff0000", "#00ff00"},
		ColorsDark: []string{"#220000"},
	}
	if _, err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "暗色") {
		t.Fatalf("expected colors_dark count error, got %v", err)
	}

	// 纯色：暗色配色最多 1 个
	badSolid := RoleStyle{
		Type:       "solid",
		Colors:     []string{"#ff0000"},
		ColorsDark: []string{"#220000", "#002200"},
	}
	if _, err := badSolid.Validate(); err == nil || !strings.Contains(err.Error(), "暗色") {
		t.Fatalf("expected solid colors_dark error, got %v", err)
	}

	// 非法 hex
	badHex := RoleStyle{
		Type:       "solid",
		Colors:     []string{"#ff0000"},
		ColorsDark: []string{"red"},
	}
	if _, err := badHex.Validate(); err == nil || !strings.Contains(err.Error(), "#RRGGBB") {
		t.Fatalf("expected hex error, got %v", err)
	}
}

func TestRoleStyleValidateSpeedRange(t *testing.T) {
	s := RoleStyle{
		Type: "linear", Colors: []string{"#ff0000", "#00ff00"},
		Animated: true, Speed: ptrF(0.1),
	}
	if _, err := s.Validate(); err == nil || !strings.Contains(err.Error(), "speed") {
		t.Fatalf("expected speed error, got %v", err)
	}
}

func TestRoleStyleValidateIconSync(t *testing.T) {
	s := RoleStyle{
		Type: "linear", Colors: []string{"#ff0000", "#00ff00"},
		IconSync: true,
		Icon: &RoleSurfaceStyle{
			Type: "radial", Colors: []string{"#0000ff", "#ffffff"},
		},
	}
	out, err := s.Validate()
	if err != nil {
		t.Fatal(err)
	}
	var parsed RoleStyle
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if !parsed.IconSync || parsed.Icon != nil {
		t.Fatalf("icon_sync should drop independent icon: %+v", parsed)
	}
}

func TestRoleStyleValidateIconIndependent(t *testing.T) {
	s := RoleStyle{
		Type: "solid", Colors: []string{"#ff0000"},
		Icon: &RoleSurfaceStyle{
			Type: "linear", Colors: []string{"#00ff00", "#0000ff"},
			Animated: true, Speed: ptrF(2),
		},
	}
	out, err := s.Validate()
	if err != nil {
		t.Fatal(err)
	}
	var parsed RoleStyle
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Icon == nil || parsed.Icon.Type != "linear" {
		t.Fatalf("expected independent icon: %+v", parsed.Icon)
	}
	if parsed.Icon.Speed == nil || *parsed.Icon.Speed != 2 {
		t.Fatalf("icon speed: %+v", parsed.Icon.Speed)
	}
}

func TestRoleStyleValidateBadgeOnly(t *testing.T) {
	show := true
	s := RoleStyle{
		Type: "",
		Badge: &RoleBadgeStyle{
			Enabled: true,
			Background: &RoleSurfaceStyle{
				Type: "linear", Colors: []string{"#ff0000", "#0000ff"},
				Animated: true,
			},
			ShowName: &show,
			TextColor: "#ffffff",
		},
	}
	out, err := s.Validate()
	if err != nil {
		t.Fatal(err)
	}
	var parsed RoleStyle
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Type != "" || parsed.Badge == nil || parsed.Badge.Background == nil {
		t.Fatalf("badge-only style: %+v", parsed)
	}
}

func ptrF(v float64) *float64 { return &v }
