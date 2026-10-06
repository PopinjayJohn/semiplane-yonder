package vault

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watch watches the vault for changes (fsnotify + debounce + self-write tolerance).
func (v *Vault) Watch(ctx context.Context, fn func(events []Event)) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }()
	if err := addRecursive(w, v.Root); err != nil {
		return err
	}
	var pending []Event
	seen := map[string]EventOp{}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	flush := func() []Event {
		out := pending
		pending = nil
		seen = map[string]EventOp{}
		return out
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			_ = err // watcher errors are non-fatal; next events still flow
		case <-timer.C:
			if len(pending) > 0 {
				fn(flush())
			}
		case ev, ok := <-w.Events:
			if !ok {
				if len(pending) > 0 {
					fn(flush())
				}
				return nil
			}
			rel, rerr := filepath.Rel(v.Root, ev.Name)
			if rerr != nil {
				continue
			}
			posix := filepath.ToSlash(rel)
			if isHiddenRel(posix) {
				continue
			}
			// New directories need watches of their own.
			if ev.Op&(fsnotify.Create) != 0 {
				if st, serr := os.Stat(ev.Name); serr == nil && st.IsDir() {
					_ = addRecursive(w, ev.Name)
					continue // dir creation itself is not a content event
				}
			}
			op := classifyOp(ev.Op)
			if op < 0 {
				continue // Chmod and friends: not content
			}
			self := v.isSelf(posix)
			key := posix
			if prev, dup := seen[key]; dup {
				// Collapse: delete wins over write, write over create.
				if prev == EventDelete || op == EventDelete {
					op = EventDelete
				} else {
					op = EventWrite
				}
				for i, p := range pending {
					if p.Path == posix {
						pending[i].Op = op
						pending[i].Time = time.Now().UnixNano()
						pending[i].Self = pending[i].Self && self
						break
					}
				}
				timer.Reset(DebounceInterval)
				continue
			}
			seen[key] = op
			pending = append(pending, Event{Path: posix, Op: op, Time: time.Now().UnixNano(), Self: self})
			timer.Reset(DebounceInterval)
		}
	}
}

// DebounceInterval coalesces fsnotify storms (multi-event saves from Obsidian
// and external editors) into one callback. Lowered by tests.
var DebounceInterval = 250 * time.Millisecond

// selfWriteWindow marks paths written by this process so Watch can flag them
// Self (self-write tolerance: consumers skip reindex for Self events instead
// of storming, but still use them for SSE/presence).
var selfWriteWindow = 2 * time.Second

func (v *Vault) isSelf(posix string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if dl, ok := v.self[fold(posix)]; ok {
		if time.Now().Before(dl) {
			return true
		}
		delete(v.self, fold(posix))
	}
	return false
}

func addRecursive(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(fp string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		if fp != root && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		// Duplicate adds are EEXIST-safe in fsnotify (returns nil or
		// "already watched" error — tolerate both).
		if aerr := w.Add(fp); aerr != nil && !strings.Contains(aerr.Error(), "already") {
			return aerr
		}
		return nil
	})
}

func classifyOp(op fsnotify.Op) EventOp {
	switch {
	case op&(fsnotify.Remove|fsnotify.Rename) != 0:
		return EventDelete
	case op&fsnotify.Create != 0:
		return EventCreate
	case op&fsnotify.Write != 0:
		return EventWrite
	default:
		return EventOp(-1)
	}
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
