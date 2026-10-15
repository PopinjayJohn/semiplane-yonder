// Import admission guard tests (R1): window budget, expiry, per-user
// isolation, in-flight semaphore, and the production defaults.
package vtt

import (
	"errors"
	"testing"
	"time"
)

func TestImportLimiterBudget(t *testing.T) {
	l := NewImportLimiter(1, 3, 5*time.Minute)
	for i := 0; i < 3; i++ {
		if err := l.Acquire("gm"); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		l.Release()
	}
	// Window budget is count-based: releasing the in-flight slot does not
	// refund the window.
	if err := l.Acquire("gm"); !errors.Is(err, ErrImportRateLimited) {
		t.Fatalf("4th acquire = %v, want ErrImportRateLimited", err)
	}
}

func TestImportLimiterWindowExpiry(t *testing.T) {
	now := time.Now()
	l := NewImportLimiter(1, 1, 5*time.Minute)
	l.now = func() time.Time { return now }
	if err := l.Acquire("gm"); err != nil {
		t.Fatal(err)
	}
	l.Release()
	if err := l.Acquire("gm"); !errors.Is(err, ErrImportRateLimited) {
		t.Fatalf("within window = %v, want ErrImportRateLimited", err)
	}
	now = now.Add(5*time.Minute + time.Second)
	if err := l.Acquire("gm"); err != nil {
		t.Fatalf("past window: %v", err)
	}
	l.Release()
}

func TestImportLimiterPerUserIsolation(t *testing.T) {
	l := NewImportLimiter(2, 1, 5*time.Minute)
	if err := l.Acquire("gm"); err != nil {
		t.Fatal(err)
	}
	l.Release()
	if err := l.Acquire("gm"); !errors.Is(err, ErrImportRateLimited) {
		t.Fatalf("gm again = %v, want ErrImportRateLimited", err)
	}
	// Another GM identity (or a second account) keeps its own budget.
	if err := l.Acquire("gm2"); err != nil {
		t.Fatalf("other user: %v", err)
	}
	l.Release()
}

func TestImportLimiterInFlight(t *testing.T) {
	l := NewImportLimiter(1, 10, 5*time.Minute)
	if err := l.Acquire("gm"); err != nil {
		t.Fatal(err)
	}
	// Budget remains, but the single in-flight slot is held — even by a
	// different user, since stacked imports serialize full rescans.
	if err := l.Acquire("gm2"); !errors.Is(err, ErrImportBusy) {
		t.Fatalf("concurrent acquire = %v, want ErrImportBusy", err)
	}
	l.Release()
	if err := l.Acquire("gm2"); err != nil {
		t.Fatalf("after release: %v", err)
	}
	l.Release()
	// Extra releases never drive the count negative (the next acquire
	// must not report busy).
	l.Release()
	if err := l.Acquire("gm"); err != nil {
		t.Fatalf("after extra release: %v", err)
	}
	l.Release()
}

func TestImportLimiterDefaults(t *testing.T) {
	l := DefaultImportLimiter()
	if l.maxInFlight != 1 || l.maxPerWindow != 3 || l.window != 5*time.Minute {
		t.Fatalf("defaults = %d in flight, %d per %v",
			l.maxInFlight, l.maxPerWindow, l.window)
	}
}
