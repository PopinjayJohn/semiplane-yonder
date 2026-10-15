package vtt

import (
	"testing"

	"github.com/semiplane/yonder/internal/uploads"
)

// EffectivePPGOf projects calibration onto the stored-art posture without
// I/O: png/jpeg art is capped at uploads.DisplayMaxDim on the longest edge,
// webp is stored as-is (D2), and anything unknowable fails closed to "not
// downscaled" (no GM warning).
func TestEffectivePPGOf(t *testing.T) {
	cap := float64(uploads.DisplayMaxDim)
	cases := []struct {
		name       string
		cal        Calibration
		downscaled bool
		scale      float64
		srcW       float64
	}{
		// Tomb demo shape: 96x72 @100ppg jpg → 9600x7200 source, capped.
		{name: "tomb jpg downscaled",
			cal:        Calibration{Background: "assets/maps/tomb.jpg", Grid: Grid{Cols: 96, Rows: 72}, PPG: 100},
			downscaled: true, scale: cap / 9600, srcW: 9600},
		// Ground-Floor demo shape: 20x20 @150ppg jpg → 3000x3000, capped.
		{name: "ground floor jpg downscaled",
			cal:        Calibration{Background: "assets/maps/ground-floor.jpg", Grid: Grid{Cols: 20, Rows: 20}, PPG: 150},
			downscaled: true, scale: cap / 3000, srcW: 3000},
		// webp rides through undecoded: never downscaled, scale stays 1.
		{name: "webp passthrough",
			cal:        Calibration{Background: "assets/maps/big.webp", Grid: Grid{Cols: 96, Rows: 72}, PPG: 100},
			downscaled: false, scale: 1, srcW: 9600},
		// Small png under the cap: stored as-is.
		{name: "small png intact",
			cal:        Calibration{Background: "assets/maps/arena.png", Grid: Grid{Cols: 20, Rows: 14}, PPG: 50},
			downscaled: false, scale: 1, srcW: 1000},
		// Fail-closed shapes: no warning without a knowable downscale.
		{name: "no background",
			cal:        Calibration{Grid: Grid{Cols: 96, Rows: 72}, PPG: 100},
			downscaled: false, scale: 1},
		{name: "no ppg",
			cal:        Calibration{Background: "assets/maps/tomb.jpg", Grid: Grid{Cols: 96, Rows: 72}},
			downscaled: false, scale: 1},
		{name: "unknown extension",
			cal:        Calibration{Background: "assets/maps/big.tiff", Grid: Grid{Cols: 96, Rows: 72}, PPG: 100},
			downscaled: false, scale: 1},
		{name: "uppercase jpg still capped",
			cal:        Calibration{Background: "assets/maps/TOMB.JPG", Grid: Grid{Cols: 96, Rows: 72}, PPG: 100},
			downscaled: true, scale: cap / 9600, srcW: 9600},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EffectivePPGOf(tc.cal)
			if got.Downscaled != tc.downscaled {
				t.Errorf("Downscaled = %v, want %v", got.Downscaled, tc.downscaled)
			}
			if got.Scale != tc.scale {
				t.Errorf("Scale = %v, want %v", got.Scale, tc.scale)
			}
			if tc.srcW != 0 && got.SrcW != tc.srcW {
				t.Errorf("SrcW = %v, want %v", got.SrcW, tc.srcW)
			}
			if got.Scale <= 0 || got.Scale > 1 {
				t.Errorf("Scale = %v out of (0,1]", got.Scale)
			}
		})
	}
}
