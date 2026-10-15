// UVTT import spike (plan/vtt-pixi-uvtt, phase P-UVTT, step 1 of N).
//
// Pure parser/mapper for Universal VTT (.uvtt / .dd2vtt / .df2vtt) JSON.
// Format-agnostic: all three extensions carry the same JSON schema and are
// accepted interchangeably; the filename is audit metadata only.
//
// Scope limits of THIS spike (no contract change):
//   - No vault writes, no sidecar/frontmatter edits, no route/CLI/snapshot
//     changes. Callers (import route, import-uvtt CLI) come later.
//   - Stdlib only (encoding/json, encoding/base64, image/png, image/jpeg,
//     hash-free WebP header sniff). No new Go dependencies.
//   - Obsidian-safe trivially: this file never touches the vault.
//
// Coordinate model (per vtt-pixi-uvtt.md section 2): UVTT positions arrive
// in image pixels with a top-left origin, +y down. Grid squares (float) =
// (pixel - map_origin) / pixels_per_grid. Rendering inverts it:
// pixel = grid * ppg + origin.
//
// Assumptions pinned for later phases to confirm:
//   - Light `range` is in image pixels (divided by ppg into squares). A
//     range already in squares would mis-scale; no exporter surveyed so far
//     does that.
//   - `objects_line_of_sight` maps to movement-only blockers (Roll20
//     convention); full vision waits for P3.
//   - VP8X (extended WebP) canvas parsing is best-effort (flags + two
//     24-bit LE words); lossy VP8 and lossless VP8L follow their specs.
package vtt

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"image/png"
	"math"
	"path"
	"strings"
)

// Limits --------------------------------------------------------------------

// MaxImageMegapixels caps the map image dims check: width*height must fit
// in 128 megapixels (A-VTT2: admits 9600x7200-class webp, which is stored
// as-is and never fully decoded at import; png/jpeg write-path decodes stay
// guarded by uploads.Normalize — over-cap png/jpeg must arrive as webp).
const MaxImageMegapixels = 128 * 1024 * 1024

// maxImageDecodedBytes bounds base64 output before sniffing so a hostile
// file cannot OOM the importer ahead of the megapixel check.
const maxImageDecodedBytes = 96 << 20

// base64ChunkSize bounds scratch per decode step (must stay a multiple of
// 4 so non-final chunks never split a quantum).
const base64ChunkSize = 32 << 10

// MaxLights caps the retained light list (R3 glow rule, §docs/plan/
// r3-oob-light-glow.md): real exports carry dozens of lights (29 observed
// max), so 512 is generous headroom while a hostile 100k-light list can no
// longer balloon the sidecar, the snapshot JSON, or the Pixi light pass.
// Truncation keeps the first MaxLights in file order (deterministic).
const MaxLights = 512

// ErrCorrupt marks every structural failure (bad JSON, missing resolution,
// bad base64, unknown image magic). Use errors.Is(err, ErrCorrupt).
var ErrCorrupt = errors.New("uvtt: corrupt file")

// MapSizeError is the typed rejection for map_size over the grid cap
// (amend: 422 with reason, no silent downscale).
type MapSizeError struct{ Cols, Rows int }

func (e *MapSizeError) Error() string {
	return fmt.Sprintf("uvtt: map size %dx%d exceeds %dx%d grid cap", e.Cols, e.Rows, MaxGridCols, MaxGridRows)
}

// ImageSizeError is the typed rejection for images over the megapixel cap.
type ImageSizeError struct{ Width, Height int }

func (e *ImageSizeError) Error() string {
	mp := float64(int64(e.Width) * int64(e.Height))
	return fmt.Sprintf("uvtt: image %dx%d (%.1fMP) exceeds %dMP cap", e.Width, e.Height, mp/(1024*1024), MaxImageMegapixels/(1024*1024))
}

// Result types (grid-square units, floats) ------------------------------------

// WallSeg is one wall/blocker segment in grid-square units, already clipped
// to [0,Cols]x[0,Rows].
type WallSeg struct{ X1, Y1, X2, Y2 float64 }

// Portal is one portal span in grid-square units. Closed=true renders as a
// door, false as a window (rotation is ignored per the plan).
type Portal struct {
	AX, AY, BX, BY float64
	Closed         bool
}

// Light is one dynamic light in grid-square units. Range is in squares,
// Color is lowercase RRGGBB (alpha dropped).
type Light struct {
	X, Y      float64
	Range     float64
	Color     string
	Intensity float64
	Shadows   bool
}

// UVTTImage is the sniffed map image. Format is one of "png", "webp",
// "jpeg". Data is the raw decoded bytes for the later upload pipeline.
type UVTTImage struct {
	Format        string
	Width, Height int
	Data          []byte
}

// UVTTMap is the mapped import: grid calibration plus clipped geometry.
// Field names mirror the amend's vtt-map sub-keys (walls, blocking-objects,
// portals, lights, ambient, baked-lighting, ppg, uvtt-source) so the later
// sidecar writer is mechanical.
type UVTTMap struct {
	Source string  // sanitized base name of the uploaded file (audit only)
	Cols   int     // from map_size.x, 1..MaxGridCols
	Rows   int     // from map_size.y, 1..MaxGridRows
	Cell   int     // pixels_per_grid clamped to MinCellPx..MaxCellPx
	PPG    float64 // source pixels-per_grid (info only)
	// GridBaked is the D12 posture the importer writes: false = the
	// renderer draws the overlay grid (the UVTT embedded-image default,
	// Dungeondraft bakes nogrid art); true = art carries its own grid.
	GridBaked bool

	OriginX, OriginY float64 // map_origin in image pixels (cropped-export offset)

	Walls   []WallSeg // line_of_sight: sight + movement
	Objects []WallSeg // objects_line_of_sight: movement-only
	Portals []Portal
	Lights  []Light

	Ambient       string // lowercase RRGGBB, "" when absent/unparseable
	BakedLighting bool   // environment.baked_lighting (warning hint only)

	Image *UVTTImage // nil when the file carries no image
}

// Parse parses raw .uvtt/.dd2vtt/.df2vtt JSON. source is the original
// filename; it is reduced to a Windows-safe base name and stored as audit
// metadata only (never used as a path). Parse performs no I/O.
func Parse(data []byte, source string) (*UVTTMap, error) {
	return ParseWithOpts(data, source, ImportOpts{})
}

// ImportOpts carries surface-specific import options. GridBaked is a pointer
// so "absent" (importer default false) is distinct from an explicit
// --no-grid-baked: the HTTP body's `grid_baked` field is read first, then a
// non-nil opts value (CLI flag) overrides it.
type ImportOpts struct {
	GridBaked *bool
}

// ParseWithOpts is Parse plus the D12 grid-baked posture: the JSON body may
// carry `grid_baked` (HTTP field / CLI parity), and a non-nil
// opts.GridBaked overrides it (CLI --grid-baked/--no-grid-baked).
func ParseWithOpts(data []byte, source string, opts ImportOpts) (*UVTTMap, error) {
	var wrap struct {
		uvttFile
		GridBaked *bool `json:"grid_baked"`
	}
	if err := json.Unmarshal(data, &wrap); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON: %v", ErrCorrupt, err)
	}
	m, err := mapUVTT(wrap.uvttFile, source)
	if err != nil {
		return nil, err
	}
	baked := false
	if wrap.GridBaked != nil {
		baked = *wrap.GridBaked
	}
	if opts.GridBaked != nil {
		baked = *opts.GridBaked
	}
	m.GridBaked = baked
	return m, nil
}

// mapUVTT validates + maps the raw schema (Parse's original body).
func mapUVTT(raw uvttFile, source string) (*UVTTMap, error) {
	if len(raw.Resolution) == 0 {
		return nil, fmt.Errorf("%w: missing resolution", ErrCorrupt)
	}
	var res uvttResolution
	if err := json.Unmarshal(raw.Resolution, &res); err != nil {
		return nil, fmt.Errorf("%w: invalid resolution: %v", ErrCorrupt, err)
	}
	ox, oy, ok := parsePoint(res.MapOrigin)
	if !ok || !finite(ox) || !finite(oy) {
		return nil, fmt.Errorf("%w: invalid map_origin", ErrCorrupt)
	}
	sx, sy, ok := parsePoint(res.MapSize)
	if !ok || !finite(sx) || !finite(sy) {
		return nil, fmt.Errorf("%w: invalid map_size", ErrCorrupt)
	}
	cols, rows := int(math.Round(sx)), int(math.Round(sy))
	if cols > MaxGridCols || rows > MaxGridRows {
		return nil, &MapSizeError{Cols: cols, Rows: rows}
	}
	if cols < 1 || rows < 1 {
		return nil, fmt.Errorf("%w: invalid map_size %gx%g", ErrCorrupt, sx, sy)
	}
	var ppg float64
	if err := json.Unmarshal(res.PixelsPerGrid, &ppg); err != nil || !finite(ppg) || ppg <= 0 {
		return nil, fmt.Errorf("%w: invalid pixels_per_grid", ErrCorrupt)
	}
	cell := int(math.Round(ppg))
	cell = min(max(cell, MinCellPx), MaxCellPx)

	out := &UVTTMap{
		Source:  sanitizeSource(source),
		Cols:    cols,
		Rows:    rows,
		Cell:    cell,
		PPG:     ppg,
		OriginX: ox,
		OriginY: oy,
	}
	toGrid := func(sx, sy float64) (float64, float64) {
		// UVTT coordinates are grid squares (verified vs real exports);
		// only the crop origin shifts them. ppg relates image pixels to
		// squares and never touches geometry.
		return sx - ox, sy - oy
	}
	maxX, maxY := float64(cols), float64(rows)

	for _, seg := range raw.LineOfSight {
		for _, s := range splitPolyline(seg) {
			gx1, gy1 := toGrid(s[0], s[1])
			gx2, gy2 := toGrid(s[2], s[3])
			if cx1, cy1, cx2, cy2, ok := clipSegToGrid(gx1, gy1, gx2, gy2, maxX, maxY); ok {
				out.Walls = append(out.Walls, WallSeg{cx1, cy1, cx2, cy2})
			}
		}
	}
	for _, seg := range raw.ObjectsLineOfSight {
		for _, s := range splitPolyline(seg) {
			gx1, gy1 := toGrid(s[0], s[1])
			gx2, gy2 := toGrid(s[2], s[3])
			if cx1, cy1, cx2, cy2, ok := clipSegToGrid(gx1, gy1, gx2, gy2, maxX, maxY); ok {
				out.Objects = append(out.Objects, WallSeg{cx1, cy1, cx2, cy2})
			}
		}
	}
	for _, praw := range raw.Portals {
		var p uvttPortal
		if err := json.Unmarshal(praw, &p); err != nil || len(p.Bounds) < 2 {
			continue
		}
		ax, ay, ok1 := parsePoint(p.Bounds[0])
		bx, by, ok2 := parsePoint(p.Bounds[1])
		if !ok1 || !ok2 {
			continue
		}
		gax, gay := toGrid(ax, ay)
		gbx, gby := toGrid(bx, by)
		if cx1, cy1, cx2, cy2, ok := clipSegToGrid(gax, gay, gbx, gby, maxX, maxY); ok {
			out.Portals = append(out.Portals, Portal{AX: cx1, AY: cy1, BX: cx2, BY: cy2, Closed: portalClosed(p.Closed)})
		}
	}
	for _, lraw := range raw.Lights {
		var l uvttLight
		if err := json.Unmarshal(lraw, &l); err != nil {
			continue
		}
		px, py, ok := parsePoint(l.Position)
		if !ok || !finite(px) || !finite(py) {
			continue // positionless lights are dropped
		}
		gx, gy := toGrid(px, py)
		rng := 0.0
		if len(l.Range) > 0 {
			var r float64
			if err := json.Unmarshal(l.Range, &r); err != nil || !finite(r) || r < 0 {
				r = 0
			}
			rng = r // UVTT range is grid squares (verified vs real export)
		}
		if rng <= 0 {
			continue // zero/negative range lights are dropped
		}
		if !lightGlowOverlaps(gx, gy, rng, maxX, maxY) {
			continue // fully-off-board glow is dropped, never clamped
		}
		color, ok := parseUVTTColor(l.Color)
		if !ok {
			continue // unparseable color drops the light; never invent one
		}
		intensity := 1.0
		if len(l.Intensity) > 0 {
			var in float64
			if err := json.Unmarshal(l.Intensity, &in); err != nil || !finite(in) || in < 0 {
				in = 1
			}
			intensity = in
		}
		shadows := false
		if len(l.Shadows) > 0 {
			var b bool
			if err := json.Unmarshal(l.Shadows, &b); err != nil {
				var n float64
				if jerr := json.Unmarshal(l.Shadows, &n); jerr == nil {
					b = n != 0
				} else {
					b = false
				}
			}
			shadows = b
		}
		out.Lights = append(out.Lights, Light{X: gx, Y: gy, Range: rng, Color: color, Intensity: intensity, Shadows: shadows})
	}
	if len(out.Lights) > MaxLights {
		out.Lights = out.Lights[:MaxLights] // hostile huge lists stay bounded, file order kept
	}
	if len(raw.Environment) > 0 {
		var env uvttEnvironment
		if err := json.Unmarshal(raw.Environment, &env); err == nil {
			out.BakedLighting = env.BakedLighting
			if ambient, ok := parseUVTTColor(env.AmbientLight); ok {
				out.Ambient = ambient
			}
		}
	}
	img, err := parseImage(raw.Image)
	if err != nil {
		return nil, err
	}
	out.Image = img
	return out, nil
}

// sanitizeSource reduces an upload filename to a Windows-safe audit label:
// base name only, backslashes folded, reserved stems suffixed.
func sanitizeSource(s string) string {
	s = strings.ReplaceAll(s, "\\", "/")
	base := path.Base(strings.TrimSpace(s))
	if base == "" || base == "." || base == "/" {
		return "import.uvtt"
	}
	if windowsReserved(strings.TrimSuffix(base, path.Ext(base))) {
		base = base + "_map"
	}
	return base
}

// Raw schema ---------------------------------------------------------------

type uvttFile struct {
	Resolution         json.RawMessage   `json:"resolution"`
	LineOfSight        []json.RawMessage `json:"line_of_sight"`
	ObjectsLineOfSight []json.RawMessage `json:"objects_line_of_sight"`
	Portals            []json.RawMessage `json:"portals"`
	Environment        json.RawMessage   `json:"environment"`
	Lights             []json.RawMessage `json:"lights"`
	Image              string            `json:"image"`
}

type uvttResolution struct {
	MapOrigin     json.RawMessage `json:"map_origin"`
	MapSize       json.RawMessage `json:"map_size"`
	PixelsPerGrid json.RawMessage `json:"pixels_per_grid"`
}

type uvttPortal struct {
	Bounds []json.RawMessage `json:"bounds"`
	Closed json.RawMessage   `json:"closed"`
}

type uvttLight struct {
	Position  json.RawMessage `json:"position"`
	Range     json.RawMessage `json:"range"`
	Color     string          `json:"color"`
	Intensity json.RawMessage `json:"intensity"`
	Shadows   json.RawMessage `json:"shadows"`
}

type uvttEnvironment struct {
	BakedLighting bool   `json:"baked_lighting"`
	AmbientLight  string `json:"ambient_light"`
}

// Lenient geometry -----------------------------------------------------------

// parsePoint accepts {"x":..,"y":..} or [x, y].
func parsePoint(raw json.RawMessage) (x, y float64, ok bool) {
	var obj struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.X, obj.Y, true
	}
	var arr [2]float64
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr[0], arr[1], true
	}
	return 0, 0, false
}

// parseSegPixels accepts [[{x,y},{x,y}]] elements as [p1, p2], {a,b} /
// {start,end} / {p1,p2} objects, or flat [x1,y1,x2,y2]. Output is in image
// pixels; the caller converts to grid units.
func parseSegPixels(raw json.RawMessage) (x1, y1, x2, y2 float64, ok bool) {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		switch len(arr) {
		case 2:
			ax, ay, ok1 := parsePoint(arr[0])
			bx, by, ok2 := parsePoint(arr[1])
			if !ok1 || !ok2 {
				return 0, 0, 0, 0, false
			}
			return ax, ay, bx, by, true
		case 4:
			var nums [4]float64
			for i, n := range arr {
				if err := json.Unmarshal(n, &nums[i]); err != nil {
					return 0, 0, 0, 0, false
				}
			}
			return nums[0], nums[1], nums[2], nums[3], true
		}
		return 0, 0, 0, 0, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return 0, 0, 0, 0, false
	}
	for _, keys := range [][2]string{{"a", "b"}, {"start", "end"}, {"p1", "p2"}} {
		ra, oka := obj[keys[0]]
		rb, okb := obj[keys[1]]
		if !oka || !okb {
			continue
		}
		ax, ay, ok1 := parsePoint(ra)
		bx, by, ok2 := parsePoint(rb)
		if !ok1 || !ok2 {
			return 0, 0, 0, 0, false
		}
		return ax, ay, bx, by, true
	}
	return 0, 0, 0, 0, false
}

// splitPolyline splits one line_of_sight element into [x1,y1,x2,y2]
// segments: N-point polylines yield N-1 consecutive pairs (cave walls
// arrive as many-point chains); other shapes fall back to parseSegPixels.
func splitPolyline(raw json.RawMessage) [][4]float64 {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) >= 2 {
		pts := make([][2]float64, 0, len(arr))
		for _, p := range arr {
			x, y, ok := parsePoint(p)
			if !ok || !finite(x) || !finite(y) {
				pts = nil
				break
			}
			pts = append(pts, [2]float64{x, y})
		}
		if len(pts) >= 2 {
			out := make([][4]float64, 0, len(pts)-1)
			for i := 0; i+1 < len(pts); i++ {
				out = append(out, [4]float64{pts[i][0], pts[i][1], pts[i+1][0], pts[i+1][1]})
			}
			return out
		}
	}
	if x1, y1, x2, y2, ok := parseSegPixels(raw); ok {
		return [][4]float64{{x1, y1, x2, y2}}
	}
	return nil
}

// portalClosed treats missing as open (window); accepts bool or 0/1.
func portalClosed(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n != 0
	}
	return false
}

// lightGlowOverlaps reports whether a light's glow disc — center (x, y),
// radius rng in grid squares — touches the board box [0,maxX]x[0,maxY]
// (R3 retention rule). A centered-on-board light always overlaps; a
// partially-off-board center is kept as-is (never clamped) so the glow
// source stays where the exporter put it; a fully-off-board disc drops.
// Callers enforce rng > 0 and finite coordinates first.
func lightGlowOverlaps(x, y, rng, maxX, maxY float64) bool {
	cx := min(max(x, 0), maxX)
	cy := min(max(y, 0), maxY)
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= rng*rng
}

// clipSegToGrid clips a grid-unit segment to [0,maxX]x[0,maxY]
// (Liang-Barsky). Fully-outside and degenerate (zero-length) segments
// report ok=false and are dropped by the caller.
func clipSegToGrid(x1, y1, x2, y2, maxX, maxY float64) (float64, float64, float64, float64, bool) {
	dx, dy := x2-x1, y2-y1
	if dx == 0 && dy == 0 {
		return 0, 0, 0, 0, false
	}
	t0, t1 := 0.0, 1.0
	if !clipParam(-dx, x1, &t0, &t1) || !clipParam(dx, maxX-x1, &t0, &t1) ||
		!clipParam(-dy, y1, &t0, &t1) || !clipParam(dy, maxY-y1, &t0, &t1) || t0 > t1 {
		return 0, 0, 0, 0, false
	}
	nx1, ny1 := x1+t0*dx, y1+t0*dy
	nx2, ny2 := x1+t1*dx, y1+t1*dy
	if nx1 == nx2 && ny1 == ny2 {
		return 0, 0, 0, 0, false
	}
	return nx1, ny1, nx2, ny2, true
}

func clipParam(p, q float64, t0, t1 *float64) bool {
	if p == 0 {
		return q >= 0
	}
	r := q / p
	if p < 0 {
		if r > *t1 {
			return false
		}
		if r > *t0 {
			*t0 = r
		}
	} else {
		if r < *t0 {
			return false
		}
		if r < *t1 {
			*t1 = r
		}
	}
	return true
}

// parseUVTTColor leniently normalizes exporter colors to lowercase RRGGBB:
// optional #/0x prefix; rgb / rgba nibbles expanded; rrggbb kept; rrggbbaa
// drops alpha. Anything else reports ok=false.
func parseUVTTColor(s string) (string, bool) {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.TrimPrefix(t, "#")
	t = strings.TrimPrefix(t, "0x")
	expand := func(nibbles string) (string, bool) {
		var b strings.Builder
		for i := 0; i < len(nibbles); i++ {
			c := nibbles[i]
			if !isHex(c) {
				return "", false
			}
			b.WriteByte(c)
			b.WriteByte(c)
		}
		return b.String(), true
	}
	switch len(t) {
	case 3:
		return expand(t)
	case 4:
		return expand(t[:3]) // drop alpha nibble (#rgba order)
	case 6:
		if isHexStr(t) {
			return t, true
		}
	case 8:
		// UVTT emits aarrggbb: drop the LEADING alpha pair, keep last 6
		// (verified vs 00-Ground-Floor: "ffeccd8b" -> "eccd8b").
		if isHexStr(t[2:]) && isHexStr(t[:2]) {
			return t[2:], true
		}
	}
	return "", false
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f'
}

func isHexStr(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isHex(s[i]) {
			return false
		}
	}
	return true
}

// Image ----------------------------------------------------------------------

// parseImage decodes the raw base64 image (no data: prefix), magic-sniffs
// png/webp/jpeg, reads dimensions, and enforces the megapixel cap. Empty
// input means "no image" (nil, nil).
func parseImage(s string) (*UVTTImage, error) {
	stripped := strings.Join(strings.Fields(s), "")
	if stripped == "" {
		return nil, nil
	}
	if strings.HasPrefix(strings.ToLower(stripped), "data:") {
		return nil, fmt.Errorf("%w: image must be raw base64, not a data: URL", ErrCorrupt)
	}
	data, err := decodeBase64Chunked(stripped)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid image base64: %v", ErrCorrupt, err)
	}
	format := sniffImage(data)
	if format == "" {
		return nil, fmt.Errorf("%w: unknown image magic (want png/webp/jpeg)", ErrCorrupt)
	}
	w, h, err := imageDims(format, data)
	if err != nil {
		return nil, fmt.Errorf("%w: unreadable %s image: %v", ErrCorrupt, format, err)
	}
	if int64(w)*int64(h) > MaxImageMegapixels {
		return nil, &ImageSizeError{Width: w, Height: h}
	}
	return &UVTTImage{Format: format, Width: w, Height: h, Data: data}, nil
}

// decodeBase64Chunked decodes in fixed chunks so only one small scratch
// buffer is live at a time. A missing-pad final quantum is retried
// padding-free as a leniency.
func decodeBase64Chunked(s string) ([]byte, error) {
	if int64(len(s))*3/4 > maxImageDecodedBytes {
		return nil, fmt.Errorf("image exceeds %dMiB decoded cap", maxImageDecodedBytes/(1<<20))
	}
	out := make([]byte, 0, min(len(s)*3/4+3, maxImageDecodedBytes))
	for i := 0; i < len(s); i += base64ChunkSize {
		j := min(i+base64ChunkSize, len(s))
		part := s[i:j]
		buf := make([]byte, base64.StdEncoding.DecodedLen(len(part)))
		n, err := base64.StdEncoding.Decode(buf, []byte(part))
		if err != nil {
			if j == len(s) && len(part)%4 != 0 {
				buf = make([]byte, base64.RawStdEncoding.DecodedLen(len(part)))
				n, err = base64.RawStdEncoding.Decode(buf, []byte(part))
			}
			if err != nil {
				return nil, err
			}
		}
		out = append(out, buf[:n]...)
		if int64(len(out)) > maxImageDecodedBytes {
			return nil, fmt.Errorf("image exceeds %dMiB decoded cap", maxImageDecodedBytes/(1<<20))
		}
	}
	return out, nil
}

var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// sniffImage reports "png", "webp", "jpeg", or "" for unknown magic.
func sniffImage(data []byte) string {
	if len(data) >= 8 && bytes.Equal(data[:8], pngMagic) {
		return "png"
	}
	if len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "webp"
	}
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xD8 {
		return "jpeg"
	}
	return ""
}

// imageDims reads dimensions without a full decode: stdlib DecodeConfig
// for png/jpeg (header-only input suffices), manual RIFF parse for webp
// (stdlib has no webp decoder).
func imageDims(format string, data []byte) (int, int, error) {
	switch format {
	case "png":
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return 0, 0, err
		}
		return cfg.Width, cfg.Height, nil
	case "jpeg":
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return 0, 0, err
		}
		return cfg.Width, cfg.Height, nil
	case "webp":
		w, h, ok := webpDims(data)
		if !ok {
			return 0, 0, errors.New("unparseable webp chunks")
		}
		return w, h, nil
	}
	return 0, 0, errors.New("unknown format")
}

// webpDims parses the RIFF container for the first VP8 / VP8L / VP8X chunk
// carrying dimensions. VP8X is best-effort (see package doc).
func webpDims(data []byte) (int, int, bool) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0, false
	}
	off := 12
	for iter := 0; iter < 16 && off+8 <= len(data); iter++ {
		fourcc := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		body := data[off+8:]
		if len(body) > size {
			body = body[:size]
		}
		switch fourcc {
		case "VP8 ":
			// Frame tag (3) + start code 9D 012A + 2x 14-bit LE dims.
			if len(body) >= 10 && body[3] == 0x9D && body[4] == 0x01 && body[5] == 0x2A {
				w := int(body[6]) | int(body[7]&0x3F)<<8
				h := int(body[8]) | int(body[9]&0x3F)<<8
				if w > 0 && h > 0 {
					return w, h, true
				}
			}
			return 0, 0, false
		case "VP8L":
			// Signature 0x2F + 32 LE bits: 14 width-1, 14 height-1, ...
			if len(body) >= 5 && body[0] == 0x2F {
				bits := binary.LittleEndian.Uint32(body[1:5])
				w := int(bits&0x3FFF) + 1
				h := int((bits>>14)&0x3FFF) + 1
				if w > 0 && h > 0 {
					return w, h, true
				}
			}
			return 0, 0, false
		case "VP8X":
			// flags byte + 3 reserved bytes + 24-bit LE width-1 +
			// 24-bit LE height-1 (verified vs a real animated VP8X).
			if len(body) >= 10 {
				w := int(body[4]) | int(body[5])<<8 | int(body[6])<<16
				h := int(body[7]) | int(body[8])<<8 | int(body[9])<<16
				return w + 1, h + 1, true
			}
			return 0, 0, false
		}
		off += 8 + size + (size & 1) // chunks are pad-aligned
	}
	return 0, 0, false
}

func finite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}
