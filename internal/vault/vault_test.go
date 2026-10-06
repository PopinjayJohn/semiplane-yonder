package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testVaultRoot(t *testing.T) *Vault {
	t.Helper()
	v, err := NewVault(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestWriteReadRoundTrip(t *testing.T) {
	ctx := context.Background()
	v := testVaultRoot(t)
	if err := v.WriteFile(ctx, "notes/hello.md", "# Hi\n\nbody\n"); err != nil {
		t.Fatal(err)
	}
	got, err := v.ReadFile(ctx, "notes/hello.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# Hi\n\nbody\n" {
		t.Fatalf("read back %q", got)
	}
	// Case-insensitive lookup.
	got, err = v.ReadFile(ctx, "NOTES/HELLO.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# Hi\n\nbody\n" {
		t.Fatalf("case-insensitive read %q", got)
	}
}

func TestWriteConflictOnExternalEdit(t *testing.T) {
	ctx := context.Background()
	v := testVaultRoot(t)
	if err := v.WriteFile(ctx, "a.md", "v1\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.ReadFile(ctx, "a.md"); err != nil {
		t.Fatal(err)
	}
	// External editor (another process) saves without going through WriteFile.
	if err := os.WriteFile(filepath.Join(v.Root, "a.md"), []byte("v2-external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := v.WriteFile(ctx, "a.md", "v3-stale\n")
	var ce *ErrConflict
	if !errors.As(err, &ce) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if !strings.Contains(ce.ConflictPath, ".conflict-") || !strings.HasSuffix(ce.ConflictPath, ".md") {
		t.Fatalf("bad conflict pattern: %q", ce.ConflictPath)
	}
	// Original untouched; incoming parked in the conflict file.
	orig, _ := v.ReadFile(ctx, "a.md")
	if string(orig) != "v2-external\n" {
		t.Fatalf("original clobbered: %q", orig)
	}
	parked, err := v.ReadConflict(ctx, ce.ConflictPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(parked.Content) != "v3-stale\n" || parked.ParentPath != "a.md" {
		t.Fatalf("conflict = %+v", parked)
	}
}

func TestResolveConflict(t *testing.T) {
	ctx := context.Background()
	v := testVaultRoot(t)
	if err := v.WriteFile(ctx, "a.md", "v1\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.ReadFile(ctx, "a.md"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v.Root, "a.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var ce *ErrConflict
	if err := v.WriteFile(ctx, "a.md", "v3\n"); !errors.As(err, &ce) {
		t.Fatalf("want conflict, got %v", err)
	}
	// Manual merge: caller supplies the text (never auto-merged).
	if err := v.ResolveConflict(ctx, ce.ConflictPath, "merged\n"); err != nil {
		t.Fatal(err)
	}
	got, err := v.ReadFile(ctx, "a.md")
	if err != nil || string(got) != "merged\n" {
		t.Fatalf("parent = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(v.Root, filepath.FromSlash(ce.ConflictPath))); !os.IsNotExist(err) {
		t.Fatal("conflict file should be deleted after resolve")
	}
}

func TestCleanRelRejects(t *testing.T) {
	for _, bad := range []string{"", "/abs.md", "../esc.md", "a/../../esc.md", "CON.md", "a/AUX", "a:b.md", "nick?.md", "trail. ", "nul.txt"} {
		if _, err := CleanRel(bad); err == nil {
			t.Fatalf("CleanRel(%q) accepted", bad)
		}
	}
	for _, ok := range []string{"a.md", "notes/My Page.md", "a.b/c-d_e.md"} {
		if _, err := CleanRel(ok); err != nil {
			t.Fatalf("CleanRel(%q) rejected: %v", ok, err)
		}
	}
}

func TestRenameMovesContent(t *testing.T) {
	ctx := context.Background()
	v := testVaultRoot(t)
	if err := v.WriteFile(ctx, "old.md", "data\n"); err != nil {
		t.Fatal(err)
	}
	if err := v.RenameFile(ctx, "old.md", "sub/new.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(v.Root, "old.md")); !os.IsNotExist(err) {
		t.Fatal("old path should be gone")
	}
	got, err := v.ReadFile(ctx, "sub/new.md")
	if err != nil || string(got) != "data\n" {
		t.Fatalf("new = %q, %v", got, err)
	}
}

func TestListFilesSkipsConflicts(t *testing.T) {
	ctx := context.Background()
	v := testVaultRoot(t)
	for _, f := range []string{"a.md", "b.md", "a.conflict-1.md"} {
		if err := os.WriteFile(filepath.Join(v.Root, f), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := v.ListFiles(ctx, "*.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ListFiles = %v (conflict must be hidden)", got)
	}
}

func TestWatchDebounceAndSelf(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v := testVaultRoot(t)
	if err := v.WriteFile(ctx, "w.md", "v1\n"); err != nil {
		t.Fatal(err)
	}
	old := DebounceInterval
	DebounceInterval = 50 * time.Millisecond
	defer func() { DebounceInterval = old }()

	got := make(chan []Event, 4)
	werr := make(chan error, 1)
	go func() { werr <- v.Watch(ctx, func(ev []Event) { got <- ev }) }()

	// Storm of rapid saves collapses to one callback bearing Self.
	time.Sleep(100 * time.Millisecond) // let the watcher arm
	for i := 0; i < 5; i++ {
		if err := v.WriteFile(ctx, "w.md", "v1\n"); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case evs := <-got:
		if len(evs) == 0 {
			t.Fatal("empty event batch")
		}
		found := false
		for _, e := range evs {
			if e.Path == "w.md" && e.Self {
				found = true
			}
		}
		if !found {
			t.Fatalf("self write not flagged: %+v", evs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no watch events (self-write tolerance dropped our own save?)")
	}
	cancel()
	<-werr
}
