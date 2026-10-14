// Package vtt implements the VTT (spec §9).
//
// Scope fence (spec §9): image + fog + tokens + dice tray + initiative ONLY.
// No WebSockets (Datastar SSE via the frozen /events stream), no lighting
// engine, no dashboard code (I2 owns the dashboard; it embeds our fragment).
//
// Contracts consumed (frozen, I2 exit — never reshaped here):
//   - plugins.MapFragmentPath  GET /vtt/{mapID}/fragment → inner HTML for #vtt-map
//   - plugins.StateSnapshotPath GET /api/vtt/{mapID}/state → plugins.StateSnapshot
//   - SSE event names on /events: hello, secret-flip (M1-immutable) plus
//     dice-roll, dice-blind, initiative-update, token-move, token-hp,
//     fog-update, encounter-spawn, session-recap.
//
// Storage (P08 locked Option A):
//   - Grid calibration/size/rotation + background + fog/token defaults live in
//     the sidecar page maps/<mapID>.md frontmatter under the `vtt-map` key
//     (Obsidian-visible, survives DB rebuild; parsers never rewrite source).
//   - Live token positions/fog/visibility live in the app DB tables
//     vtt_maps/vtt_tokens(+0003 display columns)/vtt_fog/vtt_initiative
//     (ephemeral; index rebuilds never touch them; full data-dir loss resets
//     to sidecar defaults with fog fail-closed to fully hidden).
//
// Security: login required for every endpoint (guests: 401, nothing live);
// hidden-token ids/names/positions/character links are filtered server-side
// per viewer and never emitted to unauthorized viewers; map assets serve only
// through the existing ACL-checked /assets handler (this package only
// renders the URL, never the bytes).
package vtt

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

// SidecarDir is the vault directory holding map sidecar pages:
// maps/<mapID>.md. The sidecar's own secret/owner/editable-by frontmatter is
// the map ACL (nearest-ancestor inheritance applies via the index row).
const SidecarDir = "maps"

// FrontmatterKey is the sidecar calibration key (append-only amend, Phase 4).
const FrontmatterKey = "vtt-map"

// Map path helpers -----------------------------------------------------------

// windowsReserved reports Windows-reserved file stems (case-insensitive).
func windowsReserved(s string) bool {
	switch strings.ToUpper(s) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	}
	return false
}

// ValidMapID reports whether id is a safe map slug: 1-64 chars of
// alphanumerics/dash/underscore, not a Windows-reserved name.
func ValidMapID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return !windowsReserved(id)
}

// ValidTokenID is the same shape as ValidMapID (token ids ride SSE payloads
// and DB keys; keep them filesystem-safe too).
func ValidTokenID(id string) bool { return ValidMapID(id) }

// SidecarPath maps a validated map id to its vault-relative sidecar page.
func SidecarPath(mapID string) string { return SidecarDir + "/" + mapID + ".md" }

// MapIDFromPath extracts the map id from a request path with the given
// prefix/suffix shape (e.g. prefix "/vtt/", suffix "/fragment"). It returns
// ok=false for malformed or unsafe ids. Matching is prefix-based because the
// frozen route patterns carry {mapID} mid-pattern (see toStdlibPattern).
func MapIDFromPath(p, prefix, suffix string) (id string, ok bool) {
	if !strings.HasPrefix(p, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(p, prefix)
	if suffix != "" {
		if !strings.HasSuffix(rest, suffix) {
			return "", false
		}
		rest = strings.TrimSuffix(rest, suffix)
	}
	rest = strings.Trim(rest, "/")
	if strings.Contains(rest, "/") || !ValidMapID(rest) {
		return "", false
	}
	return rest, true
}

// Calibration ----------------------------------------------------------------

// Grid bounds (P08: grid calibration in sidecar; A-VTT2 raised the cap to
// admit 96x72-class UVTT maps — rendering stays percentage-based, so cost
// scales with rects/tokens, not cells).
const (
	MaxGridCols = 128
	MaxGridRows = 128
	MinCellPx   = 16
	MaxCellPx   = 256
)

// Grid is the sidecar grid calibration.
type Grid struct {
	Cols    int // cells wide (1..128)
	Rows    int // cells high (1..128)
	CellPx  int // render cell size in px (16..256, display hint only)
	OffsetX int // background offset, px
	OffsetY int // background offset, px
	// Rotation is one of 0/90/180/270 degrees (display hint).
	Rotation int
}

// DefaultGrid is used when the sidecar carries no (or a broken) grid block.
func DefaultGrid() Grid { return Grid{Cols: 20, Rows: 14, CellPx: 48} }

// TokenDefault is one sidecar token default (initial board layout; live rows
// in the app DB win once present, and a GM "reset to sidecar" reseed restores
// these after data-dir loss).
type TokenDefault struct {
	ID        string
	Name      string
	X, Y      int
	HP, MaxHP int
	Hidden    bool
	Character string
}

// Calibration is the parsed sidecar `vtt-map` block.
type Calibration struct {
	Background string // vault-relative asset path ("" = none)
	Grid       Grid
	// GridBaked reports the background art carries its own grid: renderers
	// skip the overlay grid lines (tokens/fog still render). Default false
	// (nogrid art, including dd2vtt embedded images — overlay rendered).
	GridBaked bool
	// FogDefault is the GM-reseed posture ("hidden" default, "revealed").
	FogDefault string
	Defaults   []TokenDefault
	// PPG is the source pixels-per-grid (info only; grid.Cell holds the
	// clamped display value).
	PPG float64
	// Ambient is normalized lowercase rrggbb ("" = absent).
	Ambient string
	// BakedLighting warns that art already has light/shadow rendered.
	BakedLighting bool
	// Walls are sight+movement blockers, Objects movement-only blockers,
	// all in grid-square floats (UVTT convention; reuses the importer
	// types so the sidecar writer stays mechanical).
	Walls   []WallSeg
	Objects []WallSeg
	// Portals are door/window spans with sidecar default states.
	Portals []Portal
	// Lights are dynamic lights in grid-square units (range in squares).
	Lights []Light
	// UVTTSource is the original import filename (audit only).
	UVTTSource string
}

// ParseCalibration parses the sidecar `vtt-map` frontmatter value (nil = no
// block). It never fails: broken values fall back to defaults field-by-field
// so one bad key cannot brick the board (the page itself quarantines on a
// non-map `vtt-map` per the Lane K frontmatter amend).
func ParseCalibration(v any) Calibration {
	c := Calibration{Grid: DefaultGrid(), FogDefault: "hidden"}
	m, ok := v.(map[string]any)
	if !ok {
		return c
	}
	if bg, ok := asString(m["background"]); ok {
		if cleanAssetRef(bg) {
			c.Background = bg
		}
	}
	if g, ok := m["grid"].(map[string]any); ok {
		c.Grid = parseGrid(g)
	}
	if fd, ok := asString(m["fog-default"]); ok {
		if fd == "revealed" || fd == "hidden" {
			c.FogDefault = fd
		}
	}
	if b, ok := asBool(m["grid-baked"]); ok {
		c.GridBaked = b
	}
	if n, ok := asFloat(m["ppg"]); ok && n > 0 && finite(n) {
		c.PPG = n
	}
	if s, ok := asString(m["ambient"]); ok {
		if norm, ok := normalizeColor(s); ok {
			c.Ambient = norm
		}
	}
	if b, ok := asBool(m["baked-lighting"]); ok {
		c.BakedLighting = b
	}
	c.Walls = parseWallSegs(m["walls"], c.Grid)
	c.Objects = parseWallSegs(m["blocking-objects"], c.Grid)
	c.Portals = parsePortalStates(m["portals"], c.Grid)
	c.Lights = parseLightStates(m["lights"], c.Grid)
	if s, ok := asString(m["uvtt-source"]); ok {
		c.UVTTSource = truncate(sanitizeSource(s), 128)
	}
	if toks, ok := m["tokens"].([]any); ok {
		for _, t := range toks {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			d, ok := parseTokenDefault(tm, c.Grid)
			if ok {
				c.Defaults = append(c.Defaults, d)
			}
		}
	}
	return c
}

func parseGrid(g map[string]any) Grid {
	out := DefaultGrid()
	if n, ok := asInt(g["cols"]); ok && n >= 1 && n <= MaxGridCols {
		out.Cols = n
	}
	if n, ok := asInt(g["rows"]); ok && n >= 1 && n <= MaxGridRows {
		out.Rows = n
	}
	if n, ok := asInt(g["cell"]); ok && n >= MinCellPx && n <= MaxCellPx {
		out.CellPx = n
	}
	if n, ok := asInt(g["offset-x"]); ok && abs(n) <= 4096 {
		out.OffsetX = n
	}
	if n, ok := asInt(g["offset-y"]); ok && abs(n) <= 4096 {
		out.OffsetY = n
	}
	if n, ok := asInt(g["rotation"]); ok && (n == 0 || n == 90 || n == 180 || n == 270) {
		out.Rotation = n
	}
	return out
}

func parseTokenDefault(tm map[string]any, g Grid) (TokenDefault, bool) {
	id, ok := asString(tm["id"])
	if !ok || !ValidTokenID(id) {
		return TokenDefault{}, false
	}
	d := TokenDefault{ID: id}
	if s, ok := asString(tm["name"]); ok {
		d.Name = truncate(s, 80)
	}
	if d.Name == "" {
		d.Name = id
	}
	if n, ok := asInt(tm["x"]); ok {
		d.X = n
	}
	if n, ok := asInt(tm["y"]); ok {
		d.Y = n
	}
	if d.X < 0 || d.Y < 0 || d.X >= g.Cols || d.Y >= g.Rows {
		return TokenDefault{}, false
	}
	if n, ok := asInt(tm["hp"]); ok && n >= 0 && n <= 9999 {
		d.HP = n
	}
	if n, ok := asInt(tm["max-hp"]); ok && n >= 0 && n <= 9999 {
		d.MaxHP = n
	}
	if d.HP > d.MaxHP {
		d.HP = d.MaxHP
	}
	if b, ok := asBool(tm["hidden"]); ok {
		d.Hidden = b
	}
	if s, ok := asString(tm["character"]); ok && cleanPageRef(s) {
		d.Character = s
	}
	return d, true
}

// cleanAssetRef validates a sidecar background reference: vault-relative
// posix path, no traversal/absolute/drive shapes, never a markdown source
// (those are pages, not assets — the ACL asset handler refuses them anyway).
func cleanAssetRef(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	if strings.Contains(p, "..") || strings.HasPrefix(p, ".") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || windowsReserved(strings.TrimSuffix(seg, path.Ext(seg))) {
			return false
		}
	}
	return !strings.HasSuffix(strings.ToLower(p), ".md")
}

// cleanPageRef validates a token→character link: a vault-relative .md page.
func cleanPageRef(p string) bool {
	if !strings.HasSuffix(strings.ToLower(p), ".md") {
		return false
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.Contains(p, "..") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || strings.HasPrefix(seg, ".") {
			return false
		}
	}
	return true
}

// Fog codec ------------------------------------------------------------------

// Rect is one hidden fog cell-rectangle (grid coords, half-open).
type Rect struct{ X, Y, W, H int }

// fogCodecVersion pins the Shape/mask encoding (opaque to I2; interpreted
// only here). Shape "v1;<cols>x<rows>;<x,y,w,h;...>" lists HIDDEN cells.
// This satisfies the frozen-contract review: the opaque string carries grid
// dimensions + hidden rects, everything the board renderer needs.
const fogCodecVersion = "v1"

// EncodeMask canonicalizes hidden rects for storage (vtt_fog.mask) and the
// snapshot Shape: clipped to the grid, degenerate rects dropped, sorted.
// Empty rects (fully revealed) encode as "v1;WxH;" — callers must still treat
// a MISSING/EMPTY mask row as fully hidden (fail closed); only an explicit
// stored mask (even the revealed encoding) opens the board.
func EncodeMask(g Grid, rects []Rect) string {
	var kept []Rect
	for _, r := range rects {
		if r.W <= 0 || r.H <= 0 {
			continue
		}
		if r.X < 0 {
			r.W += r.X
			r.X = 0
		}
		if r.Y < 0 {
			r.H += r.Y
			r.Y = 0
		}
		if r.X >= g.Cols || r.Y >= g.Rows {
			continue
		}
		if r.X+r.W > g.Cols {
			r.W = g.Cols - r.X
		}
		if r.Y+r.H > g.Rows {
			r.H = g.Rows - r.Y
		}
		if r.W > 0 && r.H > 0 {
			kept = append(kept, r)
		}
	}
	sort.Slice(kept, func(i, j int) bool {
		if kept[i].Y != kept[j].Y {
			return kept[i].Y < kept[j].Y
		}
		if kept[i].X != kept[j].X {
			return kept[i].X < kept[j].X
		}
		if kept[i].H != kept[j].H {
			return kept[i].H < kept[j].H
		}
		return kept[i].W < kept[j].W
	})
	var b strings.Builder
	fmt.Fprintf(&b, "%s;%dx%d;", fogCodecVersion, g.Cols, g.Rows)
	for i, r := range kept {
		if i > 0 {
			b.WriteByte(';')
		}
		fmt.Fprintf(&b, "%d,%d,%d,%d", r.X, r.Y, r.W, r.H)
	}
	return b.String()
}

// DecodeMask parses a stored mask/Shape back to hidden rects. ok=false means
// "unusable" and the caller MUST fail closed (fully hidden), never fail open.
func DecodeMask(mask string, g Grid) (rects []Rect, ok bool) {
	parts := strings.Split(mask, ";")
	if len(parts) < 2 || parts[0] != fogCodecVersion {
		return nil, false
	}
	var cols, rows int
	if _, err := fmt.Sscanf(parts[1], "%dx%d", &cols, &rows); err != nil || cols != g.Cols || rows != g.Rows {
		return nil, false
	}
	for _, p := range parts[2:] {
		if p == "" {
			continue
		}
		var r Rect
		if _, err := fmt.Sscanf(p, "%d,%d,%d,%d", &r.X, &r.Y, &r.W, &r.H); err != nil {
			return nil, false
		}
		if r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > g.Cols || r.Y+r.H > g.Rows {
			return nil, false
		}
		rects = append(rects, r)
	}
	return rects, true
}

// FogView is the resolved fog posture for one viewer render.
type FogView struct {
	// Hidden forces the client to render fully hidden (fail-closed: data
	// loss, corrupt mask, or unviewable map).
	Hidden bool
	// Shape is the opaque geometry encoding (valid only when !Hidden).
	Shape string
	// Rects are the decoded hidden rects (valid only when !Hidden).
	Rects []Rect
}

// ResolveFog maps a stored mask row to a FogView. Missing/empty/corrupt/grid-
// mismatched masks resolve Hidden (fail closed, never fail open).
func ResolveFog(mask string, present bool, g Grid) FogView {
	if !present || strings.TrimSpace(mask) == "" {
		return FogView{Hidden: true}
	}
	rects, ok := DecodeMask(strings.TrimSpace(mask), g)
	if !ok {
		return FogView{Hidden: true}
	}
	return FogView{Shape: EncodeMask(g, rects), Rects: rects}
}

// sidecarFogMask builds the seed mask for a GM "reset to sidecar": hidden
// default → full-board rect; revealed → empty (explicit revealed encoding).
func sidecarFogMask(c Calibration) string {
	if c.FogDefault == "revealed" {
		return EncodeMask(c.Grid, nil)
	}
	return EncodeMask(c.Grid, []Rect{{X: 0, Y: 0, W: c.Grid.Cols, H: c.Grid.Rows}})
}

// scalar coercions (frontmatter values arrive as int64/float64/string/bool) ---

func asString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case nil:
		return "", false
	default:
		return "", false
	}
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		if t != float64(int(t)) {
			return 0, false
		}
		return int(t), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

func asBool(v any) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "yes", "y", "on":
			return true, true
		case "false", "no", "n", "off":
			return false, true
		}
	}
	return false, false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// A-VTT2 calibration sub-keys (append-only inside `vtt-map`; broken values
// drop field-by-field, never quarantine — Obsidian users hand-edit sidecars).

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case float64:
		return t, true
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(t), "%g", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

func asFloatList(v any) ([]float64, bool) {
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]float64, 0, len(items))
	for _, e := range items {
		f, ok := asFloat(e)
		if !ok {
			return nil, false
		}
		out = append(out, f)
	}
	return out, true
}

// normalizeColor parses a lenient rrggbb color: strips a leading `#` or
// `0x`, lowercases, drops a leading alpha pair (UVTT emits aarrggbb), and
// requires exactly 6 hex digits afterwards. Anything else is absent.
func normalizeColor(s string) (string, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "#")
	s = strings.TrimPrefix(s, "0x")
	if len(s) == 8 {
		s = s[2:]
	}
	if len(s) != 6 {
		return "", false
	}
	for i := 0; i < 6; i++ {
		if !isHex(s[i]) {
			return "", false
		}
	}
	return s, true
}

// parseWallSegs parses `walls` / `blocking-objects`: lists of 4-float
// `[x1,y1,x2,y2]` segments in grid squares, clipped to the grid. Bad segs
// drop individually; zero-length segs drop.
func parseWallSegs(v any, g Grid) []WallSeg {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	maxX, maxY := float64(g.Cols), float64(g.Rows)
	var out []WallSeg
	for _, e := range items {
		nums, ok := asFloatList(e)
		if !ok || len(nums) != 4 {
			continue
		}
		for _, n := range nums {
			if !finite(n) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if cx1, cy1, cx2, cy2, ok := clipSegToGrid(nums[0], nums[1], nums[2], nums[3], maxX, maxY); ok {
			if cx1 != cx2 || cy1 != cy2 {
				out = append(out, WallSeg{cx1, cy1, cx2, cy2})
			}
		}
	}
	return out
}

func parsePointList(v any) ([2]float64, bool) {
	nums, ok := asFloatList(v)
	if !ok || len(nums) != 2 || !finite(nums[0]) || !finite(nums[1]) {
		return [2]float64{}, false
	}
	return [2]float64{nums[0], nums[1]}, true
}

// parsePortalStates parses `portals`: `{a:[x1,y1], b:[x2,y2], closed: bool}`.
// Non-bool `closed` defaults to true (fail-closed). Fully out-of-grid
// portals drop; partial ones clip.
func parsePortalStates(v any, g Grid) []Portal {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	maxX, maxY := float64(g.Cols), float64(g.Rows)
	var out []Portal
	for _, e := range items {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		a, ok := parsePointList(m["a"])
		if !ok {
			continue
		}
		b, ok := parsePointList(m["b"])
		if !ok {
			continue
		}
		closed := true
		if c, ok := asBool(m["closed"]); ok {
			closed = c
		}
		if cx1, cy1, cx2, cy2, ok := clipSegToGrid(a[0], a[1], b[0], b[1], maxX, maxY); ok {
			out = append(out, Portal{AX: cx1, AY: cy1, BX: cx2, BY: cy2, Closed: closed})
		}
	}
	return out
}

// parseLightStates parses `lights`: `{x, y, range, color, intensity,
// shadows}` in grid squares. Out-of-grid or non-positive-range lights drop;
// unparseable colors drop the light (fail-closed absent, never invented);
// non-finite intensity falls back to 1.0; non-bool shadows to false.
func parseLightStates(v any, g Grid) []Light {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []Light
	for _, e := range items {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		x, ok := asFloat(m["x"])
		if !ok || !finite(x) || x < 0 || x > float64(g.Cols) {
			continue
		}
		y, ok := asFloat(m["y"])
		if !ok || !finite(y) || y < 0 || y > float64(g.Rows) {
			continue
		}
		rng, ok := asFloat(m["range"])
		if !ok || !finite(rng) || rng <= 0 {
			continue
		}
		color := ""
		if s, ok := asString(m["color"]); ok {
			color, ok = normalizeColor(s)
			if !ok {
				continue
			}
		} else {
			continue
		}
		intensity := 1.0
		if f, ok := asFloat(m["intensity"]); ok && finite(f) && f >= 0 {
			intensity = f
		}
		shadows := false
		if b, ok := asBool(m["shadows"]); ok {
			shadows = b
		}
		out = append(out, Light{X: x, Y: y, Range: rng, Color: color, Intensity: intensity, Shadows: shadows})
	}
	return out
}
