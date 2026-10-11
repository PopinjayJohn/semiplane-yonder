package vault

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteFileBytesRoundTrip pins the B6 binary amend: arbitrary bytes
// (including invalid UTF-8) store atomically and read back exactly.
func TestWriteFileBytesRoundTrip(t *testing.T) {
	ctx := context.Background()
	v := testVaultRoot(t)
	data := []byte{0x89, 'P', 'N', 'G', 0x00, 0xff, 0xfe, '\n'}
	if err := v.WriteFileBytes(ctx, "notes/pic.png", data); err != nil {
		t.Fatal(err)
	}
	got, err := v.ReadFile(ctx, "notes/pic.png")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("read back %x, want %x", got, data)
	}
	if st, err := os.Stat(filepath.Join(v.Root, "notes", "pic.png")); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("stored mode = %v, %v; want 0644", st.Mode(), err)
	}
}

// TestWriteFileBytesNoClashMemory pins ruling option B: overwriting with
// different bytes is last-writer-wins — no error, no *.conflict-* parking
// (binary conflicts are unresolvable in-app; the caller owns 409 policy).
func TestWriteFileBytesNoClashMemory(t *testing.T) {
	ctx := context.Background()
	v := testVaultRoot(t)
	if err := v.WriteFileBytes(ctx, "a.png", []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.ReadFile(ctx, "a.png"); err != nil {
		t.Fatal(err)
	}
	// External edit, then overwrite: text WriteFile would park a conflict.
	if err := os.WriteFile(filepath.Join(v.Root, "a.png"), []byte{9, 9}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := v.WriteFileBytes(ctx, "a.png", []byte{4, 5, 6}); err != nil {
		t.Fatalf("bytes overwrite must not conflict: %v", err)
	}
	got, err := v.ReadFile(ctx, "a.png")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{4, 5, 6}) {
		t.Fatalf("stored %v, want last write", got)
	}
	var parked []string
	_ = filepath.WalkDir(v.Root, func(fp string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.Contains(d.Name(), ".conflict-") {
			rel, _ := filepath.Rel(v.Root, fp)
			parked = append(parked, rel)
		}
		return nil
	})
	if len(parked) != 0 {
		t.Fatalf("conflict files parked for bytes write: %v", parked)
	}
}

// TestWriteFileBytesGuards pins fail-closed edges: directory targets and
// cancelled contexts refuse without writing.
func TestWriteFileBytesGuards(t *testing.T) {
	ctx := context.Background()
	v := testVaultRoot(t)
	if err := os.MkdirAll(filepath.Join(v.Root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := v.WriteFileBytes(ctx, "sub", []byte{1}); err == nil {
		t.Errorf("bytes write to a directory must fail")
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if err := v.WriteFileBytes(cancelled, "x.png", []byte{1}); err == nil {
		t.Errorf("cancelled bytes write must fail")
	}
	if err := v.WriteFileBytes(ctx, "../esc.png", []byte{1}); err == nil {
		t.Errorf("escaping bytes write must fail")
	}
}
