package vault

import (
	"context"
	"io/fs"
)

// Vault provides access to the vault filesystem.
// Vault filesystem = truth; SQLite index is disposable (rebuilt via reindex).
// All vault writes go through WriteFile (atomic: re-read + temp + rename + mtime check).
// Parsers never rewrite source; writes preserve comments/order.
// Windows-safe paths (separators, case, reserved names, long paths).
type Vault struct {
	Root string
}

// NewVault creates a new vault instance.
func NewVault(root string) (*Vault, error) {
	return nil, nil // not implemented
}

// ReadFile reads a file from the vault.
// Path is vault-relative posix; lookup is case-insensitive, errors and
// events report the display (source-case) path.
func (v *Vault) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return nil, nil // not implemented
}

// WriteFile atomically writes a file to the vault.
// Atomic: re-read + temp + rename + mtime check.
// Preserves comments/order in frontmatter.
// Windows-safe paths.
func (v *Vault) WriteFile(ctx context.Context, path, content string) error {
	return nil // not implemented
}

// DeleteFile deletes a file from the vault.
func (v *Vault) DeleteFile(ctx context.Context, path string) error {
	return nil // not implemented
}

// RenameFile renames a file in the vault.
// Phase 0c: rename = delete + create with ACL preservation (owner and
// grants move to the new path; no merge, no widening).
func (v *Vault) RenameFile(ctx context.Context, oldPath, newPath string) error {
	return nil // not implemented
}

// ListFiles lists files in the vault matching a pattern.
func (v *Vault) ListFiles(ctx context.Context, pattern string) ([]string, error) {
	return nil, nil // not implemented
}

// Walk walks the vault filesystem.
func (v *Vault) Walk(ctx context.Context, fn fs.WalkDirFunc) error {
	return nil // not implemented
}

// Watch watches the vault for changes (fsnotify + debounce + self-write tolerance).
func (v *Vault) Watch(ctx context.Context, fn func(events []Event)) error {
	return nil // not implemented
}

// Event represents a vault filesystem event.
type Event struct {
	Path string
	Op   EventOp
	Time int64
	Self bool // true if triggered by our own WriteFile
}

// EventOp represents a filesystem operation.
type EventOp int

const (
	EventCreate EventOp = iota
	EventWrite
	EventDelete
	EventRename
	EventChmod
)

// ConflictFile represents a conflict file (index with parent ACL, hidden from search).
type ConflictFile struct {
	Path       string
	ParentPath string
	Content    []byte
	CreatedAt  int64
	Resolver   string // username who created the conflict
}

// ReadConflict reads a conflict file.
func (v *Vault) ReadConflict(ctx context.Context, path string) (*ConflictFile, error) {
	return nil, nil // not implemented
}

// ResolveConflict resolves a conflict (manual merge, never auto-merge).
func (v *Vault) ResolveConflict(ctx context.Context, path, resolvedContent string) error {
	return nil // not implemented
}
