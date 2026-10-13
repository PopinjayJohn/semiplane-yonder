package plugins

// FROZEN CONTRACT for Lane K (Phase 4 VTT) — I2 exit gate.
//
// This file is the join-gate handoff: map-fragment URL + SSE event catalog +
// state-snapshot endpoint. Lane K implements the server side; the GM
// dashboard (dashboard.go) embeds the fragment. Names/paths below are FROZEN:
// changing them needs an explicit amend, never a unilateral edit.
//
// Lineage: M1 shipped GET /events with `hello` + `secret-flip` (read path,
// Lane F1). Those two names are immutable. All other names below are new in
// Phase 3 and frozen as of I2 exit.

const (
	// FragmentVersion pins the fragment/snapshot schema (bump only via amend).
	// v2 (amend A-VTT2): adds walls/objects/doors/lights/ambient for the
	// PixiJS renderer + UVTT import; v1 rect fog encoding is unchanged and
	// v1 clients ignore the new fields.
	FragmentVersion = 2

	// MapFragmentPath is the per-map HTML fragment Lane K serves:
	//   GET /vtt/{mapID}/fragment
	// Login required; secret-filtered per viewer (hidden tokens/fog zones
	// omitted server-side, never hidden client-side). Response is the inner
	// HTML for MapFragmentTarget, patched over SSE (Datastar morph, focus
	// retained, aria-live per fragment.go rules below).
	MapFragmentPath = "/vtt/{mapID}/fragment"

	// MapFragmentTarget is the DOM id the fragment morphs into.
	MapFragmentTarget = "vtt-map"

	// StateSnapshotPath is the full-state resync endpoint Lane K serves:
	//   GET /api/vtt/{mapID}/state
	// Login required; per-viewer filtered; `Cache-Control: private,
	// no-store`. Clients fetch it on (re)connect and after EventFogUpdate;
	// fog fail-closed (hidden) on data loss.
	StateSnapshotPath = "/api/vtt/{mapID}/state"

	// StreamPath is the existing SSE stream (Lane F1). All events below ride
	// it. Payloads carry IDs/paths or per-viewer filtered fragments;
	// subscribers re-read + re-filter server-side before rendering.
	StreamPath = "/events"
)

// SSE event names (frozen). hello + secret-flip shipped in M1 and are
// immutable; the rest are new in Phase 3.
const (
	// EventHello is the connect greeting (existing, M1).
	EventHello = "hello"
	// EventSecretFlip wakes views after a GM -/+ toggle (existing, M1).
	EventSecretFlip = "secret-flip"
	// EventDiceRoll broadcasts a party-visible roll (aria-live polite).
	EventDiceRoll = "dice-roll"
	// EventDiceBlind notifies a blind roll occurred; only GMs receive values,
	// players receive the redacted notice (aria-live polite).
	EventDiceBlind = "dice-blind"
	// EventInitiativeUpdate carries the reordered initiative list fragment
	// (aria-live polite; order changes announced as text).
	EventInitiativeUpdate = "initiative-update"
	// EventTokenMove carries one token's new coordinates + fragment patch.
	EventTokenMove = "token-move"
	// EventTokenHP carries token HP changes (GM sees values; players see
	// only tokens they may see, values included — HP is board-visible).
	EventTokenHP = "token-hp"
	// EventFogUpdate invalidates open map views; clients refetch the
	// state snapshot (aria-live assertive for reveals).
	EventFogUpdate = "fog-update"
	// EventEncounterSpawn announces an encounter spawn intent committed
	// (tokens + initiative written in one action by Lane K).
	EventEncounterSpawn = "encounter-spawn"
	// EventSessionRecap announces session-note recap updates.
	EventSessionRecap = "session-recap"
)

// StateSnapshot is the JSON shape of StateSnapshotPath (versioned by
// FragmentVersion). All slices are per-viewer filtered server-side.
type StateSnapshot struct {
	Version    int                 `json:"version"`
	MapID      string              `json:"map_id"`
	Tokens     []SnapshotToken     `json:"tokens"`
	Fog        SnapshotFog         `json:"fog"`
	Initiative []SnapshotInitEntry `json:"initiative"`
	// v2 fields (omitempty: v1-shaped rows encode identically and v1
	// clients ignore unknown keys). Walls/objects/doors/lights/ambient
	// are board-visible to all map viewers; hidden-token stripping is
	// unchanged.
	Walls   []WallSeg   `json:"walls,omitempty"`
	Objects []WallSeg   `json:"objects,omitempty"`
	Doors   []DoorState `json:"doors,omitempty"`
	Lights  []Light     `json:"lights,omitempty"`
	Ambient string      `json:"ambient,omitempty"`
}

// WallSeg is one blocker segment in grid-square floats.
type WallSeg struct {
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
	X2 float64 `json:"x2"`
	Y2 float64 `json:"y2"`
}

// DoorState is one portal with live open/closed state. A/B are the portal
// endpoints (grid squares); Closed is the effective state (sidecar default
// overridden by the vtt_doors row when present, fail-closed to closed on
// loss). Idx is the 0-based position in the sidecar portals list.
type DoorState struct {
	Idx    int        `json:"idx"`
	A      [2]float64 `json:"a"`
	B      [2]float64 `json:"b"`
	Closed bool       `json:"closed"`
}

// Light is one dynamic light in grid squares (range in squares, color
// lowercase rrggbb).
type Light struct {
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Range     float64 `json:"range"`
	Color     string  `json:"color"`
	Intensity float64 `json:"intensity"`
	Shadows   bool    `json:"shadows"`
}

// SnapshotToken is one board-visible token.
type SnapshotToken struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	HP     int    `json:"hp"`
	MaxHP  int    `json:"max_hp"`
	Hidden bool   `json:"hidden"` // true only ever served to GMs
}

// SnapshotFog is the fog polygon/coverage descriptor (opaque to I2;
// interpreted by Lane K; hidden zones omitted for players).
type SnapshotFog struct {
	Hidden bool   `json:"hidden"` // fail-closed default
	Shape  string `json:"shape"`  // Lane K geometry encoding
}

// SnapshotInitEntry is one initiative row.
type SnapshotInitEntry struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Order int    `json:"order"`
}
