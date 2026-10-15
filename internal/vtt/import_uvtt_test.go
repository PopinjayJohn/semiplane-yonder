// UVTT parser spike tests: small hand-written fixtures only (no golden
// files, no vault writes). Covers walls+door+light in grid squares (UVTT
// units, verified vs real exports) with a cropped map_origin, polyline
// splitting, out-of-bounds clipping, corrupt inputs, color/alpha leniency,
// the >128 typed rejection, the >128MP typed rejection, and extension
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
  "resolution": {"map_origin": {"x":0,"y":0}, "map_size": {"x":4,"y":3}, "pixels_per_grid": 100},
  "line_of_sight": [
    [{"x":0,"y":0},{"x":4,"y":0}],
    [{"x":4,"y":0},{"x":4,"y":3}],
    [{"x":0,"y":3},{"x":2,"y":3},{"x":2,"y":1}]
  ],
  "objects_line_of_sight": [
    [{"x":0,"y":3},{"x":4,"y":3}]
  ],
  "portals": [
    {"position":{"x":1.5,"y":0},"bounds":[{"x":1,"y":0},{"x":2,"y":0}],"rotation":0,"closed":true}
  ],
  "environment": {"baked_lighting": false, "ambient_light": "ffffffff"},
  "lights": [
    {"position":{"x":2,"y":1.5},"range":2,"color":"ff0000ff","intensity":0.8,"shadows":true}
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
	if len(m.Walls) != 4 {
		t.Fatalf("walls = %d, want 4 (2 segs + 3-point polyline split)", len(m.Walls))
	}
	checkSeg(t, "wall0", m.Walls[0], WallSeg{0, 0, 4, 0})
	checkSeg(t, "wall1", m.Walls[1], WallSeg{4, 0, 4, 3})
	checkSeg(t, "wall2", m.Walls[2], WallSeg{0, 3, 2, 3})
	checkSeg(t, "wall3", m.Walls[3], WallSeg{2, 3, 2, 1})
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
		t.Errorf("light range = %g squares, want 2 (units are squares)", l.Range)
	}
	if l.Color != "0000ff" {
		t.Errorf("light color = %q, want 0000ff (leading alpha dropped)", l.Color)
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

func TestImportUVTTCroppedOrigin(t *testing.T) {
	// map_origin is in squares and only shifts geometry (never scales it).
	payload := `{
  "resolution": {"map_origin": {"x":10,"y":5}, "map_size": {"x":4,"y":3}, "pixels_per_grid": 100},
  "line_of_sight": [[{"x":10,"y":5},{"x":14,"y":5}]],
  "lights": [{"position":{"x":12,"y":6.5},"range":2,"color":"ffffff","intensity":1,"shadows":false}]
}`
	m, err := Parse([]byte(payload), "crop.uvtt")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Walls) != 1 {
		t.Fatalf("walls = %d, want 1", len(m.Walls))
	}
	checkSeg(t, "cropped", m.Walls[0], WallSeg{0, 0, 4, 0})
	if len(m.Lights) != 1 || !approxEq(m.Lights[0].X, 2) || !approxEq(m.Lights[0].Y, 1.5) {
		t.Errorf("lights = %+v, want one at (2,1.5)", m.Lights)
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
		if len(m.Walls) != 4 {
			t.Errorf("Parse(%s) walls = %d, want 4", name, len(m.Walls))
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
    [{"x":-1,"y":2},{"x":2,"y":2}],
    [{"x":9,"y":9},{"x":10,"y":10}]
  ],
  "objects_line_of_sight": [],
  "portals": [
    {"bounds":[{"x":-0.5,"y":0},{"x":1,"y":0}],"closed":false}
  ],
  "lights": [
    {"position":{"x":2,"y":2},"range":2,"color":"ffffff","intensity":1,"shadows":false},
    {"position":{"x":4.5,"y":2},"range":1,"color":"ffffff","intensity":1,"shadows":false},
    {"position":{"x":9,"y":9},"range":2,"color":"ffffff","intensity":1,"shadows":false}
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
	if len(m.Lights) != 2 {
		t.Fatalf("lights = %d, want 2 (in-board + glow-overlap retained, fully-oob dropped)", len(m.Lights))
	}
	if !approxEq(m.Lights[0].X, 2) || !approxEq(m.Lights[0].Y, 2) {
		t.Errorf("light pos = (%g,%g), want (2,2)", m.Lights[0].X, m.Lights[0].Y)
	}
	if !approxEq(m.Lights[1].X, 4.5) || !approxEq(m.Lights[1].Y, 2) {
		t.Errorf("glow light center = (%g,%g), want (4.5,2) kept as-is, never clamped",
			m.Lights[1].X, m.Lights[1].Y)
	}
}

// TestImportUVTTPartialOOBLightRetained pins the Ground-Floor 29th light
// (verified vs 00-Ground-Floor.dd2vtt: light #25 at x=-0.570313,
// y=19.539101, range 1.3 on a 20x20 grid): its glow disc reaches 0.57
// squares past the west edge, so R3 retains it with coordinates intact.
func TestImportUVTTPartialOOBLightRetained(t *testing.T) {
	payload := `{
  "resolution": {"map_origin": {"x":0,"y":0}, "map_size": {"x":20,"y":20}, "pixels_per_grid": 150},
  "lights": [
    {"position":{"x":9.015625,"y":3},"range":5,"color":"ffeccd8b","intensity":0.8,"shadows":true},
    {"position":{"x":-0.570313,"y":19.539101},"range":1.3,"color":"ffeccd8b","intensity":1,"shadows":true}
  ]
}`
	m, err := Parse([]byte(payload), "ground-floor.dd2vtt")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Lights) != 2 {
		t.Fatalf("lights = %d, want 2 (the OOB #25 glow is retained)", len(m.Lights))
	}
	l := m.Lights[1]
	if !approxEq(l.X, -0.570313) || !approxEq(l.Y, 19.539101) {
		t.Errorf("light pos = (%g,%g), want (-0.570313,19.539101) unclamped", l.X, l.Y)
	}
	if !approxEq(l.Range, 1.3) || l.Color != "eccd8b" {
		t.Errorf("light = %+v, want range 1.3 color eccd8b", l)
	}
}

// TestImportUVTTFullyOOBLightDropped pins the drop side: a glow disc
// entirely off-board (and a zero-range light) contributes nothing.
func TestImportUVTTFullyOOBLightDropped(t *testing.T) {
	payload := `{
  "resolution": {"map_origin": {"x":0,"y":0}, "map_size": {"x":20,"y":20}, "pixels_per_grid": 150},
  "lights": [
    {"position":{"x":-5,"y":10},"range":1.3,"color":"ffffff","intensity":1,"shadows":false},
    {"position":{"x":10,"y":10},"range":0,"color":"ffffff","intensity":1,"shadows":false},
    {"position":{"x":10,"y":10},"range":2,"color":"ffffff","intensity":1,"shadows":false}
  ]
}`
	m, err := Parse([]byte(payload), "far.dd2vtt")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Lights) != 1 {
		t.Fatalf("lights = %d, want 1 (fully-oob + zero-range dropped)", len(m.Lights))
	}
	if !approxEq(m.Lights[0].X, 10) || !approxEq(m.Lights[0].Y, 10) {
		t.Errorf("light pos = (%g,%g), want (10,10)", m.Lights[0].X, m.Lights[0].Y)
	}
}

// TestImportUVTTLightCap pins the culling bound: a hostile huge light list
// stays bounded by MaxLights, keeping file order (deterministic).
func TestImportUVTTLightCap(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"resolution": {"map_origin": [0,0], "map_size": [128,128], "pixels_per_grid": 50}, "lights": [`)
	for i := 0; i < MaxLights+88; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"position":[%d,1],"range":2,"color":"ffffff","intensity":1,"shadows":false}`, i%128)
	}
	b.WriteString(`]}`)
	m, err := Parse([]byte(b.String()), "hostile.uvtt")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Lights) != MaxLights {
		t.Fatalf("lights = %d, want MaxLights %d", len(m.Lights), MaxLights)
	}
	if !approxEq(m.Lights[0].X, 0) || !approxEq(m.Lights[MaxLights-1].X, float64((MaxLights-1)%128)) {
		t.Errorf("cap kept wrong slice: first x=%g last x=%g", m.Lights[0].X, m.Lights[MaxLights-1].X)
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
    {"position":[1,1],"range":2,"color":"#FF0000"},
    {"position":[2,2],"range":2,"color":"00ff00ff"},
    {"position":[3,3],"range":2,"color":"abc"},
    {"position":[1,3],"range":2,"color":"AABBCCDD"},
    {"position":[3,1],"range":2,"color":"zzz"}
  ]
}`
	m, err := Parse([]byte(payload), "color.uvtt")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// 8-hex drops the LEADING alpha pair; bad colors drop the light.
	want := []string{"ff0000", "ff00ff", "aabbcc", "bbccdd"}
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
	for _, size := range []string{`[129,10]`, `[10,200]`, `{"x":200,"y":200}`} {
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
	// 128x128 exactly is the cap, not over it.
	m, err := Parse([]byte(`{"resolution": {"map_origin": [0,0], "map_size": [128,128], "pixels_per_grid": 50}}`), "cap.uvtt")
	if err != nil {
		t.Fatalf("128x128 at cap: %v", err)
	}
	if m.Cols != 128 || m.Rows != 128 {
		t.Errorf("grid = %dx%d, want 128x128", m.Cols, m.Rows)
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
	payload := strings.Replace(uvttBasic(t, "PLACEHOLDER"), "PLACEHOLDER", pngWithDims(t, 12000, 12000), 1)
	_, err := Parse([]byte(payload), "huge.uvtt")
	if err == nil {
		t.Fatalf("want error, got nil")
	}
	var imgErr *ImageSizeError
	if !errors.As(err, &imgErr) {
		t.Fatalf("error %v is not *ImageSizeError", err)
	}
	if imgErr.Width != 12000 || imgErr.Height != 12000 {
		t.Errorf("dims = %dx%d, want 12000x12000", imgErr.Width, imgErr.Height)
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
	// Extended 2x3: flags + 3 reserved bytes + 24-bit LE (width-1, height-1).
	vp8x := webpFile("VP8X", []byte{0, 0, 0, 0, 1, 0, 0, 2, 0, 0})
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
