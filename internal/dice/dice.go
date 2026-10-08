package dice

import (
	"context"

	"github.com/semiplane/yonder/internal/ruleset"
)

// Transport defines the dice transport convention for rolling, logging, and replay.
// Core P12 = generic dice engine + transport (log, blind routing, broadcast, replay).
// Maxima: 100 dice / 1000 faces. Bases set lower values.
// crypto/rand used for entropy.
// Replay: stored values replayed with per-viewer re-auth.

// Roller is the generic dice roller interface.
// Bases supply dice programs; core executes them.
type Roller interface {
	// Roll executes a dice notation and returns the result.
	// notation: standard dice notation (e.g., "2d6+3", "1d20adv", "4d6kh3")
	Roll(ctx context.Context, notation string) (*RollResult, error)
	// RollWithSeed executes a dice notation with a predetermined seed (for replay).
	RollWithSeed(ctx context.Context, notation string, seed []byte) (*RollResult, error)
}

// RollResult contains the result of a dice roll.
type RollResult struct {
	Notation  string
	Total     int64
	Dice      []DieResult
	Symbols   []SymbolFace // populated for symbolic dice only
	Modifiers []AppliedModifier
	Seed      []byte // for replay verification
	Timestamp int64
	RollID    string // unique ID for this roll
	Blind     bool   // blind roll (GM only)
	ActorID   string
	IntentID  string
	// Discarded holds the losing attempt of an advantage/disadvantage
	// roll-twice pair (nil otherwise). Audit only, never added to Total.
	Discarded *RollResult
}

// SymbolFace is one rolled symbolic face.
type SymbolFace struct {
	Symbol  string // face name from the base-declared SymbolTable
	Dropped bool   // excluded by keep/drop
}

// DieResult represents a single die result.
type DieResult struct {
	Faces    int
	Value    int
	Dropped  bool // for keep highest/lowest
	Exploded bool
	Rerolled bool
	Original int // original value if rerolled
}

// AppliedModifier represents a modifier applied to the roll.
type AppliedModifier struct {
	Source string
	Type   string
	Value  int64
	Reason string
}

// LogEntry represents a dice log entry for replay/audit.
type LogEntry struct {
	RollID     string
	ActorID    string
	IntentID   string
	Notation   string
	Seed       []byte
	Result     *RollResult
	Modifiers  *ruleset.Modifiers // snapshot of modifiers at roll time
	ViewerHash string             // hash of viewer context for re-auth on replay
	Timestamp  int64
	Blind      bool
	Broadcast  bool
	// Envelope is the stored {intent, actor, targets, tool, context}
	// envelope, serialized to envelope_json at Save. Not part of the
	// frozen transport surface; readers ignore unknown keys.
	Envelope map[string]any
}

// TransportConfig configures the dice transport.
type TransportConfig struct {
	MaxDice      int // default 100
	MaxFaces     int // default 1000
	MaxRolls     int // per second per actor
	LogRetention int // days
}

// DefaultTransportConfig returns sensible defaults.
func DefaultTransportConfig() TransportConfig {
	return TransportConfig{
		MaxDice:      100,
		MaxFaces:     1000,
		MaxRolls:     10,
		LogRetention: 30,
	}
}

// TransportService, Roll, and Replay are implemented in transport.go.

// RollRequest represents a roll request through the transport.
type RollRequest struct {
	Notation  string
	ActorID   string
	IntentID  string
	Modifiers *ruleset.Modifiers
	Blind     bool
	Broadcast bool
	Seed      []byte // optional, for deterministic rolls
	Metadata  map[string]any
	// Metadata conventions (all optional):
	//   "scope"   Applies scope for numeric bonuses (default "all";
	//             Effective.BonusFor(scope) is added to the total).
	//   "targets" []string, "tool" string, "context" map — stored into
	//             the log envelope {intent, actor, targets, tool, context}.
	//   "viewer"  routing key for blind rolls (default "actor:"+ActorID).
	//   "ip"      guest bucket key for rate limiting (ActorID preferred).
}

// RollResponse represents a roll response.
type RollResponse struct {
	Result    *RollResult
	LogID     string
	Broadcast []BroadcastTarget
}

// BroadcastTarget represents a broadcast recipient.
type BroadcastTarget struct {
	ViewerHash string
	RollResult *RollResult // filtered for this viewer
}

// LogStore defines the interface for dice log persistence.
type LogStore interface {
	// Save persists a log entry.
	Save(ctx context.Context, entry *LogEntry) error
	// Get retrieves a log entry by ID.
	Get(ctx context.Context, rollID string) (*LogEntry, error)
	// List returns recent log entries for an actor.
	List(ctx context.Context, actorID string, limit int) ([]*LogEntry, error)
	// PurgeOld removes entries older than retention.
	PurgeOld(ctx context.Context, days int) error
}
