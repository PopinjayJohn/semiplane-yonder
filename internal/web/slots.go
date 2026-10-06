package web

import (
	"sort"
	"strings"

	"github.com/semiplane/yonder/internal/auth"
)

// SlotRegistry manages UI slots for plugin/components injection.
// Slots: header-*, sidebar-left/right, footer, page-actions, sheet-header
// {component, priority, show-if}, GM-disablable, SSE-patchable, secret-filtered input.
type SlotRegistry struct {
	slots map[string][]SlotComponent
}

// SlotComponent represents a component registered in a slot.
type SlotComponent struct {
	SlotName       string // slot name (e.g., "header-left")
	ID             string
	Component      string // templ component name
	Priority       int    // higher = first
	ShowIf         ShowCondition
	GMOnly         bool
	SecretFiltered bool   // input filtered by secrets.Filter
	PluginID       string // for namespacing CSS
}

// ShowCondition defines when a slot component should be shown.
type ShowCondition struct {
	// Viewer must have all of these grants
	Grants []string
	// Viewer must not have any of these grants
	NotGrants []string
	// Custom expression (evaluated server-side)
	Expression string
	// Path prefix match
	PathPrefix string
}

// NewSlotRegistry creates a new slot registry.
func NewSlotRegistry() *SlotRegistry {
	return &SlotRegistry{
		slots: make(map[string][]SlotComponent),
	}
}

// Register adds a component to a slot.
func (r *SlotRegistry) Register(slot string, component SlotComponent) {
	r.slots[slot] = append(r.slots[slot], component)
}

// Get returns components for a slot, filtered by viewer and path.
// GM-only components require an effective GM (GM preview-as-player hides
// them: the preview must show exactly what the previewed user sees).
// ShowIf grants match case-insensitively; PathPrefix gates per-page slots.
// Sorted by Priority descending. (Lane F1 implements the Phase-0 stub;
// signature and slot names unchanged.)
func (r *SlotRegistry) Get(slot string, viewer *auth.Viewer, path string) []SlotComponent {
	if r == nil {
		return nil
	}
	grants := map[string]bool{}
	if viewer != nil {
		for _, g := range viewer.Grants {
			grants[strings.ToLower(g)] = true
		}
	}
	var out []SlotComponent
	for _, c := range r.slots[slot] {
		if c.GMOnly && !effectiveGM(viewer) {
			continue
		}
		ok := true
		for _, g := range c.ShowIf.Grants {
			if !grants[strings.ToLower(g)] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		for _, g := range c.ShowIf.NotGrants {
			if grants[strings.ToLower(g)] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if c.ShowIf.PathPrefix != "" && !strings.HasPrefix(path, c.ShowIf.PathPrefix) {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Priority > out[j].Priority })
	return out
}

// effectiveGM reports GM status honoring GM-only PreviewAs impersonation:
// a preview filters as the previewed user, never as GM.
func effectiveGM(viewer *auth.Viewer) bool {
	if viewer == nil {
		return false
	}
	if viewer.PreviewAs != "" {
		return false
	}
	return viewer.IsGM
}

// Core slot names (frozen - amend only)
const (
	SlotHeaderLeft   = "header-left"
	SlotHeaderCenter = "header-center"
	SlotHeaderRight  = "header-right"
	SlotSidebarLeft  = "sidebar-left"
	SlotSidebarRight = "sidebar-right"
	SlotFooter       = "footer"
	SlotPageActions  = "page-actions"
	SlotSheetHeader  = "sheet-header"
)
