// UVTT import apply (amend A-VTT2 §4): one mapper shared by the GM HTTP route
// (POST /vtt/{mapID}/import) and the import-uvtt CLI.
//
// Writes (both surfaces, in order):
//  1. The map image: parser-sniffed bytes through the frozen uploads pipeline
//     (Normalize: decode + 32MP guard + downscale + EXIF-strip re-encode for
//     png/jpeg; webp stored as-is per D2) to assets/maps/<mapID>.<ext>, the
//     bundle dir that inherits the sidecar ACL. Bytes are never served from
//     the .uvtt source itself.
//  2. The sidecar page maps/<mapID>.md: the vtt-map calibration block,
//     merged surgically (D2: comments/order preserved; importer-owned keys
//     replaced, hand-edited keys such as `tokens` kept).
//
// This file performs the durable vault writes only; live-state reseed
// (ResetToSidecar + all-hidden fog + SSE) and the index refresh belong to the
// caller, which knows its own surface (HTTP handler vs CLI).

package vtt

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/semiplane/yonder/internal/uploads"
)

// MapAssetDir is the vault bundle directory holding imported map images
// (assets/maps/<mapID>.<ext>). The sidecar's ACL governs the directory.
const MapAssetDir = "assets/maps"

// maxImportImageBytes bounds a single imported image beyond the HTTP body
// cap (the CLI is uncapped per D1): map art is legitimately larger than a
// page image, but not unbounded.
const maxImportImageBytes = 64 << 20

// maxHTTPImportImageBytes bounds one stored image on the HTTP route (R1:
// 40MB, the largest image encodable in a 55MB body at base64 ×4/3 plus
// envelope). webp is stored as-is (no decode transient); png/jpeg stay
// under the 32MP uploads.Normalize guard and downscale to 1600px, so stored
// bytes stay small. Anything larger keeps the "use the import-uvtt CLI" 422.
const maxHTTPImportImageBytes = 40 << 20

// ImportRequest is one parsed import ready to apply.
type ImportRequest struct {
	Vault VaultWriter
	MapID string
	Map   *UVTTMap
	// Background optionally replaces the embedded image: bytes of an
	// external file (CLI --background). Nil uses the UVTT embedded image
	// (and when the file carries none, no background key is written).
	Background    []byte
	BackgroundExt string // "png" | "webp" | "jpeg" (sniffed by the caller)
	// ImageByteCap bounds one stored image: the HTTP route passes
	// maxHTTPImportImageBytes (40MB, R1 — the largest image encodable in a
	// 55MB body), the CLI passes 0 (uncapped per D1 — "larger files via
	// CLI"). The parser's 96MB decoded cap and the 128MP dims cap bound
	// both surfaces regardless.
	ImageByteCap int
	// FreshSecret stamps `secret: true` on a sidecar this import CREATES
	// (the maps/ tree is secret). Existing sidecars keep their own flag.
	// The HTTP route computes it from the index; the CLI (a local GM tool
	// against the GM's own vault) leaves it false.
	FreshSecret bool
}

// ImportResult reports what an import wrote (for CLI output + tests).
type ImportResult struct {
	SidecarPath string // maps/<mapID>.md
	AssetPath   string // assets/maps/<mapID>.<ext> ("" when no image)
	ImageFormat string // png/webp/jpeg ("" when no image)
	Cols, Rows  int
	Cell        int
	Walls       int
	Objects     int
	Portals     int
	Lights      int
	Ambient     string
	GridBaked   bool
	BakedLight  bool
	Source      string
}

// VaultWriter is the import write seam (the web.VaultWriter import would
// cycle: web does not import vtt, vtt imports web). *vault.Vault satisfies
// it; tests use the same interface.
type VaultWriter interface {
	WriteFile(ctx context.Context, path, content string) error
	WriteFileBytes(ctx context.Context, path string, data []byte) error
	ReadFile(ctx context.Context, path string) ([]byte, error)
}

// ApplyImport writes the map image + sidecar calibration for one parsed
// import. It never touches live state (app DB) or the index.
func ApplyImport(ctx context.Context, req ImportRequest) (*ImportResult, error) {
	if req.Vault == nil {
		return nil, errors.New("import: vault not configured")
	}
	if !ValidMapID(req.MapID) {
		return nil, fmt.Errorf("import: bad map id %q", req.MapID)
	}
	m := req.Map
	if m == nil {
		return nil, errors.New("import: no parsed map")
	}
	res := &ImportResult{
		SidecarPath: SidecarPath(req.MapID),
		Cols:        m.Cols, Rows: m.Rows, Cell: m.Cell,
		Walls: len(m.Walls), Objects: len(m.Objects),
		Portals: len(m.Portals), Lights: len(m.Lights),
		Ambient: m.Ambient, GridBaked: m.GridBaked, BakedLight: m.BakedLighting,
		Source: m.Source,
	}
	// 1. Image: external --background bytes or the UVTT embedded image.
	assetRel := ""
	imgFormat := ""
	if len(req.Background) > 0 {
		out, format, err := processImportImage(req.Background, req.BackgroundExt, req.ImageByteCap)
		if err != nil {
			return nil, err
		}
		assetRel, imgFormat = MapAssetDir+"/"+req.MapID+"."+imageExt(format), format
		if err := req.Vault.WriteFileBytes(ctx, assetRel, out); err != nil {
			return nil, fmt.Errorf("import: write background: %w", err)
		}
	} else if m.Image != nil && len(m.Image.Data) > 0 {
		out, format, err := processImportImage(m.Image.Data, m.Image.Format, req.ImageByteCap)
		if err != nil {
			return nil, err
		}
		assetRel, imgFormat = MapAssetDir+"/"+req.MapID+"."+imageExt(format), format
		if err := req.Vault.WriteFileBytes(ctx, assetRel, out); err != nil {
			return nil, fmt.Errorf("import: write image: %w", err)
		}
	}
	res.AssetPath, res.ImageFormat = assetRel, imgFormat
	// 2. Sidecar: surgical vtt-map merge over whatever is on disk.
	existing := ""
	if b, err := req.Vault.ReadFile(ctx, res.SidecarPath); err == nil {
		existing = string(b)
	}
	updated := applyVTTMapBlock(existing, req.MapID, sidecarVTTMapEntries(m, assetRel),
		sidecarBodyFor(req.MapID, m, assetRel), req.FreshSecret)
	if err := req.Vault.WriteFile(ctx, res.SidecarPath, updated); err != nil {
		return nil, fmt.Errorf("import: write sidecar: %w", err)
	}
	return res, nil
}

// sidecarBodyFor is the prose a FRESH sidecar gets (existing pages keep
// their body byte-for-byte). It documents the map in GM terms and, when no
// image was written, names the conventional art path to link.
func sidecarBodyFor(mapID string, m *UVTTMap, assetRel string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", mapID)
	fmt.Fprintf(&b, "Imported UVTT map (`%s`): grid %dx%d at %dpx cells, %d walls, %d blocking objects, %d portals, %d lights.\n",
		m.Source, m.Cols, m.Rows, m.Cell, len(m.Walls), len(m.Objects), len(m.Portals), len(m.Lights))
	if m.Ambient != "" {
		fmt.Fprintf(&b, "Ambient light #%s; baked lighting: %v.\n", m.Ambient, m.BakedLighting)
	}
	b.WriteString("\nThis page holds calibration only — live tokens, fog, and door states live in the app DB\n")
	b.WriteString("and reseed from this page when a GM clicks **Reset to sidecar** (fog always starts hidden).\n")
	if assetRel == "" {
		fmt.Fprintf(&b, "\nNo background art is committed (UVTT imports can link it instead of vendoring the\n"+
			"bytes). Drop the export image at `assets/maps/%s.png` (or `.webp`/`.jpg`) and add\n"+
			"`background: assets/maps/%s.png` to the `vtt-map` block above when you want art on the\n"+
			"table; the board serves it through the ACL-checked `/assets` handler.\n", mapID, mapID)
	} else {
		fmt.Fprintf(&b, "\nBackground art: `%s` (served through the ACL-checked `/assets` handler, never raw).\n", assetRel)
	}
	return b.String()
}

// CalibrationOf projects a parsed import onto the sidecar Calibration the
// live-state reseed consumes (same values the sidecar now carries; fog
// posture is always hidden after an import — fail closed).
func CalibrationOf(m *UVTTMap, assetRel string) Calibration {
	return Calibration{
		Background:    assetRel,
		Grid:          Grid{Cols: m.Cols, Rows: m.Rows, CellPx: m.Cell},
		GridBaked:     m.GridBaked,
		FogDefault:    "hidden",
		PPG:           m.PPG,
		Ambient:       m.Ambient,
		BakedLighting: m.BakedLighting,
		Walls:         m.Walls,
		Objects:       m.Objects,
		Portals:       m.Portals,
		Lights:        m.Lights,
		UVTTSource:    m.Source,
	}
}

// processImportImage runs import image bytes through the frozen uploads
// pipeline. The parser already magic-sniffed + dim-capped the payload, so
// this applies the write-half policy of D2: png/jpeg go through
// uploads.Normalize (decode, 32MP MaxPixels guard, DisplayMaxDim downscale,
// EXIF-strip re-encode — over-cap png/jpeg must arrive as webp instead),
// webp is stored as-is (never fully decoded at import). byteCap > 0 is the
// 40MB image cap the HTTP route applies (R1 — "larger files via CLI" — the
// CLI passes 0, still bounded by the parser's 96MB decoded cap).
func processImportImage(data []byte, format string, byteCap int) ([]byte, string, error) {
	if len(data) == 0 {
		return nil, "", errors.New("import: empty image")
	}
	if byteCap > 0 && len(data) > byteCap {
		return nil, "", fmt.Errorf("import: image is %d bytes (cap %d — use the import-uvtt CLI for larger art)", len(data), byteCap)
	}
	if len(data) > maxImportImageBytes {
		return nil, "", fmt.Errorf("import: image is %d bytes (cap %d)", len(data), maxImportImageBytes)
	}
	switch format {
	case "webp":
		return data, "webp", nil
	case "png", "jpeg":
		mime := uploads.MIMEPNG
		if format == "jpeg" {
			mime = uploads.MIMEJPEG
		}
		out, err := uploads.Normalize(data, mime, uploads.DisplayMaxDim)
		if err != nil {
			if errors.Is(err, uploads.ErrTooManyPixels) {
				return nil, "", fmt.Errorf("import: %s exceeds the 32MP write-path cap (convert to webp first)", format)
			}
			return nil, "", fmt.Errorf("import: process %s: %w", format, err)
		}
		return out, format, nil
	}
	return nil, "", fmt.Errorf("import: unsupported image format %q", format)
}

// imageExt maps the sniffed format to the stored extension (the sidecar
// background reference and the asset filename always agree).
func imageExt(format string) string {
	switch format {
	case "png":
		return "png"
	case "jpeg":
		return "jpg"
	case "webp":
		return "webp"
	}
	return "bin"
}

// ---------------------------------------------------------------------------
// Sidecar serialization (vtt-map sub-keys, append-only per the amend)
// ---------------------------------------------------------------------------

// importerOwnedKeys lists the vtt-map sub-keys the importer writes (and
// therefore replaces on re-import). Every OTHER sub-key inside vtt-map
// (tokens, fog-default, hand edits) survives the merge.
var importerOwnedKeys = []string{
	"background", "grid", "grid-baked", "ppg", "ambient", "baked-lighting",
	"walls", "blocking-objects", "portals", "lights", "uvtt-source",
}

// sidecarEntry is one rendered `key: value` frontmatter entry.
type sidecarEntry struct {
	key   string
	lines []string // full lines (first carries "key: ..."), no line endings
}

// sidecarVTTMapEntries renders the importer-owned calibration entries for one
// parsed map. Empty collections are omitted (absent = none), matching the
// append-only, degrade-field-by-field sidecar contract.
func sidecarVTTMapEntries(m *UVTTMap, assetRel string) []sidecarEntry {
	var out []sidecarEntry
	if assetRel != "" {
		out = append(out, sidecarEntry{key: "background", lines: []string{"  background: " + quoteScalar(assetRel)}})
	}
	out = append(out, sidecarEntry{key: "grid", lines: []string{
		fmt.Sprintf("  grid: {cols: %d, rows: %d, cell: %d}", m.Cols, m.Rows, m.Cell),
	}})
	out = append(out, sidecarEntry{key: "grid-baked", lines: []string{
		"  grid-baked: " + strconv.FormatBool(m.GridBaked),
	}})
	out = append(out, sidecarEntry{key: "ppg", lines: []string{
		"  ppg: " + formatFloat(m.PPG),
	}})
	if m.Ambient != "" {
		out = append(out, sidecarEntry{key: "ambient", lines: []string{"  ambient: " + quoteScalar(m.Ambient)}})
	}
	if m.BakedLighting {
		out = append(out, sidecarEntry{key: "baked-lighting", lines: []string{"  baked-lighting: true"}})
	}
	if len(m.Walls) > 0 {
		out = append(out, wallEntry("walls", m.Walls))
	}
	if len(m.Objects) > 0 {
		out = append(out, wallEntry("blocking-objects", m.Objects))
	}
	if len(m.Portals) > 0 {
		lines := make([]string, 0, len(m.Portals))
		for _, p := range m.Portals {
			lines = append(lines, fmt.Sprintf("    - {a: [%s, %s], b: [%s, %s], closed: %s}",
				formatFloat(p.AX), formatFloat(p.AY), formatFloat(p.BX), formatFloat(p.BY),
				strconv.FormatBool(p.Closed)))
		}
		out = append(out, sidecarEntry{key: "portals", lines: append([]string{"  portals:"}, lines...)})
	}
	if len(m.Lights) > 0 {
		lines := make([]string, 0, len(m.Lights))
		for _, l := range m.Lights {
			lines = append(lines, fmt.Sprintf("    - {x: %s, y: %s, range: %s, color: %s, intensity: %s, shadows: %s}",
				formatFloat(l.X), formatFloat(l.Y), formatFloat(l.Range),
				quoteScalar(l.Color), formatFloat(l.Intensity), strconv.FormatBool(l.Shadows)))
		}
		out = append(out, sidecarEntry{key: "lights", lines: append([]string{"  lights:"}, lines...)})
	}
	out = append(out, sidecarEntry{key: "uvtt-source", lines: []string{"  uvtt-source: " + quoteScalar(m.Source)}})
	return out
}

func wallEntry(key string, segs []WallSeg) sidecarEntry {
	lines := make([]string, 0, len(segs)+1)
	lines = append(lines, "  "+key+":")
	for _, s := range segs {
		lines = append(lines, fmt.Sprintf("    - [%s, %s, %s, %s]",
			formatFloat(s.X1), formatFloat(s.Y1), formatFloat(s.X2), formatFloat(s.Y2)))
	}
	return sidecarEntry{key: key, lines: lines}
}

// formatFloat renders a grid coordinate compactly: clipped float tails round
// away (Liang-Barsky leaves 0.9999999999999999), integers stay bare so the
// YAML subset parses them back as ints.
func formatFloat(f float64) string {
	if !finite(f) {
		return "0"
	}
	r := math.Round(f*1e4) / 1e4
	if r == 0 {
		return "0"
	}
	return strconv.FormatFloat(r, 'g', -1, 64)
}

// quoteScalar renders a scalar safe for a frontmatter line: bare when simple,
// double-quoted otherwise (same policy as the write path's quoteYAML).
func quoteScalar(s string) string {
	simple := s != "" && !strings.ContainsAny(s, ":#{}[],&*!|>'\"%@`") &&
		!strings.HasPrefix(s, " ") && !strings.HasSuffix(s, " ") &&
		!strings.ContainsAny(s, "\r\n\t")
	if simple {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString("\\n")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
