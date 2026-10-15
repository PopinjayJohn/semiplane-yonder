// Import apply tests: the surgical sidecar merge (comments/order/hand edits
// preserved), the vtt-map YAML round-trip through the REAL frontmatter
// parser, the image write through a real vault, and the grid-baked posture.
package vtt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/markdown"
	"github.com/semiplane/yonder/internal/vault"
)

// existingSidecar is a hand-edited sidecar: comments, a hand-added token
// default, and an unknown (future) sub-key the importer must not touch.
const existingSidecar = `---
title: Arena
owner: gm
# map calibration below (hand-edited)
vtt-map:
  background: assets/maps/old-art.png
  # starting layout — hand-added, must survive an import
  tokens:
    - {id: mira-pc, name: Mira, x: 1, y: 1, hp: 13, max-hp: 13, character: characters/mira/index.md}
  fog-default: revealed
---

# Arena

Body prose must survive byte-for-byte.
`

func testVault(t *testing.T) (*vault.Vault, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "maps"), 0o755); err != nil {
		t.Fatal(err)
	}
	v, err := vault.NewVault(dir)
	if err != nil {
		t.Fatal(err)
	}
	return v, dir
}

func TestApplyImportRoundTrip(t *testing.T) {
	v, dir := testVault(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(dir, "maps", "arena.md"), []byte(existingSidecar), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Parse([]byte(uvttBasic(t, tinyPNG)), "ford.dd2vtt")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	res, err := ApplyImport(ctx, ImportRequest{Vault: v, MapID: "arena", Map: m})
	if err != nil {
		t.Fatalf("ApplyImport: %v", err)
	}
	if res.SidecarPath != "maps/arena.md" {
		t.Errorf("sidecar = %q", res.SidecarPath)
	}
	if res.AssetPath != "assets/maps/arena.png" || res.ImageFormat != "png" {
		t.Errorf("asset = %q (%s), want assets/maps/arena.png png", res.AssetPath, res.ImageFormat)
	}
	if res.Cols != 4 || res.Rows != 3 || res.Walls != 4 || res.Objects != 1 || res.Portals != 1 || res.Lights != 1 {
		t.Errorf("summary = %+v", res)
	}
	// Image bytes landed through the vault (1x1 PNG, re-encoded by Normalize).
	img, err := os.ReadFile(filepath.Join(dir, "assets", "maps", "arena.png"))
	if err != nil {
		t.Fatalf("image not written: %v", err)
	}
	if len(img) == 0 {
		t.Error("empty image bytes")
	}
	// Sidecar: hand edits survive, importer keys replaced.
	out, err := os.ReadFile(filepath.Join(dir, "maps", "arena.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{
		"title: Arena", "owner: gm",
		"# map calibration below (hand-edited)",
		"mira-pc", "fog-default: revealed",
		"# starting layout — hand-added, must survive an import",
		"# Arena\n\nBody prose must survive byte-for-byte.",
		"background: assets/maps/arena.png",
		"grid: {cols: 4, rows: 3, cell: 100}",
		"grid-baked: false",
		"ppg: 100",
		"ambient: ffffff",
		"uvtt-source: ford.dd2vtt",
		"walls:", "blocking-objects:", "portals:", "lights:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("sidecar missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "old-art.png") {
		t.Error("stale background key survived (owned key must be replaced)")
	}
	// Round-trip: the REAL frontmatter parser reads the written block back to
	// exactly the calibration the importer computed.
	page, err := markdown.Parse(ctx, got, "maps/arena.md")
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if page.Quarantined {
		t.Fatalf("imported sidecar quarantined: %s", page.QuarantineReason)
	}
	cal := ParseCalibration(page.Frontmatter["vtt-map"])
	want := CalibrationOf(m, res.AssetPath)
	if cal.Grid != want.Grid || cal.Background != want.Background || cal.GridBaked != want.GridBaked {
		t.Errorf("round-trip grid/bg/baked = %+v, want %+v", cal.Grid, want.Grid)
	}
	if cal.PPG != want.PPG || cal.Ambient != want.Ambient || cal.UVTTSource != want.UVTTSource {
		t.Errorf("round-trip ppg/ambient/source = %v/%q/%q, want %v/%q/%q",
			cal.PPG, cal.Ambient, cal.UVTTSource, want.PPG, want.Ambient, want.UVTTSource)
	}
	if len(cal.Walls) != len(want.Walls) || len(cal.Objects) != len(want.Objects) ||
		len(cal.Portals) != len(want.Portals) || len(cal.Lights) != len(want.Lights) {
		t.Fatalf("round-trip geometry = %d/%d/%d/%d, want %d/%d/%d/%d",
			len(cal.Walls), len(cal.Objects), len(cal.Portals), len(cal.Lights),
			len(want.Walls), len(want.Objects), len(want.Portals), len(want.Lights))
	}
	for i := range cal.Walls {
		if cal.Walls[i] != want.Walls[i] {
			t.Errorf("wall %d = %+v, want %+v", i, cal.Walls[i], want.Walls[i])
		}
	}
	for i := range cal.Portals {
		if cal.Portals[i] != want.Portals[i] {
			t.Errorf("portal %d = %+v, want %+v", i, cal.Portals[i], want.Portals[i])
		}
	}
	for i := range cal.Lights {
		if cal.Lights[i] != want.Lights[i] {
			t.Errorf("light %d = %+v, want %+v", i, cal.Lights[i], want.Lights[i])
		}
	}
	// Hand-edited sub-keys survive: the token default is still there.
	if len(cal.Defaults) != 1 || cal.Defaults[0].ID != "mira-pc" {
		t.Errorf("token defaults lost: %+v", cal.Defaults)
	}
	if cal.FogDefault != "revealed" {
		t.Errorf("fog-default lost: %q", cal.FogDefault)
	}
}

func TestApplyImportFreshSidecar(t *testing.T) {
	v, dir := testVault(t)
	ctx := context.Background()
	m, err := Parse([]byte(uvttBasic(t, "")), "fresh.uvtt") // no embedded image
	if err != nil {
		t.Fatal(err)
	}
	res, err := ApplyImport(ctx, ImportRequest{Vault: v, MapID: "tomb", Map: m})
	if err != nil {
		t.Fatalf("ApplyImport: %v", err)
	}
	if res.AssetPath != "" {
		t.Errorf("asset = %q, want none (no embedded image)", res.AssetPath)
	}
	out, err := os.ReadFile(filepath.Join(dir, "maps", "tomb.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.HasPrefix(got, "---\ntitle: tomb\nvtt-map:") {
		t.Errorf("fresh sidecar head:\n%s", got)
	}
	fm := got[:strings.Index(got, "\n---\n")+5]
	if strings.Contains(fm, "background:") {
		t.Error("fresh sidecar frontmatter must not carry a background key with no image")
	}
	if !strings.Contains(got, "No background art is committed") {
		t.Error("fresh sidecar body should name the link-not-vendor art path")
	}
	page, err := markdown.Parse(ctx, got, "maps/tomb.md")
	if err != nil || page.Quarantined {
		t.Fatalf("fresh sidecar parse: %v %v", err, page)
	}
	cal := ParseCalibration(page.Frontmatter["vtt-map"])
	if cal.Grid.Cols != 4 || cal.Grid.Rows != 3 || len(cal.Walls) != 4 {
		t.Errorf("fresh calibration = %+v", cal)
	}
}

// A sidecar whose vtt-map is flow style (one line) is rebuilt as a block.
func TestApplyImportFlowStyleBlock(t *testing.T) {
	v, dir := testVault(t)
	ctx := context.Background()
	flow := "---\ntitle: Flow\nvtt-map: {grid: {cols: 2, rows: 2}}\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "maps", "arena.md"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ := Parse([]byte(uvttBasic(t, "")), "flow.uvtt")
	res, err := ApplyImport(ctx, ImportRequest{Vault: v, MapID: "arena", Map: m})
	if err != nil {
		t.Fatalf("ApplyImport: %v", err)
	}
	if res.AssetPath != "" {
		t.Errorf("asset = %q, want none", res.AssetPath)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "maps", "arena.md"))
	page, err := markdown.Parse(ctx, string(out), "maps/arena.md")
	if err != nil || page.Quarantined {
		t.Fatalf("flow sidecar parse: %v %v\n%s", err, page, out)
	}
	cal := ParseCalibration(page.Frontmatter["vtt-map"])
	if cal.Grid.Cols != 4 || cal.Grid.Rows != 3 {
		t.Errorf("flow sidecar grid = %+v, want 4x3", cal.Grid)
	}
	if !strings.Contains(string(out), "body") {
		t.Error("body lost")
	}
}

// CRLF sidecars stay CRLF (Windows-safe surgical edits).
func TestApplyImportCRLFPreserved(t *testing.T) {
	v, dir := testVault(t)
	ctx := context.Background()
	crlf := "---\r\ntitle: CRLF\r\nvtt-map:\r\n  grid: {cols: 2, rows: 2}\r\n---\r\n\r\nbody\r\n"
	if err := os.WriteFile(filepath.Join(dir, "maps", "arena.md"), []byte(crlf), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ := Parse([]byte(uvttBasic(t, "")), "crlf.uvtt")
	if _, err := ApplyImport(ctx, ImportRequest{Vault: v, MapID: "arena", Map: m}); err != nil {
		t.Fatalf("ApplyImport: %v", err)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "maps", "arena.md"))
	if !strings.Contains(string(out), "\r\n") {
		t.Error("CRLF endings lost")
	}
	if strings.Contains(strings.TrimSuffix(string(out), "\r\n"), "\n") && !strings.Contains(string(out), "\r\n") {
		t.Error("mixed endings")
	}
	page, err := markdown.Parse(ctx, string(out), "maps/arena.md")
	if err != nil || page.Quarantined {
		t.Fatalf("crlf sidecar parse: %v %v", err, page)
	}
	cal := ParseCalibration(page.Frontmatter["vtt-map"])
	if cal.Grid.Cols != 4 || len(cal.Walls) != 4 {
		t.Errorf("crlf sidecar calibration = %+v", cal)
	}
}

// The HTTP image cap (5MB) rejects big embedded art with a clear reason;
// the CLI (cap 0) is uncapped (D1).
func TestApplyImportImageCap(t *testing.T) {
	v, _ := testVault(t)
	ctx := context.Background()
	// A 6MB PNG payload: the 5MB HTTP cap rejects it before any decode; the
	// CLI path (cap 0) proceeds and fails on the undecodable bytes instead.
	big := make([]byte, 6<<20)
	copy(big, pngMagic)
	m := &UVTTMap{Cols: 4, Rows: 3, Cell: 100, Source: "big.uvtt",
		Image: &UVTTImage{Format: "png", Width: 9600, Height: 7200, Data: big}}
	if _, err := ApplyImport(ctx, ImportRequest{Vault: v, MapID: "arena", Map: m, ImageByteCap: 5 << 20}); err == nil {
		t.Error("oversize image accepted on the capped (HTTP) path")
	} else if !strings.Contains(err.Error(), "cap") {
		t.Errorf("capped-path error should name the cap: %v", err)
	}
	if _, err := ApplyImport(ctx, ImportRequest{Vault: v, MapID: "arena", Map: m}); err == nil {
		t.Error("uncapped path accepted an unparseable image")
	}
}

// Grid-baked posture: body field, then the opts override (CLI flag wins).
func TestParseWithOptsGridBaked(t *testing.T) {
	payload := uvttBasic(t, "")
	m, err := ParseWithOpts([]byte(payload), "b.uvtt", ImportOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if m.GridBaked {
		t.Error("default grid-baked must be false")
	}
	// JSON body field.
	withField := strings.Replace(payload, `"format": 1.0`, `"format": 1.0, "grid_baked": true`, 1)
	m, err = ParseWithOpts([]byte(withField), "b.uvtt", ImportOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !m.GridBaked {
		t.Error("body grid_baked ignored")
	}
	// CLI flag overrides the body field.
	no := false
	m, err = ParseWithOpts([]byte(withField), "b.uvtt", ImportOpts{GridBaked: &no})
	if err != nil {
		t.Fatal(err)
	}
	if m.GridBaked {
		t.Error("opts override ignored")
	}
	// Parse (no opts) stays the plain default.
	m, _ = Parse([]byte(payload), "b.uvtt")
	if m.GridBaked {
		t.Error("Parse defaults to unbaked")
	}
}

// Full-blocker posture (R6): the importer writes objects-block-sight on
// every import (mirroring grid-baked), the REAL frontmatter parser reads it
// back, and a re-import with the opposite posture flips the owned key
// without touching hand edits (idempotent, no duplication).
func TestApplyImportObjectsBlockSight(t *testing.T) {
	v, dir := testVault(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(dir, "maps", "arena.md"), []byte(existingSidecar), 0o644); err != nil {
		t.Fatal(err)
	}
	yes := true
	m, err := ParseWithOpts([]byte(uvttBasic(t, tinyPNG)), "ford.dd2vtt", ImportOpts{ObjectsBlockSight: &yes})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	res, err := ApplyImport(ctx, ImportRequest{Vault: v, MapID: "arena", Map: m})
	if err != nil {
		t.Fatalf("ApplyImport: %v", err)
	}
	if !res.ObjectsBlockSight {
		t.Errorf("result ObjectsBlockSight = false, want true")
	}
	out, err := os.ReadFile(filepath.Join(dir, "maps", "arena.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); !strings.Contains(got, "objects-block-sight: true") {
		t.Errorf("sidecar missing objects-block-sight: true:\n%s", got)
	}
	page, err := markdown.Parse(ctx, string(out), "maps/arena.md")
	if err != nil || page.Quarantined {
		t.Fatalf("reparse: %v %v", err, page)
	}
	if cal := ParseCalibration(page.Frontmatter["vtt-map"]); !cal.ObjectsBlockSight {
		t.Errorf("round-trip ObjectsBlockSight = false, want true")
	}
	// Re-import with the default posture: the owned key flips to false
	// (exactly once), hand edits still survive.
	m2, err := Parse([]byte(uvttBasic(t, tinyPNG)), "ford.dd2vtt")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := ApplyImport(ctx, ImportRequest{Vault: v, MapID: "arena", Map: m2}); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	out2, err := os.ReadFile(filepath.Join(dir, "maps", "arena.md"))
	if err != nil {
		t.Fatal(err)
	}
	got2 := string(out2)
	if strings.Contains(got2, "objects-block-sight: true") {
		t.Errorf("stale owned key survived the re-import:\n%s", got2)
	}
	if n := strings.Count(got2, "objects-block-sight:"); n != 1 {
		t.Errorf("objects-block-sight lines = %d, want 1 (no duplication):\n%s", n, got2)
	}
	for _, want := range []string{"mira-pc", "fog-default: revealed", "objects-block-sight: false"} {
		if !strings.Contains(got2, want) {
			t.Errorf("re-import lost %q:\n%s", want, got2)
		}
	}
	page2, err := markdown.Parse(ctx, got2, "maps/arena.md")
	if err != nil || page2.Quarantined {
		t.Fatalf("re-import reparse: %v %v", err, page2)
	}
	if cal := ParseCalibration(page2.Frontmatter["vtt-map"]); cal.ObjectsBlockSight {
		t.Errorf("re-import round-trip ObjectsBlockSight = true, want false")
	}
}
