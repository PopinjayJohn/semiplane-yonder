package vault

import (
	"context"
	"os"
	"strings"
	"time"
)

// ConflictFile represents a conflict file (index with parent ACL, hidden from search).
type ConflictFile struct {
	Path       string
	ParentPath string
	Content    []byte
	CreatedAt  int64
	Resolver   string // username who created the conflict
}

// conflictParentOf recovers the parent page from a conflict path:
// "notes/a.conflict-123.md" → "notes/a.md".
func conflictParentOf(rel string) string {
	i := strings.LastIndex(rel, ".conflict-")
	if i < 0 || !strings.HasSuffix(rel, ".md") {
		return ""
	}
	return rel[:i] + ".md"
}

// ReadConflict reads a conflict file.
func (v *Vault) ReadConflict(ctx context.Context, path string) (*ConflictFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rel, err := CleanRel(path)
	if err != nil {
		return nil, err
	}
	parent := conflictParentOf(rel)
	if parent == "" {
		return nil, &ErrConflict{ConflictPath: rel, ParentPath: rel}
	}
	rel = v.resolveExisting(rel)
	full := v.abs(rel)
	content, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	var created int64
	if st, serr := os.Stat(full); serr == nil {
		created = st.ModTime().UnixNano()
	}
	// Resolver identity is not stored in the filesystem; the app layer
	// records it in edits_log when it creates the conflict. Empty here.
	return &ConflictFile{Path: rel, ParentPath: parent, Content: content, CreatedAt: created}, nil
}

// ResolveConflict resolves a conflict (manual merge, never auto-merge): the
// caller supplies the merged text, which is written atomically to the parent
// page; the conflict file is then deleted. The parent write goes through the
// normal clash check, so a concurrent save still protects the page.
func (v *Vault) ResolveConflict(ctx context.Context, path, resolvedContent string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rel, err := CleanRel(path)
	if err != nil {
		return err
	}
	parent := conflictParentOf(rel)
	if parent == "" {
		return &ErrConflict{ConflictPath: rel, ParentPath: rel}
	}
	rel = v.resolveExisting(rel)
	cf, err := v.ReadConflict(ctx, rel)
	if err != nil {
		return err
	}
	_ = cf
	// Forced write: the human just merged by hand, so the normal clash check
	// is bypassed deliberately (this is the explicit override path, not a
	// silent clobber). Still atomic (temp + rename).
	if err := v.atomicWrite(parent, resolvedContent); err != nil {
		return err
	}
	if st, serr := os.Stat(v.abs(parent)); serr == nil {
		v.mu.Lock()
		v.known[fold(parent)] = fileState{mtime: st.ModTime().UnixNano(), hash: hashBytes([]byte(resolvedContent))}
		v.self[fold(parent)] = time.Now().Add(selfWriteWindow)
		v.mu.Unlock()
	}
	if err := os.Remove(v.abs(rel)); err != nil && !os.IsNotExist(err) {
		return err
	}
	v.mu.Lock()
	delete(v.known, fold(rel))
	v.mu.Unlock()
	return nil
}
