// UVTT parser spike tests: small hand-written fixtures only (no golden
// files, no vault writes). Covers walls+door+light with a cropped
// map_origin, out-of-bounds clipping, corrupt inputs, color/alpha leniency,
// the >64 typed rejection, the >32MP typed rejection, and extension
// agnosticism.
package vtt

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"strings"
	"testing"
)

// tinyPNG is a 1x1 transparent PNG (magic-sniff + DecodeConfig target).
const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func uvttBasic(t *testing.T, imageB64 string) string {
	t.Helper()
	return fmt.Sprintf(`{
  "format": 1.0,
  "resolution": {"map_origin": {"x":50,"y":25}, "map_size": {"x":4,"y":3}, "pixels_per_grid": 100},
  "line_of_sight": [
    [{"x":50,"y":25},{"x":450,"y":25}],
    [{"x":450,"y":25},{"x":450,"y":325}]
  ],
  "objects_line_of_sight": [
    [{"x":50,"y":325},{"x":450,"y":325}]
  ],
  "portals": [
    {"position":{"x":200,"y":25},"bounds":[{"x":150,"y":25},{"x":250,"y":25}],"rotation":0,"closed":true}
  ],
  "environment": {"baked_lighting": false, "ambient_light": "ffffffff"},
  "lights": [
    {"position":{"x":250,"y":175},"range":200,"color":"ff0000ff","intensity":0.8,"shadows":true}
  ],
  "image": "%s"
}`, imageB64)
}

func approxEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func checkSeg(t *testing.T, what string, got WallSeg, want WallSeg) {
	t.Helper()
	if !approxEq(got.X1, want.X1) || !approxEq(got.Y1, want.Y1) ||
		!approxEq(got.X2, want.X2) || !approxEq(got.Y2, want.Y2) {
		t.Errorf("%s = %+v, want %+v", what, got, want)
	}
}

func TestImportUVTTBasic(t *testing.T) {
	m, err := Parse([]byte(uvttBasic(t, tinyPNG)), "ford.dd2vtt")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Source != "ford.dd2vtt" {
		t.Errorf("Source = %q, want audit base name", m.Source)
	}
	if m.Cols != 4 || m.Rows != 3 {
		t.Errorf("grid = %dx%d, want 4x3", m.Cols, m.Rows)
	}
	if m.Cell != 100 || m.PPG != 100 {
		t.Errorf("cell/ppg = %d/%g, want 100/100", m.Cell, m.PPG)
	}
	if len(m.Walls) != 2 {
		t.Fatalf("walls = %d, want 2", len(m.Walls))
	}
	checkSeg(t, "wall0", m.Walls[0], WallSeg{0, 0, 4, 0})
	checkSeg(t, "wall1", m.Walls[1], WallSeg{4, 0, 4, 3})
	if len(m.Objects) != 1 {
		t.Fatalf("objects = %d, want 1", len(m.Objects))
	}
	checkSeg(t, "object0", m.Objects[0], WallSeg{0, 3, 4, 3})
	if len(m.Portals) != 1 {
		t.Fatalf("portals = %d, want 1", len(m.Portals))
	}
	p := m.Portals[0]
	if !p.Closed {
		t.Errorf("portal closed = false, want true (door)")
	}
	if !approxEq(p.AX, 1) || !approxEq(p.AY, 0) || !approxEq(p.BX, 2) || !approxEq(p.BY, 0) {
		t.Errorf("portal = %+v, want (1,0)-(2,0)", p)
	}
	if len(m.Lights) != 1 {
		t.Fatalf("lights = %d, want 1", len(m.Lights))
	}
	l := m.Lights[0]
	if !approxEq(l.X, 2) || !approxEq(l.Y, 1.5) {
		t.Errorf("light pos = (%g,%g), want (2,1.5)", l.X, l.Y)
	}
	if !approxEq(l.Range, 2) {
		t.Errorf("light range = %g squares, want 2 (200px/100ppg)", l.Range)
	}
	if l.Color != "ff0000" {
		t.Errorf("light color = %q, want ff0000 (alpha dropped)", l.Color)
	}
	if !approxEq(l.Intensity, 0.8) || !l.Shadows {
		t.Errorf("light intensity/shadows = %g/%v, want 0.8/true", l.Intensity, l.Shadows)
	}
	if m.Ambient != "ffffff" {
		t.Errorf("ambient = %q, want ffffff", m.Ambient)
	}
	if m.BakedLighting {
		t.Errorf("baked = true, want false")
	}
	if m.Image == nil {
		t.Fatalf("image = nil, want parsed")
	}
	if m.Image.Format != "png" || m.Image.Width != 1 || m.Image.Height != 1 {
		t.Errorf("image = %s %dx%d, want png 1x1", m.Image.Format, m.Image.Width, m.Image.Height)
	}
}

func TestImportUVTTExtensionAgnostic(t *testing.T) {
	payload := uvttBasic(t, tinyPNG)
	for _, name := range []string{"map.uvtt", "map.dd2vtt", "map.df2vtt", "map.weird"} {
		m, err := Parse([]byte(payload), name)
		if err != nil {
			t.Errorf("Parse(%s): %v", name, err)
			continue
		}
		if m.Source != name {
			t.Errorf("Source = %q, want %q", m.Source, name)
		}
		if len(m.Walls) != 2 {
			t.Errorf("Parse(%s) walls = %d, want 2", name, len(m.Walls))
		}
	}
	// Windows-hostile source is sanitized, never used as a path.
	m, err := Parse([]byte(payload), `C:\games\CON.dd2vtt`)
	if err != nil {
		t.Fatalf("Parse windows source: %v", err)
	}
	if strings.ContainsAny(m.Source, `\/:`) || m.Source == "" {
		t.Errorf("Source = %q, want safe base name", m.Source)
	}
}

func TestImportUVTTOOBClip(t *testing.T) {
	payload := `{
  "resolution": {"map_origin": {"x":0,"y":0}, "map_size": {"x":4,"y":4}, "pixels_per_grid": 50},
  "line_of_sight": [
    [{"x":-100,"y":100},{"x":100,"y":100}],
    [{"x":300,"y":300},{"x":400,"y":400}]
  ],
  "objects_line_of_sight": [],
  "portals": [
    {"bounds":[{"x":-50,"y":0},{"x":50,"y":0}],"closed":false}
  ],
  "lights": [
    {"position":{"x":500,"y":500},"range":50,"color":"ffffff","intensity":1,"shadows":false},
    {"position":{"x":100,"y":100},"range":50,"color":"ffffff","intensity":1,"shadows":false}
  ]
}`
	m, err := Parse([]byte(payload), "oob.uvtt")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Walls) != 1 {
		t.Fatalf("walls = %d, want 1 (partial clipped, outside dropped)", len(m.Walls))
	}
	checkSeg(t, "clipped", m.Walls[0], WallSeg{0, 2, 2, 2})
	if len(m.Portals) != 1 {
		t.Fatalf("portals = %d, want 1 (clipped)", len(m.Portals))
	}
	p := m.Portals[0]
	if p.Closed {
		t.Errorf("portal closed = true, want false (window default)")
	}
	if !approxEq(p.AX, 0) || !approxEq(p.AY, 0) || !approxEq(p.BX, 1) || !approxEq(p.BY, 0) {
		t.Errorf("portal = %+v, want (0,0)-(1,0)", p)
	}
	if len(m.Lights) != 1 {
		t.Fatalf("lights = %d, want 1 (oob dropped)", len(m.Lights))
	}
	if !approxEq(m.Lights[0].X, 2) || !approxEq(m.Lights[0].Y, 2) {
		t.Errorf("light pos = (%g,%g), want (2,2)", m.Lights[0].X, m.Lights[0].Y)
	}
}

func TestImportUVTTCorrupt(t *testing.T) {
	okPayload := uvttBasic(t, tinyPNG)
	cases := map[string]string{
		"bad json":      `{not json`,
		"empty":         ``,
		"missing res":   `{"line_of_sight": []}`,
		"bad origin":    `{"resolution": {"map_origin": {"x":"far"},"map_size": {"x":4,"y":4}, "pixels_per_grid": 50}}`,
		"zero ppg":      `{"resolution": {"map_origin": [0,0],"map_size": [4,4], "pixels_per_grid": 0}}`,
		"bad base64":    strings.Replace(okPayload, tinyPNG, "!!!not-base64!!!", 1),
		"unknown magic": strings.Replace(okPayload, tinyPNG, base64.StdEncoding.EncodeToString([]byte("hello")), 1),
		"data url":      strings.Replace(okPayload, tinyPNG, "data:image/png;base64,"+tinyPNG, 1),
	}
	for name, payload := range cases {
		_, err := Parse([]byte(payload), "bad.uvtt")
		if err == nil {
			t.Errorf("%s: want error, got nil", name)
			continue
		}
		if !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: error %v does not wrap ErrCorrupt", name, err)
		}
	}
}

func TestImportUVTTColorLeniency(t *testing.T) {
	payload := `{
  "resolution": {"map_origin": [0,0], "map_size": [4,4], "pixels_per_grid": 50},
  "environment": {"ambient_light": "#00FF00"},
  "lights": [
    {"position":[10,10],"range":50,"color":"#FF0000"},
    {"position":[20,20],"range":50,"color":"00ff00ff"},
    {"position":[30,30],"range":50,"color":"abc"},
    {"position":[40,40],"range":50,"color":"AABBCCDD"},
    {"position":[50,50],"range":50,"color":"zzz"}
  ]
}`
	m, err := Parse([]byte(payload), "color.uvtt")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"ff0000", "00ff00", "aabbcc", "aabbcc", "ffffff"}
	if len(m.Lights) != len(want) {
		t.Fatalf("lights = %d, want %d", len(m.Lights), len(want))
	}
	for i, w := range want {
		if m.Lights[i].Color != w {
			t.Errorf("light %d color = %q, want %q", i, m.Lights[i].Color, w)
		}
	}
	if m.Ambient != "00ff00" {
		t.Errorf("ambient = %q, want 00ff00", m.Ambient)
	}
	// Invalid ambient degrades to absent, never to a guess.
	m2, err := Parse([]byte(strings.Replace(payload, `"#00FF00"`, `"morse"`, 1)), "color.uvtt")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m2.Ambient != "" {
		t.Errorf("ambient = %q, want empty on invalid input", m2.Ambient)
	}
}

func TestImportUVTTMapTooLarge(t *testing.T) {
	for _, size := range []string{`[65,10]`, `[10,100]`, `{"x":200,"y":200}`} {
		payload := fmt.Sprintf(`{"resolution": {"map_origin": [0,0], "map_size": %s, "pixels_per_grid": 50}}`, size)
		_, err := Parse([]byte(payload), "big.uvtt")
		if err == nil {
			t.Errorf("size %s: want error, got nil", size)
			continue
		}
		var sizeErr *MapSizeError
		if !errors.As(err, &sizeErr) {
			t.Errorf("size %s: error %v is not *MapSizeError", size, err)
		}
	}
	// 64x64 exactly is the cap, not over it.
	m, err := Parse([]byte(`{"resolution": {"map_origin": [0,0], "map_size": [64,64], "pixels_per_grid": 50}}`), "cap.uvtt")
	if err != nil {
		t.Fatalf("64x64 at cap: %v", err)
	}
	if m.Cols != 64 || m.Rows != 64 {
		t.Errorf("grid = %dx%d, want 64x64", m.Cols, m.Rows)
	}
}

// pngWithDims builds a header-only PNG (valid signature + IHDR with a real
// CRC) so the megapixel gate can be tested without shipping megabytes.
func pngWithDims(t *testing.T, w, h uint32) string {
	t.Helper()
	var buf []byte
	buf = append(buf, 0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A)
	ihdr := make([]byte, 4+4+13)
	binary.BigEndian.PutUint32(ihdr[0:4], 13)
	copy(ihdr[4:8], "IHDR")
	binary.BigEndian.PutUint32(ihdr[8:12], w)
	binary.BigEndian.PutUint32(ihdr[12:16], h)
	ihdr[16] = 8 // bit depth
	ihdr[17] = 2 // color type: truecolor
	// compression, filter, interlace stay 0
	buf = append(buf, ihdr...)
	crc := crc32.ChecksumIEEE(buf[len(buf)-17:])
	var crcB [4]byte
	binary.BigEndian.PutUint32(crcB[:], crc)
	buf = append(buf, crcB[:]...)
	return base64.StdEncoding.EncodeToString(buf)
}

func TestImportUVTTImageTooLarge(t *testing.T) {
	payload := strings.Replace(uvttBasic(t, "PLACEHOLDER"), "PLACEHOLDER", pngWithDims(t, 6000, 6000), 1)
	_, err := Parse([]byte(payload), "huge.uvtt")
	if err == nil {
		t.Fatalf("want error, got nil")
	}
	var imgErr *ImageSizeError
	if !errors.As(err, &imgErr) {
		t.Fatalf("error %v is not *ImageSizeError", err)
	}
	if imgErr.Width != 6000 || imgErr.Height != 6000 {
		t.Errorf("dims = %dx%d, want 6000x6000", imgErr.Width, imgErr.Height)
	}
}

// webpFile wraps a chunk body in a minimal RIFF/WEBP container.
func webpFile(fourcc string, body []byte) string {
	var buf []byte
	buf = append(buf, "RIFF"...)
	size := make([]byte, 4)
	binary.LittleEndian.PutUint32(size, uint32(4+8+len(body)))
	buf = append(buf, size...)
	buf = append(buf, "WEBP"...)
	buf = append(buf, fourcc...)
	ch := make([]byte, 4)
	binary.LittleEndian.PutUint32(ch, uint32(len(body)))
	buf = append(buf, ch...)
	buf = append(buf, body...)
	if len(body)&1 == 1 {
		buf = append(buf, 0)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func TestImportUVTTWebPDims(t *testing.T) {
	// Lossless 1x1: signature 0x2F + zero packed bits (width-1=0,height-1=0).
	vp8l := webpFile("VP8L", []byte{0x2F, 0, 0, 0, 0})
	// Lossy 8x8: frame tag + start code + LE dims.
	vp8 := webpFile("VP8 ", []byte{0, 0, 0, 0x9D, 0x01, 0x2A, 8, 0, 8, 0})
	// Extended 2x3 best-effort: flags + 24-bit LE (width-1, height-1).
	vp8x := webpFile("VP8X", []byte{0, 1, 0, 0, 2, 0, 0})
	for name, tc := range map[string]struct {
		b64  string
		w, h int
	}{
		"lossless": {vp8l, 1, 1},
		"lossy":    {vp8, 8, 8},
		"extended": {vp8x, 2, 3},
	} {
		m, err := Parse([]byte(uvttBasic(t, tc.b64)), "w.uvtt")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if m.Image == nil || m.Image.Format != "webp" {
			t.Errorf("%s: format = %+v, want webp", name, m.Image)
			continue
		}
		if m.Image.Width != tc.w || m.Image.Height != tc.h {
			t.Errorf("%s: dims = %dx%d, want %dx%d", name, m.Image.Width, m.Image.Height, tc.w, tc.h)
		}
	}
}

func TestImportUVTTNoImage(t *testing.T) {
	payload := `{"resolution": {"map_origin": [0,0], "map_size": [4,4], "pixels_per_grid": 140}}`
	m, err := Parse([]byte(payload), "bare.df2vtt")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Image != nil {
		t.Errorf("image = %+v, want nil", m.Image)
	}
	// Source ppg kept as info while cell clamps to the render range.
	if m.PPG != 140 || m.Cell != 140 {
		t.Errorf("ppg/cell = %g/%d, want 140/140", m.PPG, m.Cell)
	}
	lo, err := Parse([]byte(strings.Replace(payload, "140", "8", 1)), "bare.df2vtt")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if lo.PPG != 8 || lo.Cell != MinCellPx {
		t.Errorf("ppg/cell = %g/%d, want 8/%d", lo.PPG, lo.Cell, MinCellPx)
	}
}
