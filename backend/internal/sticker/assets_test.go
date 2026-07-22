package sticker

import (
	"testing"
)

func TestSniffMediaMIME(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if got := sniffMediaMIME(png, ""); got != "image/png" {
		t.Fatalf("png sniff = %q", got)
	}
	gif := []byte("GIF89a............")
	if got := sniffMediaMIME(gif, ""); got != "image/gif" {
		t.Fatalf("gif sniff = %q", got)
	}
	// ftyp box at offset 4
	mp4 := make([]byte, 16)
	copy(mp4[4:8], []byte("ftyp"))
	copy(mp4[8:12], []byte("isom"))
	if got := sniffMediaMIME(mp4, "application/octet-stream"); got != "video/mp4" {
		t.Fatalf("mp4 sniff = %q", got)
	}
	webm := []byte{0x1A, 0x45, 0xDF, 0xA3, 0, 0, 0, 0, 0, 0, 0, 0}
	if got := sniffMediaMIME(webm, ""); got != "video/webm" {
		t.Fatalf("webm sniff = %q", got)
	}
}

func TestAllowedMIMEIncludesVideo(t *testing.T) {
	for _, mime := range []string{
		"image/png", "image/webp", "image/gif", "image/jpeg",
		"video/mp4", "video/webm",
	} {
		if _, ok := allowedMIME[mime]; !ok {
			t.Fatalf("missing allowed MIME %s", mime)
		}
	}
}

func TestValidAssetName(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if !validAssetName(hash + ".mp4") {
		t.Fatal("mp4 name should be valid")
	}
	if !validAssetName(hash + ".webm") {
		t.Fatal("webm name should be valid")
	}
	if validAssetName(hash + ".exe") {
		t.Fatal("exe should be invalid")
	}
}
