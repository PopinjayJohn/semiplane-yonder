package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// refreshSwapParser is a minimal index Parser for refresh tests.
func refreshSwapParser(title string) Parser {
	return func(ctx context.Context, content []byte, path string) (*ParsedPage, error) {
		return &ParsedPage{Title: title, FrontmatterJSON: `{"title":"` + title + `"}`,
			Chunks: []ParsedBlock{{Text: "# " + title}}}, nil
	}
}

// TestRefreshIndexDoesNotDropReaders pins the swap discipline: a reader
// must never observe a half-swapped pool. The shipped failure mode was
// RefreshIndex closing the old pool before reopening, so any request
// reading the index inside that window got "database is closed" — a
// PageGet error reads as "no such map", which surfaced as a flaky 404 on
// freshly imported maps (TestImportGateCreatesMapOverHTTP).
func TestRefreshIndexDoesNotDropReaders(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "a.md"), []byte("---\ntitle: A\n---\n\n# A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := filepath.Join(dir, "t.index.db")
	s, err := Open(idx, filepath.Join(dir, "t.app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := Reindex(context.Background(), vault, idx, refreshSwapParser("A")); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshIndex(context.Background()); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	errs := 0
	first := ""
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := s.PageGet(context.Background(), "a.md"); err != nil {
					mu.Lock()
					errs++
					if first == "" {
						first = err.Error()
					}
					mu.Unlock()
				}
				time.Sleep(50 * time.Microsecond)
			}
		}()
	}
	// Reindex + refresh repeatedly: every swap is a chance to drop a reader.
	for i := 0; i < 40; i++ {
		if err := Reindex(context.Background(), vault, idx, refreshSwapParser("A")); err != nil {
			close(stop)
			t.Fatalf("Reindex: %v", err)
		}
		if err := s.RefreshIndex(context.Background()); err != nil {
			close(stop)
			t.Fatalf("RefreshIndex: %v", err)
		}
	}
	close(stop)
	wg.Wait()
	if errs > 0 {
		t.Fatalf("%d reads failed across 40 index swaps (first: %s)", errs, first)
	}
	// The swapped pool serves the rebuilt index (not a stale snapshot).
	if _, err := s.PageGet(context.Background(), "a.md"); err != nil {
		t.Fatalf("post-swap read: %v", err)
	}
}
