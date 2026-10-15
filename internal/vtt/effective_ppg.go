package vtt

import (
	"strings"

	"github.com/semiplane/yonder/internal/uploads"
)

// EffectivePPG projects a map calibration onto its stored-art resolution
// posture (R2 design: docs/plan/r2-effective-ppg.md).
//
// The import pipeline stores png/jpeg art downscaled to
// uploads.DisplayMaxDim on the longest edge (processImportImage via
// uploads.Normalize) and stores webp as-is (D2). Geometry (walls, objects,
// doors, lights) lives in grid squares end-to-end, so the downscale never
// misaligns overlays — both renderers normalize grid squares against the
// same stage the background stretches to. This projection exists only to
// decide whether the GM gets the downscale warning in the fragment.
//
// It is pure and I/O-free: the cap rule is deterministic, so no image
// decode or vault read is needed. Hand-swapped art may differ from the
// projection — the warning text says "capped", never measured dims.
type EffectivePPG struct {
	// SrcW/SrcH are the source-art dims in px (cols*ppg, rows*ppg).
	SrcW, SrcH float64
	// Scale is the float art-scale factor (1 = stored as-is).
	Scale float64
	// Downscaled reports the stored art is (or would be, on re-import)
	// downscaled from source. Only then does the fragment warn the GM.
	Downscaled bool
}

// EffectivePPGOf derives the stored-art posture from calibration alone.
// Fail-closed to "not downscaled" (no warning) when ppg is absent, the
// grid is degenerate, there is no background, or the extension is unknown
// or webp (stored as-is, never downscaled at import).
func EffectivePPGOf(cal Calibration) EffectivePPG {
	out := EffectivePPG{Scale: 1}
	if cal.PPG <= 0 || !finite(cal.PPG) {
		return out
	}
	if cal.Grid.Cols < 1 || cal.Grid.Rows < 1 {
		return out
	}
	bg := strings.ToLower(strings.TrimSpace(cal.Background))
	if bg == "" {
		return out // art-less board: nothing to downscale
	}
	switch {
	case strings.HasSuffix(bg, ".webp"):
		out.SrcW, out.SrcH = float64(cal.Grid.Cols)*cal.PPG, float64(cal.Grid.Rows)*cal.PPG
		return out // stored as-is per D2: never downscaled
	case strings.HasSuffix(bg, ".png"),
		strings.HasSuffix(bg, ".jpg"),
		strings.HasSuffix(bg, ".jpeg"):
		// Cap rule mirrors processImportImage: longest stored edge is
		// uploads.DisplayMaxDim.
	default:
		return out // unknown extension: fail closed, no warning
	}
	out.SrcW, out.SrcH = float64(cal.Grid.Cols)*cal.PPG, float64(cal.Grid.Rows)*cal.PPG
	longest := out.SrcW
	if out.SrcH > longest {
		longest = out.SrcH
	}
	cap := float64(uploads.DisplayMaxDim)
	if longest <= cap {
		return out
	}
	out.Scale = cap / longest
	out.Downscaled = true
	return out
}
