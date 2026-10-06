package bench

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// TestBench5k runs the P04 benchmark (cold rebuild + warm rescan + filtered
// player search) and logs the numbers. Timings are recorded, not promised
// (P04): the test fails only on wrongness (leaks, missing rows), never on
// speed. Set BENCH_FILES to shrink the vault locally; BENCH_RECORD=1 writes
// internal/bench/results.json via `make bench`.
func TestBench5k(t *testing.T) {
	num := DefaultNumFiles
	if v := os.Getenv("BENCH_FILES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 10 {
			t.Fatalf("bad BENCH_FILES=%q: want int >= 10", v)
		}
		num = n
	}

	res, err := Run(context.Background(), filepath.Join(t.TempDir(), "bench.index.db"), num)
	if err != nil {
		t.Fatalf("bench run: %v", err)
	}
	t.Logf("bench %d files (%s/%s, %d cpu, db %.1fMB):",
		res.NumFiles, res.GOOS, res.GOARCH, res.NumCPU, float64(res.DBBytes)/1e6)
	for _, p := range res.Phases {
		t.Logf("  %-12s %6dms  %s", p.Name, p.DurationMs, p.Detail)
	}
	t.Logf("  %-12s %6dms", "total", res.TotalMs())

	if os.Getenv("BENCH_RECORD") == "1" {
		_, thisFile, _, ok := runtime.Caller(0)
		if !ok {
			t.Fatal("runtime.Caller failed")
		}
		out := filepath.Join(filepath.Dir(thisFile), "results.json")
		if err := WriteJSON(out, res); err != nil {
			t.Fatalf("record results: %v", err)
		}
		t.Logf("recorded %s", out)
	}
}
