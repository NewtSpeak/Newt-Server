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
