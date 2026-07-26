package cosmetics

// 动图嗅探纯函数单测（无 DB）：APNG 的 acTL-before-IDAT 规则与
// 动态 WebP 的 VP8X animation 位，含字节巧合误报回归。

import (
	"encoding/binary"
	"testing"
)

// buildPNG 按 chunk 序列构造合法结构的 PNG 字节（CRC 填零即可，嗅探不校验 CRC）。
func buildPNG(chunks ...struct {
	typ     string
	payload []byte
}) []byte {
	out := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	for _, ch := range chunks {
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(ch.payload)))
		out = append(out, lenBuf[:]...)
		out = append(out, ch.typ...)
		out = append(out, ch.payload...)
		out = append(out, 0, 0, 0, 0) // CRC 占位
	}
	return out
}

type pngChunk = struct {
	typ     string
	payload []byte
}

func TestPNGIsAnimated(t *testing.T) {
	ihdr := pngChunk{"IHDR", make([]byte, 13)}
	actl := pngChunk{"acTL", make([]byte, 8)}
	idat := pngChunk{"IDAT", []byte{0x01, 0x02}}
	iend := pngChunk{"IEND", nil}

	if !pngIsAnimated(buildPNG(ihdr, actl, idat, iend)) {
		t.Fatal("含 acTL（IDAT 前）的 APNG 应判定为动图")
	}
	if pngIsAnimated(buildPNG(ihdr, idat, iend)) {
		t.Fatal("普通 PNG 应判定为静图")
	}
	// 字节巧合回归：像素数据（IDAT payload）里出现 "acTL" 字样不得误报
	trap := pngChunk{"IDAT", []byte("xxacTLxx")}
	if pngIsAnimated(buildPNG(ihdr, trap, iend)) {
		t.Fatal("IDAT 内含 acTL 字节的静态 PNG 不应误报为动图")
	}
	// 非 PNG / 截断数据不 panic 且返回 false
	if pngIsAnimated([]byte("notpng")) || pngIsAnimated(nil) {
		t.Fatal("非法输入应返回 false")
	}
}

// buildWebP 构造最小 RIFF/WEBP 容器；vp8x=true 时首 chunk 为 VP8X 并按 flags 置位。
func buildWebP(vp8x bool, flags byte) []byte {
	out := []byte("RIFF")
	out = append(out, 0x20, 0, 0, 0) // RIFF size 占位
	out = append(out, "WEBP"...)
	if vp8x {
		out = append(out, "VP8X"...)
		out = append(out, 10, 0, 0, 0) // chunk size = 10（小端）
		out = append(out, flags)
		out = append(out, 0, 0, 0)    // 保留位
		out = append(out, 0x1F, 0, 0) // canvas width-1 = 31 → 32
		out = append(out, 0x3F, 0, 0) // canvas height-1 = 63 → 64
	} else {
		out = append(out, "VP8 "...)
		out = append(out, 4, 0, 0, 0)
		out = append(out, 0, 0, 0, 0)
	}
	return out
}

func TestWebPIsAnimated(t *testing.T) {
	if !webpIsAnimated(buildWebP(true, 0x02)) {
		t.Fatal("VP8X animation 位置位的 WebP 应判定为动图")
	}
	if webpIsAnimated(buildWebP(true, 0x00)) {
		t.Fatal("VP8X 但 animation 位未置位应为静图")
	}
	if webpIsAnimated(buildWebP(false, 0)) {
		t.Fatal("VP8 简单格式必为静图")
	}
	if webpIsAnimated([]byte("RIFFxxxx")) || webpIsAnimated(nil) {
		t.Fatal("非法输入应返回 false")
	}
}

func TestWebPDimensions(t *testing.T) {
	w, h, ok := webpDimensions(buildWebP(true, 0x02))
	if !ok || w != 32 || h != 64 {
		t.Fatalf("VP8X 画布尺寸解析错误: ok=%v w=%d h=%d，期待 32×64", ok, w, h)
	}
	if _, _, ok := webpDimensions(buildWebP(false, 0)); ok {
		t.Fatal("非 VP8X 格式不应返回尺寸")
	}
}
