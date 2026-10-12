package plugins

import (
	"log/slog"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/web"
)

// ---------------------------------------------------------------------------
// Slot fragment rendering (T1-D). The shell mounts one
// <div data-plugin-slot> per enabled component and fills it with the plugin's
// rendered fragment. This file is the dispatch half: each plugin implements
// SlotRenderer, and the registry renders enabled plugins only.
//
// Red line (P06): SlotRenderer receives ONLY already-secret-filtered shell
// data (web.SlotShellData). Plugins map that neutral data onto their own
// Source/Input types — which refuse unfiltered input fail-closed — and return
// their escaped HTML. A render error leaves the mount EMPTY: never
// half-rendered, never unfiltered, never a broken page.
// ---------------------------------------------------------------------------

// SlotRenderer is the optional per-plugin render hook. slot is the frozen
// slot name; a plugin returns ("", nil) for slots it does not contribute to.
type SlotRenderer interface {
	RenderSlot(slot string, in web.SlotShellData, viewer *auth.Viewer) (string, error)
}

// RenderSlots renders every enabled plugin's fragment for one slot, in the
// provider's mount order (SlotsFor: priority-sorted, viewer/path-gated).
// A plugin that does not implement SlotRenderer, or whose render fails, is
// skipped with a log — the mount simply stays empty, which is the
// fail-closed outcome.
func (r *Registry) RenderSlots(slot string, viewer *auth.Viewer, path string, in web.SlotShellData) []web.SlotFragment {
	var out []web.SlotFragment
	for _, c := range r.SlotsFor(slot, viewer, path) {
		r.mu.RLock()
		p := r.plugins[c.PluginID]
		r.mu.RUnlock()
		sr, ok := p.(SlotRenderer)
		if !ok {
			continue
		}
		html, err := sr.RenderSlot(slot, in, viewer)
		if err != nil {
			slog.Warn("plugin slot render failed, mount stays empty",
				"plugin", c.PluginID, "slot", slot, "err", err)
			continue
		}
		if html == "" {
			continue
		}
		out = append(out, web.SlotFragment{ID: c.ID, HTML: html})
	}
	return out
}

// Compile-time proof the registry satisfies the seam web type-asserts.
var _ web.SlotRenderer = (*Registry)(nil)
