package plugins

import (
	"context"
	"fmt"
	"github.com/semiplane/yonder/internal/auth"
	"html"
	"sort"
	"strings"

	"github.com/semiplane/yonder/internal/web"
)

// Collapsible vault tree for slot sidebar-left (plugin ID vault-tree).
//
// Pure logic + fragment rendering only: no vault/DB writes, no new
// dependencies. Input is caller-supplied, ALREADY secret-filtered nav rows
// (the same fail-closed Filtered pattern as DashboardSource): secret
// titles/paths are dropped by the caller (navEntries drops, never redacts
// in listings) and never reach this package. The renderer escapes every
// string via html.EscapeString.

// VaultTreePluginID is the plugin/slot/component ID of the vault tree.
const VaultTreePluginID = "vault-tree"

// VaultTreeEntry is one already-secret-filtered nav row (Path is the
// extensionless nav path, e.g. "quests/ambush").
type VaultTreeEntry struct {
	Path   string
	Title  string
	Secret bool // visible-to-this-viewer secret flag (text badge, never color-only)
}

// VaultTreeInput carries ONLY secret-filtered entries (red line). Construct
// via NewVaultTreeInput; BuildTree rejects inputs with Filtered=false.
type VaultTreeInput struct {
	Filtered bool
	Entries  []VaultTreeEntry
}

// NewVaultTreeInput marks caller-filtered rows as safe input.
func NewVaultTreeInput(entries []VaultTreeEntry) VaultTreeInput {
	return VaultTreeInput{Filtered: true, Entries: entries}
}

// VaultTreePage is one leaf page in the tree.
type VaultTreePage struct {
	Path   string
	Title  string
	Secret bool
	Active bool
}

// VaultTreeFolder is one folder node. The root has Name "" and Path "".
type VaultTreeFolder struct {
	Name    string
	Path    string // slash-joined prefix, "" at root
	Depth   int    // root = -1, top-level folders = 0
	Folders []*VaultTreeFolder
	Pages   []*VaultTreePage
}

// normalizeTreePath normalizes one slash-separated nav path. Windows-safe:
// splits on "/" only (backslashes are tolerated as separators, never
// assumed), no reserved-name assumptions (CON, trailing dots, etc. are
// ordinary names), no filesystem access.
func normalizeTreePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimSpace(p)
	p = strings.Trim(p, "/")
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	return p
}

// normalizeActive folds an active page id to the extensionless nav form so
// "quests/ambush.md" matches entry "quests/ambush".
func normalizeActive(p string) string {
	p = normalizeTreePath(p)
	lower := strings.ToLower(p)
	if strings.HasSuffix(lower, ".md") {
		p = p[:len(p)-len(".md")]
	} else if strings.HasSuffix(lower, ".markdown") {
		p = p[:len(p)-len(".markdown")]
	}
	return strings.Trim(p, "/")
}

// BuildTree builds a hierarchical folder tree from slash-separated,
// already-secret-filtered entries. It refuses unfiltered input fail-closed.
// Order is deterministic: entries sort by case-insensitive path, then title
// (same rule as navEntries), and folders/pages sort the same way at every
// level.
func BuildTree(in VaultTreeInput, activePath string) (*VaultTreeFolder, error) {
	if !in.Filtered {
		return nil, &PluginError{Code: "UNFILTERED_INPUT", Message: "vault-tree input must be secret-filtered"}
	}
	active := normalizeActive(activePath)
	entries := make([]VaultTreeEntry, 0, len(in.Entries))
	for _, e := range in.Entries {
		p := normalizeTreePath(e.Path)
		if p == "" {
			continue
		}
		entries = append(entries, VaultTreeEntry{Path: p, Title: e.Title, Secret: e.Secret})
	}
	sort.Slice(entries, func(i, j int) bool {
		li, lj := strings.ToLower(entries[i].Path), strings.ToLower(entries[j].Path)
		if li != lj {
			return li < lj
		}
		return entries[i].Title < entries[j].Title
	})

	root := &VaultTreeFolder{Depth: -1}
	byPath := map[string]*VaultTreeFolder{"": root}
	// Folders are created in first-use order, then sorted below.
	for _, e := range entries {
		segs := strings.Split(e.Path, "/")
		prefix := ""
		parent := root
		for _, seg := range segs[:len(segs)-1] {
			if prefix == "" {
				prefix = seg
			} else {
				prefix += "/" + seg
			}
			f, ok := byPath[prefix]
			if !ok {
				f = &VaultTreeFolder{Name: seg, Path: prefix, Depth: parent.Depth + 1}
				byPath[prefix] = f
				parent.Folders = append(parent.Folders, f)
			}
			parent = f
		}
		title := e.Title
		if strings.TrimSpace(title) == "" {
			title = segs[len(segs)-1]
		}
		parent.Pages = append(parent.Pages, &VaultTreePage{
			Path:   e.Path,
			Title:  title,
			Secret: e.Secret,
			Active: active != "" && strings.EqualFold(e.Path, active),
		})
	}
	sortTree(root)
	return root, nil
}

func sortTree(f *VaultTreeFolder) {
	sort.Slice(f.Folders, func(i, j int) bool {
		li, lj := strings.ToLower(f.Folders[i].Path), strings.ToLower(f.Folders[j].Path)
		if li != lj {
			return li < lj
		}
		return f.Folders[i].Name < f.Folders[j].Name
	})
	sort.Slice(f.Pages, func(i, j int) bool {
		li, lj := strings.ToLower(f.Pages[i].Path), strings.ToLower(f.Pages[j].Path)
		if li != lj {
			return li < lj
		}
		return f.Pages[i].Title < f.Pages[j].Title
	})
	for _, sub := range f.Folders {
		sortTree(sub)
	}
}

// RenderTree renders the folder tree as an SSE-patchable fragment: a
// <nav aria-label="Vault tree"> landmark with nested <details>/<summary>
// per folder (first level open, rest closed), links for pages, and
// aria-current="page" on the active link. Secret flags render as text
// badges (never color-only). Every string is HTML-escaped; secret titles
// never appear because the caller drops them before BuildTree.
func RenderTree(root *VaultTreeFolder) UIFragment {
	var b strings.Builder
	b.WriteString(`<nav aria-label="Vault tree" data-plugin="vault-tree">` + "\n")
	b.WriteString(`<div class="vt-wrap" id="vault-tree" data-sse-fragment="vault-tree" aria-live="polite">` + "\n")
	if root != nil {
		b.WriteString(`<ul class="vt-list">` + "\n")
		renderTreePages(&b, root.Pages)
		for _, f := range root.Folders {
			renderTreeFolder(&b, f)
		}
		b.WriteString("</ul>\n")
	}
	b.WriteString("</div>\n</nav>")
	return UIFragment{Target: "vault-tree", HTML: b.String()}
}

func renderTreeFolder(b *strings.Builder, f *VaultTreeFolder) {
	open := ""
	if f.Depth <= 0 {
		open = " open"
	}
	// The count rides in the accessible name and is aria-hidden in the
	// chrome: the visible label (the folder name) must be contained in the
	// name (WCAG 2.5.3 Label in Name; axe label-content-name-mismatch), so
	// "Toggle folder maps, 1 item" wraps the visible "maps" while AT users
	// still get the count.
	count := len(f.Folders) + len(f.Pages)
	noun := "items"
	if count == 1 {
		noun = "item"
	}
	fmt.Fprintf(b, `<li class="vt-folder" data-tree-path="%s">`, html.EscapeString(f.Path))
	fmt.Fprintf(b, `<details class="vt-details"%s>`, open)
	fmt.Fprintf(b, `<summary class="vt-summary" aria-label="Toggle folder %s, %d %s"><span class="vt-caret" aria-hidden="true">▾</span> <span class="vt-name">%s</span> <span class="vt-count" aria-hidden="true">%d</span></summary>`+"\n",
		html.EscapeString(f.Name), count, noun, html.EscapeString(f.Name), count)
	b.WriteString(`<ul class="vt-list">` + "\n")
	renderTreePages(b, f.Pages)
	for _, sub := range f.Folders {
		renderTreeFolder(b, sub)
	}
	b.WriteString("</ul>\n</details></li>\n")
}

func renderTreePages(b *strings.Builder, pages []*VaultTreePage) {
	for _, p := range pages {
		current := ""
		if p.Active {
			current = ` aria-current="page"`
		}
		fmt.Fprintf(b, `<li class="vt-page" data-tree-path="%s"><a class="vt-link" href="/p/%s"%s>%s</a>`,
			html.EscapeString(p.Path), html.EscapeString(p.Path), current, html.EscapeString(p.Title))
		if p.Secret {
			b.WriteString(` <span class="vt-badge">secret</span>`)
		}
		b.WriteString("</li>\n")
	}
}

// RenderSlot (T1-D) maps the filtered shell data onto the tree input and
// renders the sidebar-left fragment. Entries arrive already filtered
// (BuildTree rejects unmarked input); the active page highlights itself.
func (VaultTreePlugin) RenderSlot(slot string, in web.SlotShellData, _ *auth.Viewer) (string, error) {
	if slot != web.SlotSidebarLeft {
		return "", nil
	}
	entries := make([]VaultTreeEntry, 0, len(in.Pages))
	for _, p := range in.Pages {
		entries = append(entries, VaultTreeEntry{Path: p.Path, Title: p.Title, Secret: p.Secret})
	}
	root, err := BuildTree(NewVaultTreeInput(entries), in.Path)
	if err != nil {
		return "", err
	}
	return RenderTree(root).HTML, nil
}

// vaultTreeCSS is the plugin stylesheet: @layer plugins, every selector
// scoped under [data-plugin="vault-tree"], no bare elements, no !important.
// Links/summaries meet the 44px touch target; active links are bold +
// underlined (never color-only); transitions yield to prefers-reduced-motion.
const vaultTreeCSS = `@layer plugins {
[data-plugin="vault-tree"] .vt-wrap { padding: 0.5rem; }
[data-plugin="vault-tree"] .vt-list { list-style: none; margin: 0; padding-left: 0; }
[data-plugin="vault-tree"] .vt-list .vt-list { padding-left: 1rem; }
[data-plugin="vault-tree"] .vt-summary { display: flex; align-items: center; gap: 0.5rem; min-height: 44px; cursor: pointer; border-radius: 0.375rem; }
[data-plugin="vault-tree"] .vt-summary:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
[data-plugin="vault-tree"] .vt-link { display: flex; align-items: center; min-height: 44px; border-radius: 0.375rem; text-decoration: none; }
[data-plugin="vault-tree"] .vt-link:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
[data-plugin="vault-tree"] .vt-link[aria-current="page"] { font-weight: 700; text-decoration: underline; }
[data-plugin="vault-tree"] .vt-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
[data-plugin="vault-tree"] .vt-count { font-size: 0.75rem; border: 1px solid var(--border); border-radius: 999px; padding: 0 0.5rem; }
[data-plugin="vault-tree"] .vt-badge { font-size: 0.75rem; border: 1px solid var(--border); border-radius: 0.25rem; padding: 0 0.375rem; }
[data-plugin="vault-tree"] .vt-caret { transition: transform 0.15s ease; }
[data-plugin="vault-tree"] .vt-details[open] .vt-caret { transform: rotate(0deg); }
@media (prefers-reduced-motion: reduce) {
[data-plugin="vault-tree"] .vt-caret { transition: none; }
[data-plugin="vault-tree"] .vt-link { transition: none; }
}
}`

// VaultTreePlugin is the sidebar-left vault-tree Plugin implementation.
type VaultTreePlugin struct{}

// Compile-time interface check.
var _ Plugin = (*VaultTreePlugin)(nil)

// NewVaultTreePlugin constructs the plugin.
func NewVaultTreePlugin() *VaultTreePlugin { return &VaultTreePlugin{} }

func (p *VaultTreePlugin) ID() string      { return VaultTreePluginID }
func (p *VaultTreePlugin) Name() string    { return "Vault tree" }
func (p *VaultTreePlugin) Version() string { return "1.0.0" }

func (p *VaultTreePlugin) Init(_ context.Context, _ *Registry) error { return nil }

// Routes returns nil: the tree is pure fragment rendering over
// caller-filtered input, with no vault/DB writes and no new routes.
func (p *VaultTreePlugin) Routes() []web.Route { return nil }

// Slots contributes the collapsible tree to sidebar-left.
func (p *VaultTreePlugin) Slots() []web.SlotComponent {
	return []web.SlotComponent{{
		SlotName: web.SlotSidebarLeft, ID: VaultTreePluginID,
		Component: VaultTreePluginID, Priority: 20,
		SecretFiltered: true, PluginID: VaultTreePluginID,
	}}
}

// CSS returns the namespaced plugin stylesheet (well under the 20KB cap).
func (p *VaultTreePlugin) CSS() string { return vaultTreeCSS }

// JS returns "": native <details>/<summary> needs no script.
func (p *VaultTreePlugin) JS() string { return "" }

func (p *VaultTreePlugin) OnEnable(_ context.Context) error  { return nil }
func (p *VaultTreePlugin) OnDisable(_ context.Context) error { return nil }

// ConfigSchema describes the optional tree configuration.
func (p *VaultTreePlugin) ConfigSchema() string {
	return `{"type":"object","additionalProperties":false,"properties":{"openDepth":{"type":"integer","minimum":0,"maximum":8}}}`
}

// ValidateConfig accepts an empty config or a bounded openDepth.
func (p *VaultTreePlugin) ValidateConfig(config map[string]any) error {
	if len(config) == 0 {
		return nil
	}
	for k, v := range config {
		switch k {
		case "openDepth":
			n, ok := v.(float64)
			if !ok {
				if i, ok := v.(int); ok {
					n = float64(i)
				} else {
					return &PluginError{Code: "BAD_CONFIG", Message: "vault-tree: openDepth must be an integer 0..8"}
				}
			}
			if n != float64(int(n)) || n < 0 || n > 8 {
				return &PluginError{Code: "BAD_CONFIG", Message: "vault-tree: openDepth must be an integer 0..8"}
			}
		default:
			return &PluginError{Code: "BAD_CONFIG", Message: "vault-tree: unknown config key " + k}
		}
	}
	return nil
}

// A11y declares the tree's screen-reader contract: the navigation
// landmark, the folder-toggle + page-link labels, and the polite live
// region used for SSE patches. Badges are text, never color-only.
func (p *VaultTreePlugin) A11y() A11ySpec {
	return A11ySpec{
		Landmarks:   []string{"navigation"},
		Labels:      []string{"Vault tree", "Toggle folder", "secret"},
		LiveRegions: []string{"polite"},
	}
}
