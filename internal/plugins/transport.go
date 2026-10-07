package plugins

import (
	"context"

	"github.com/semiplane/yonder/internal/auth"
)

// P12 transport interfaces (owned by Lane I2; H2's dice engine implements
// these, Lane K consumes them for display).
//
// Core carries opaque roll results plus a generic roller; it never defines
// what dice mean. Transport: log, blind routing, broadcast, replay of stored
// values with per-viewer re-auth (crypto/rand entropy at roll time).
// Maxima (p12): 100 dice, 1000 faces/symbols per roll, 10 rolls/sec per
// authenticated user (per-IP bucket for guests). Bases may set lower values.

// Transport limits (p12 §Scope, transport maxima).
const (
	MaxDicePerRoll   = 100
	MaxFacesPerRoll  = 1000
	MaxRollsPerSec   = 10
	MaxRollLogLimit  = 100
	BlindPlaceholder = "▓▓ blind roll (GM only)"
)

// RollEvent is the opaque broadcast unit: identity + routing, never faces.
// Totals/faces stay in the engine log; replay re-authorizes per viewer.
type RollEvent struct {
	RollID   string // unique roll ID (log key)
	ActorID  string // roller (character/NPC id)
	IntentID string // ruleset intent envelope id ("" when free roll)
	Notation string // grammar text (e.g. "2d6+3"); display only
	Blind    bool   // GM-blind: only GM (and roller, if GM) sees values
	Total    *int64 // nil when withheld from this viewer
}

// ViewerOf maps a request viewer to the routing identity. GM preview-as
// filters as the previewed user, never as GM (Phase 0c contract).
func ViewerOf(v *auth.Viewer) auth.Viewer {
	if v == nil {
		return auth.Viewer{}
	}
	if v.PreviewAs != "" {
		return auth.Viewer{UserID: v.PreviewAs}
	}
	return *v
}

// RouteBlind applies GM-blind routing to one event for one viewer: blind
// rolls reveal totals to effective GMs only; everyone else gets the event
// with Total=nil (renderers show BlindPlaceholder as text — never
// color-only, announced to screen readers).
func RouteBlind(ev RollEvent, viewer *auth.Viewer) RollEvent {
	if !ev.Blind {
		return ev
	}
	eff := ViewerOf(viewer)
	if eff.IsGM {
		return ev
	}
	ev.Total = nil
	return ev
}

// Publisher broadcasts roll events to SSE subscribers (per-viewer filtered
// via RouteBlind before the wire).
type Publisher interface {
	Publish(ctx context.Context, ev RollEvent) error
}

// DiceLogReader serves the replayable dice log. Replay reads stored values,
// never re-rolls; every replayed event is re-authorized against the current
// viewer (blind rolls stay blind).
type DiceLogReader interface {
	Recent(ctx context.Context, actorID string, limit int) ([]RollEvent, error)
	Replay(ctx context.Context, rollID string, viewer *auth.Viewer) (RollEvent, error)
}

// ClampLimit bounds a log-page limit to [1, MaxRollLogLimit].
func ClampLimit(n int) int {
	if n < 1 {
		return 1
	}
	if n > MaxRollLogLimit {
		return MaxRollLogLimit
	}
	return n
}
