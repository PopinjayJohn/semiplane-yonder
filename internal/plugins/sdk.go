package plugins

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/semiplane/yonder/internal/web"
)

// P06 Feature / UI / Ruleset SDK.
//
// Kinds (p06): Ruleset (data, hot-reload) / Feature (Go) / UI (templ slots).
// This file defines the Go half: Feature, SheetRenderer, BlockRenderer,
// RulesetDef, plus slot-contribution validation and the --plugins /
// enabled-plugins plumbing. Namespaces: plugin IDs live apart from ruleset
// enabled-features (separate registries, separate config keys).

// Feature is a compiled-in Go extension (random-tables first, then
// initiative-tracker, vtt-maps, character-wizard, secrets-advanced).
type Feature interface {
	// FeatureID is the stable feature ID (also a valid plugin ID).
	FeatureID() string
	// FeatureRoutes are the feature's HTTP routes, registered via the frozen
	// web.RouteRegistry — never by editing core routing.
	FeatureRoutes() []web.Route
	// Publish emits a feature event onto the SSE broadcast (secret-filtered
	// per viewer by the transport layer before it hits the wire).
	Publish(ctx context.Context, event FeatureEvent) error
}

// FeatureEvent is an opaque feature-level broadcast (dice, initiative,
// spawns). The transport drops or redacts it per viewer; features never
// branch on secrecy themselves.
type FeatureEvent struct {
	Name    string // SSE event name (see fragment.go catalog)
	MapID   string // VTT map scope, "" when global
	Payload string // pre-rendered, already secret-filtered fragment or ID reference
}

// SheetRenderer renders a character sheet fragment from filtered input.
type SheetRenderer interface {
	RenderSheet(ctx context.Context, in SheetInput) (UIFragment, error)
}

// BlockRenderer renders one vault block from filtered input.
type BlockRenderer interface {
	RenderBlock(ctx context.Context, in BlockInput) (UIFragment, error)
}

// RulesetDef is the Go-side handle for a data-only ruleset pack
// (bases/overlays install as vault packs under rules/, never bundled).
type RulesetDef interface {
	RulesetID() string
	Validate() error
}

// SheetInput carries ONLY secret-filtered sheet data (red line). Construct
// via NewSheetInput; renderers must reject inputs with Filtered=false.
type SheetInput struct {
	Viewer    string // username the fragment is rendered for ("" = guest)
	Filtered  bool   // set only by NewSheetInput after secrets.Filter
	TitleHTML string // pre-escaped, filtered title fragment
	BodyHTML  string // pre-escaped, filtered body fragment
}

// NewSheetInput builds a renderer input from already-filtered HTML.
func NewSheetInput(viewer, titleHTML, bodyHTML string) SheetInput {
	return SheetInput{Viewer: viewer, Filtered: true, TitleHTML: titleHTML, BodyHTML: bodyHTML}
}

// BlockInput carries ONLY secret-filtered block data (red line).
type BlockInput struct {
	Viewer   string
	Filtered bool
	BodyHTML string
}

// NewBlockInput builds a renderer input from already-filtered HTML.
func NewBlockInput(viewer, bodyHTML string) BlockInput {
	return BlockInput{Viewer: viewer, Filtered: true, BodyHTML: bodyHTML}
}

// UIFragment is an SSE-patchable HTML fragment: Target is the DOM id the
// Datastar morph addresses, HTML is the escaped fragment body.
type UIFragment struct {
	Target string
	HTML   string
}

var pluginIDRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// ValidPluginID reports whether id is a legal plugin/feature ID
// (lowercase alnum + dashes, e.g. "random-tables").
func ValidPluginID(id string) bool {
	return len(id) > 0 && len(id) <= 64 && pluginIDRe.MatchString(id)
}

// FrozenSlot reports whether name is one of the P06 core slots.
// Slot names are frozen (amend only); plugins cannot invent slots.
func FrozenSlot(name string) bool {
	switch name {
	case web.SlotHeaderLeft, web.SlotHeaderCenter, web.SlotHeaderRight,
		web.SlotSidebarLeft, web.SlotSidebarRight, web.SlotFooter,
		web.SlotPageActions, web.SlotSheetHeader:
		return true
	}
	return false
}

// ValidateSlot enforces the contribution contract for one slot component:
// frozen slot name, non-empty component, owning PluginID set, and — the red
// line — SecretFiltered input.
func ValidateSlot(pluginID string, s web.SlotComponent) error {
	if !FrozenSlot(s.SlotName) {
		return &PluginError{Code: "BAD_SLOT", Message: fmt.Sprintf("%s: unknown slot %q", pluginID, s.SlotName)}
	}
	if strings.TrimSpace(s.Component) == "" {
		return &PluginError{Code: "BAD_SLOT", Message: pluginID + ": empty component"}
	}
	if s.PluginID != pluginID {
		return &PluginError{Code: "BAD_SLOT", Message: fmt.Sprintf("%s: PluginID %q mismatch", pluginID, s.PluginID)}
	}
	if !s.SecretFiltered {
		return &PluginError{Code: "UNFILTERED_SLOT", Message: pluginID + "/" + s.ID + ": slot input must be secret-filtered"}
	}
	return nil
}

// EnabledHandler wraps a plugin route handler so a disabled plugin serves a
// uniform 404 (no UI trace, no existence oracle beyond the shared 404).
func EnabledHandler(reg *Registry, id string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if reg == nil || !reg.IsEnabled(id) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h(w, r)
	}
}

// ParseEnabledPlugins merges the --plugins= CSV flag with the campaign.yaml
// enabled-plugins[] list into a validated, deduplicated, sorted ID set.
// Unknown-but-well-formed IDs pass through (resolution happens against the
// registry at Enable time); malformed IDs are an error.
func ParseEnabledPlugins(flagCSV string, campaign []string) ([]string, error) {
	seen := map[string]bool{}
	var bad []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if !ValidPluginID(id) {
			bad = append(bad, id)
			return
		}
		seen[id] = true
	}
	for _, id := range strings.Split(flagCSV, ",") {
		add(id)
	}
	for _, id := range campaign {
		add(id)
	}
	if len(bad) > 0 {
		return nil, &PluginError{Code: "BAD_PLUGIN_ID", Message: "invalid plugin ids: " + strings.Join(bad, ", ")}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sortStrings(out)
	return out, nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
