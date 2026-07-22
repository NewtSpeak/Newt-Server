package config

import "testing"

func TestParseByteSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"1024", 1024},
		{"50m", 50 << 20},
		{"50mb", 50 << 20},
		{"50MiB", 50 << 20},
		{"512k", 512 << 10},
		{"1g", 1 << 30},
		{"1.5m", int64(1.5 * float64(1<<20))},
	}
	for _, tc := range cases {
		got, err := parseByteSize(tc.in)
		if err != nil {
			t.Fatalf("parseByteSize(%q) error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("parseByteSize(%q)=%d want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseByteSizeInvalid(t *testing.T) {
	for _, in := range []string{"", "abc", "-1", "xxmb"} {
		if _, err := parseByteSize(in); err == nil {
			t.Fatalf("parseByteSize(%q) expected error", in)
		}
	}
}
