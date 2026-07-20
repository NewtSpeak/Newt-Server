package security

import "testing"

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("a-correct-password")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "a-correct-password") {
		t.Fatal("正确密码应通过校验")
	}
	if VerifyPassword(hash, "wrong-password") {
		t.Fatal("错误密码不应通过校验")
	}
}

func TestPasswordLength(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("短密码必须被拒绝")
	}
}
