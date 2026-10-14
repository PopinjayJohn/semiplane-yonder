// import-uvtt (amend A-VTT2 §4, D1): the offline UVTT import surface.
//
//	yonder import-uvtt --vault DIR --map ID [--background FILE]
//	    [--grid-baked|--no-grid-baked] <file.uvtt|dd2vtt|df2vtt>
//
// Same parser + sidecar writer as POST /vtt/{mapID}/import, but with NO
// HTTP cap (D1: "larger files via CLI"): the 12MB body cap and the 5MB
// image cap do not apply here — the parser's 96MB decoded cap and the
// 128MP dims cap still do. Writes the map image to assets/maps/<id>.<ext>
// and merges the vtt-map calibration into maps/<id>.md surgically
// (comments/order preserved; hand-edited sub-keys such as `tokens` kept).
//
// Live VTT state (tokens/fog/doors) is deliberately NOT touched: it is
// ephemeral app-DB state, and the table's "Reset to sidecar" reseeds it
// from the calibration this command writes. Run `yonder reindex` after
// importing outside a serve process (serve's watcher picks the change up
// on its own).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/semiplane/yonder/internal/vault"
	"github.com/semiplane/yonder/internal/vtt"
)

func runImportUVTTCommand(vaultPath string, args []string) error {
	var (
		mapID       string
		background  string
		gridBaked   bool
		noGridBaked bool
	)
	fs := flag.NewFlagSet("import-uvtt", flag.ContinueOnError)
	vaultFlag(fs, &vaultPath)
	fs.StringVar(&mapID, "map", "", "Map id (sidecar maps/<id>.md; asset assets/maps/<id>.<ext>)")
	fs.StringVar(&background, "background", "", "External background image file (default: the UVTT embedded image)")
	fs.BoolVar(&gridBaked, "grid-baked", false, "The background art carries its own grid (renderer skips the overlay)")
	fs.BoolVar(&noGridBaked, "no-grid-baked", false, "Force the overlay grid (the default)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if mapID == "" {
		return fmt.Errorf("usage: import-uvtt --vault DIR --map ID [--background FILE] [--grid-baked] <file.uvtt|dd2vtt|df2vtt>")
	}
	if gridBaked && noGridBaked {
		return fmt.Errorf("--grid-baked and --no-grid-baked are mutually exclusive")
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: import-uvtt --vault DIR --map ID [--background FILE] [--grid-baked] <file.uvtt|dd2vtt|df2vtt>")
	}
	if vaultPath == "" {
		return fmt.Errorf("vault path required (--vault)")
	}
	if !vtt.ValidMapID(mapID) {
		return fmt.Errorf("bad map id %q (1-64 chars of letters/digits/dash/underscore)", mapID)
	}
	baked := gridBaked && !noGridBaked

	data, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("read %s: %w", fs.Arg(0), err)
	}
	m, err := vtt.ParseWithOpts(data, filepath.Base(fs.Arg(0)), vtt.ImportOpts{GridBaked: &baked})
	if err != nil {
		return fmt.Errorf("parse %s: %w", fs.Arg(0), err)
	}
	v, err := vault.NewVault(vaultPath)
	if err != nil {
		return fmt.Errorf("open vault %s: %w", vaultPath, err)
	}
	req := vtt.ImportRequest{Vault: v, MapID: mapID, Map: m}
	if background != "" {
		bg, err := os.ReadFile(background)
		if err != nil {
			return fmt.Errorf("read background %s: %w", background, err)
		}
		format := sniffImportFormat(bg)
		if format == "" {
			return fmt.Errorf("background %s: unknown image magic (want png/webp/jpeg)", background)
		}
		req.Background, req.BackgroundExt = bg, format
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := vtt.ApplyImport(ctx, req)
	if err != nil {
		return err
	}
	printImportSummary(mapID, res)
	return nil
}

// sniffImportFormat magic-sniffs external background bytes (never the
// extension — the same rule as every other upload path).
func sniffImportFormat(data []byte) string {
	switch {
	case len(data) >= 8 && string(data[:4]) == "\x89PNG":
		return "png"
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "webp"
	case len(data) >= 2 && data[0] == 0xFF && data[1] == 0xD8:
		return "jpeg"
	}
	return ""
}

func printImportSummary(mapID string, res *vtt.ImportResult) {
	asset := res.AssetPath
	if asset == "" {
		asset = "(none — add background: assets/maps/" + mapID + ".<ext> when you have art)"
	}
	fmt.Printf("imported %s -> %s\n", res.Source, res.SidecarPath)
	fmt.Printf("  grid %dx%d (cell %dpx), grid-baked %v, ambient %q, baked-lighting %v\n",
		res.Cols, res.Rows, res.Cell, res.GridBaked, res.Ambient, res.BakedLight)
	fmt.Printf("  walls %d, blocking-objects %d, portals %d, lights %d\n",
		res.Walls, res.Objects, res.Portals, res.Lights)
	fmt.Printf("  background %s\n", asset)
	if !strings.Contains(res.SidecarPath, "/") {
		fmt.Printf("  map id %s opens at /vtt/%s\n", mapID, mapID)
	}
}
