package vault

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Vault provides access to the vault filesystem.
// Vault filesystem = truth; SQLite index is disposable (rebuilt via reindex).
// All vault writes go through WriteFile (atomic: re-read + temp + rename + mtime check).
// Parsers never rewrite source; writes preserve comments/order.
// Windows-safe paths (separators, case, reserved names, long paths).
type Vault struct {
	Root string

	mu    sync.Mutex
	known map[string]fileState // fold(path) → state at last Read/Write
	self  map[string]time.Time // fold(path) → self-write suppression deadline
}

// fileState records what this process last saw for optimistic clash compare.
type fileState struct {
	mtime int64 // unix nanos
	hash  string
}

// NewVault creates a new vault instance.
func NewVault(root string) (*Vault, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("vault root is not a directory: %s", root)
	}
	return &Vault{Root: abs, known: map[string]fileState{}, self: map[string]time.Time{}}, nil
}

// CleanRel validates and normalizes a vault-relative posix path. It rejects
// absolute paths, `..` escapes, empty paths, and Windows-reserved names in
// any segment (CON, PRN, AUX, NUL, COM1-9, LPT1-9, trailing dots/spaces,
// illegal characters < > : " | ? *). Returns the cleaned posix path.
func CleanRel(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty vault path")
	}
	slashed := strings.ReplaceAll(p, "\\", "/")
	if filepath.IsAbs(p) || strings.HasPrefix(slashed, "/") {
		return "", fmt.Errorf("absolute vault path: %q", p)
	}
	cleaned := filepath.Clean(filepath.FromSlash(slashed))
	if cleaned == "." {
		return "", fmt.Errorf("empty vault path")
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("vault path escapes root: %q", p)
	}
	posix := filepath.ToSlash(cleaned)
	for _, seg := range strings.Split(posix, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("invalid path segment %q in %q", seg, p)
		}
		if reservedSegment(seg) {
			return "", fmt.Errorf("reserved file name %q in %q", seg, p)
		}
	}
	return posix, nil
}

func reservedSegment(seg string) bool {
	name := seg
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		// Extension allowed; check stem, and reject trailing dots/spaces.
		stem := name[:i]
		if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
			return true
		}
		name = stem
	} else if strings.HasSuffix(seg, " ") {
		return true
	}
	upper := strings.ToUpper(name)
	switch upper {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) {
		if c := upper[3]; c >= '1' && c <= '9' {
			return true
		}
	}
	for _, r := range seg {
		switch r {
		case '<', '>', ':', '"', '|', '?', '*':
			return true
		case 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31:
			return true
		}
	}
	return false
}

// abs joins a cleaned rel path onto the root (OS-native separators).
func (v *Vault) abs(rel string) string {
	return filepath.Join(v.Root, filepath.FromSlash(rel))
}

// resolveExisting returns the display-case rel path for a file that exists
// with different case (lookup case-insensitive, display preserving, Phase
// 0c). Resolution is segment-wise so "NOTES/HELLO.md" finds "notes/hello.md"
// even on case-sensitive filesystems. Returns rel unchanged when nothing
// matches.
func (v *Vault) resolveExisting(rel string) string {
	if _, err := os.Stat(v.abs(rel)); err == nil {
		return rel
	}
	cur := v.Root
	parts := strings.Split(rel, "/")
	built := []string{}
	for i, seg := range parts {
		next := filepath.Join(cur, seg)
		if _, err := os.Stat(next); err == nil {
			cur = next
			built = append(built, seg)
			continue
		}
		entries, err := os.ReadDir(cur)
		if err != nil {
			return rel
		}
		matched := ""
		for _, e := range entries {
			if strings.EqualFold(e.Name(), seg) {
				matched = e.Name()
				break
			}
		}
		if matched == "" {
			return rel
		}
		// Directories must stay directories and the final segment must not
		// resolve to a directory.
		full := filepath.Join(cur, matched)
		st, err := os.Stat(full)
		if err != nil {
			return rel
		}
		if i < len(parts)-1 && !st.IsDir() {
			return rel
		}
		if i == len(parts)-1 && st.IsDir() {
			return rel
		}
		cur = full
		built = append(built, matched)
	}
	return strings.Join(built, "/")
}

// fold key for known/self maps.
func fold(rel string) string { return strings.ToLower(rel) }

// ReadFile reads a file from the vault.
// Path is vault-relative posix; lookup is case-insensitive, errors and
// events report the display (source-case) path.
func (v *Vault) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rel, err := CleanRel(path)
	if err != nil {
		return nil, err
	}
	rel = v.resolveExisting(rel)
	content, err := os.ReadFile(v.abs(rel))
	if err != nil {
		return nil, err
	}
	if info, serr := os.Stat(v.abs(rel)); serr == nil {
		v.mu.Lock()
		v.known[fold(rel)] = fileState{mtime: info.ModTime().UnixNano(), hash: hashBytes(content)}
		v.mu.Unlock()
	}
	return content, nil
}

// ErrConflict is returned by WriteFile when the disk file changed since this
// process last read/wrote it (mtime/hash compare). The incoming content is
// preserved in ConflictPath (never auto-merged); the original is untouched.
type ErrConflict struct {
	// ConflictPath is the vault-relative posix path of the preserved copy.
	ConflictPath string
	// ParentPath is the contested page.
	ParentPath string
}

func (e *ErrConflict) Error() string {
	return fmt.Sprintf("write conflict on %s (preserved as %s)", e.ParentPath, e.ConflictPath)
}

// WriteFile atomically writes a file to the vault.
// Atomic: re-read + temp + rename + mtime check.
// Preserves comments/order in frontmatter.
// Windows-safe paths.
//
// Clash handling with this signature: the vault remembers the mtime+hash
// from the last ReadFile/WriteFile in this process. If the disk file differs
// from that memory (someone else saved since), the incoming content is parked
// in a `*.conflict-<ts>.md` file (single pattern everywhere) and ErrConflict
// is returned. Editors that Read-before-Write (Lane F2) therefore never
// silently clobber; a process that never read the file gets last-writer-wins
// atomicity (still crash-safe, still indexed).
func (v *Vault) WriteFile(ctx context.Context, path, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rel, err := CleanRel(path)
	if err != nil {
		return err
	}
	return v.write(ctx, rel, content)
}

func (v *Vault) write(ctx context.Context, rel, content string) error {
	full := v.abs(rel)
	info, statErr := os.Stat(full)
	var diskHash string
	var diskMtime int64
	if statErr == nil && !info.IsDir() {
		diskMtime = info.ModTime().UnixNano()
		if b, rerr := os.ReadFile(full); rerr == nil {
			diskHash = hashBytes(b)
		}
	} else if statErr == nil && info.IsDir() {
		return fmt.Errorf("vault path is a directory: %q", rel)
	}
	v.mu.Lock()
	known, haveKnown := v.known[fold(rel)]
	v.mu.Unlock()
	if statErr == nil && haveKnown && (diskMtime != known.mtime || (diskHash != "" && diskHash != known.hash)) {
		// Someone else saved since our last read/write → park incoming.
		cpath, cerr := v.createConflictFile(rel, content)
		if cerr != nil {
			return cerr
		}
		return &ErrConflict{ConflictPath: cpath, ParentPath: rel}
	}
	if err := v.atomicWrite(rel, content); err != nil {
		return err
	}
	if st, serr := os.Stat(full); serr == nil {
		v.mu.Lock()
		v.known[fold(rel)] = fileState{mtime: st.ModTime().UnixNano(), hash: hashBytes([]byte(content))}
		v.self[fold(rel)] = time.Now().Add(selfWriteWindow)
		v.mu.Unlock()
	}
	return nil
}

// atomicWritedurably writes content via temp file in the same directory +
// rename (crash-safe; same-volume atomic on all three OSes).
func (v *Vault) atomicWrite(rel, content string) error {
	full := v.abs(rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after successful rename
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, full)
}

// ConflictName derives the conflict file path for a parent page:
// "notes/a.md" → "notes/a.conflict-<ts>.md" (single pattern everywhere).
func ConflictName(parentRel string, ts time.Time) string {
	ext := filepath.Ext(parentRel)
	stem := strings.TrimSuffix(parentRel, ext)
	return fmt.Sprintf("%s.conflict-%d%s", stem, ts.UnixNano(), ext)
}

// createConflictFile parks content in a fresh *.conflict-<ts>.md file and
// records it in known so later Rescans pick it up.
func (v *Vault) createConflictFile(parentRel, content string) (string, error) {
	for i := 0; i < 10; i++ {
		cpath := ConflictName(parentRel, time.Now())
		full := v.abs(cpath)
		if _, err := os.Stat(full); err == nil {
			time.Sleep(time.Millisecond)
			continue
		}
		if err := v.atomicWrite(cpath, content); err != nil {
			return "", err
		}
		if st, serr := os.Stat(full); serr == nil {
			v.mu.Lock()
			v.known[fold(cpath)] = fileState{mtime: st.ModTime().UnixNano(), hash: hashBytes([]byte(content))}
			v.self[fold(cpath)] = time.Now().Add(selfWriteWindow)
			v.mu.Unlock()
		}
		return cpath, nil
	}
	return "", fmt.Errorf("could not mint conflict file for %q", parentRel)
}

// DeleteFile deletes a file from the vault.
func (v *Vault) DeleteFile(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rel, err := CleanRel(path)
	if err != nil {
		return err
	}
	rel = v.resolveExisting(rel)
	if err := os.Remove(v.abs(rel)); err != nil {
		return err
	}
	v.mu.Lock()
	delete(v.known, fold(rel))
	v.self[fold(rel)] = time.Now().Add(selfWriteWindow)
	v.mu.Unlock()
	return nil
}

// RenameFile renames a file in the vault.
// Phase 0c: rename = delete + create with ACL preservation (owner and
// grants move to the new path; no merge, no widening).
func (v *Vault) RenameFile(ctx context.Context, oldPath, newPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	oldRel, err := CleanRel(oldPath)
	if err != nil {
		return err
	}
	newRel, err := CleanRel(newPath)
	if err != nil {
		return err
	}
	oldRel = v.resolveExisting(oldRel)
	content, err := os.ReadFile(v.abs(oldRel))
	if err != nil {
		return err
	}
	// Create first, then delete: a crash leaves both, never neither.
	// (ACL rows move in the index layer; the vault layer never widens.)
	newExists := false
	if _, err := os.Stat(v.abs(newRel)); err == nil {
		newExists = true
	}
	if newExists {
		return fmt.Errorf("rename target exists: %q", newPath)
	}
	if err := v.atomicWrite(newRel, string(content)); err != nil {
		return err
	}
	if err := os.Remove(v.abs(oldRel)); err != nil {
		_ = os.Remove(v.abs(newRel))
		return err
	}
	v.mu.Lock()
	delete(v.known, fold(oldRel))
	if st, serr := os.Stat(v.abs(newRel)); serr == nil {
		v.known[fold(newRel)] = fileState{mtime: st.ModTime().UnixNano(), hash: hashBytes(content)}
	}
	ts := time.Now().Add(selfWriteWindow)
	v.self[fold(oldRel)], v.self[fold(newRel)] = ts, ts
	v.mu.Unlock()
	return nil
}

// ListFiles lists files in the vault matching a pattern.
func (v *Vault) ListFiles(ctx context.Context, pattern string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []string
	err := v.Walk(ctx, func(fp string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, rerr := filepath.Rel(v.Root, fp)
		if rerr != nil {
			return rerr
		}
		posix := filepath.ToSlash(rel)
		if isHiddenRel(posix) || isConflictRel(posix) {
			return nil
		}
		ok, merr := filepath.Match(pattern, posix)
		if merr != nil {
			// Fall back to prefix match for plain directory prefixes.
			if !strings.HasPrefix(posix, pattern) {
				return nil
			}
			ok = true
		}
		if ok {
			out = append(out, posix)
		}
		return nil
	})
	return out, err
}

func isHiddenRel(posix string) bool {
	for _, seg := range strings.Split(posix, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

func isConflictRel(posix string) bool {
	base := posix
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	i := strings.LastIndex(base, ".conflict-")
	return i >= 0 && strings.HasSuffix(base, ".md")
}

// Walk walks the vault filesystem.
func (v *Vault) Walk(ctx context.Context, fn fs.WalkDirFunc) error {
	return filepath.WalkDir(v.Root, func(fp string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err == nil && d.IsDir() && fp != v.Root && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		return fn(fp, d, err)
	})
}
