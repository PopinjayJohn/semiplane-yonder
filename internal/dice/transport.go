package dice

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"
)

// TransportService implements log, blind routing, broadcast hints, and
// replay over a Roller + LogStore (P12). Maxima: absolute 100 dice / 1000
// faces; TransportConfig may set lower base-declared values. Rate limit:
// MaxRolls per second per actor (authenticated ActorID, else Metadata "ip",
// else "anon"); guests share the per-IP bucket.
//
// Replay reads stored values and never re-rolls; every replayed event is
// re-authorized against the presenting viewer (default policy below, or a
// custom Authorize func wired by the web layer in I2).
type TransportService struct {
	roller  Roller
	store   LogStore
	config  TransportConfig
	symbols SymbolTable

	// Authorize re-checks a stored entry against the presenting viewer on
	// replay. Nil = DefaultAuthorize.
	Authorize func(ctx context.Context, entry *LogEntry, viewer string) error

	mu      sync.Mutex
	windows map[string][]int64 // rate-limit windows (unix nanos)
	now     func() time.Time   // test seam
}

// NewTransportService creates a new transport service.
func NewTransportService(roller Roller, store LogStore, config TransportConfig) *TransportService {
	if config.MaxDice <= 0 {
		config.MaxDice = MaxDice
	}
	if config.MaxFaces <= 0 {
		config.MaxFaces = MaxFaces
	}
	if config.MaxRolls <= 0 {
		config.MaxRolls = 10
	}
	return &TransportService{
		roller:  roller,
		store:   store,
		config:  config,
		windows: map[string][]int64{},
		now:     time.Now,
	}
}

// specRoller is an optional Roller extension: roll an already-parsed spec.
// The built-in roller implements it; the transport prefers it so the spec
// parsed here (transport symbol table + base-maxima check) is what rolls,
// instead of the roller re-parsing with its own table.
type specRoller interface {
	RollSpec(ctx context.Context, spec *Spec, notation string, seed []byte) (*RollResult, error)
}

// SetSymbols installs the base-declared symbolic dice table.
func (t *TransportService) SetSymbols(symbols SymbolTable) {
	t.symbols = symbols
}

// Roll executes a roll through the transport: parse, base-maxima check,
// rate limit, advantage fan-out, modifier application, log, routing.
func (t *TransportService) Roll(ctx context.Context, req RollRequest) (*RollResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.ActorID == "" {
		return nil, fmt.Errorf("dice: actor required")
	}
	spec, err := ParseNotation(req.Notation, t.symbols)
	if err != nil {
		return nil, err
	}
	if err := spec.WithinConfig(t.config.MaxDice, t.config.MaxFaces); err != nil {
		return nil, err
	}
	if err := t.checkRate(req); err != nil {
		return nil, err
	}

	adv := spec.Adv
	if req.Modifiers != nil && req.Modifiers.Effective.Advantage != 0 {
		// Hook-composed advantage overrides the notation suffix only when
		// the notation carries none; a suffix the player typed explicitly
		// (adv/dis) wins over silent hooks… no: hooks reflect the world
		// (flanking), the suffix reflects the player's tray click. Both set
		// is a conflict — fail closed rather than guess.
		if adv != 0 && req.Modifiers.Effective.Advantage != adv {
			return nil, fmt.Errorf("dice: conflicting advantage: notation vs modifiers")
		}
		if adv == 0 {
			adv = req.Modifiers.Effective.Advantage
		}
	}

	var res *RollResult
	if adv != 0 {
		res, err = t.rollAdvantage(ctx, spec, req, adv)
	} else {
		res, err = t.rolled(ctx, spec, req.Notation, req.Seed)
	}
	if err != nil {
		return nil, err
	}
	res.ActorID = req.ActorID
	res.IntentID = req.IntentID
	res.Blind = req.Blind

	// Numeric hooks: pinned "all" + request scope bonuses add to the total.
	scope := "all"
	if s, _ := req.Metadata["scope"].(string); s != "" {
		scope = s
	}
	var applied []AppliedModifier
	if req.Modifiers != nil {
		bonus := req.Modifiers.Effective.BonusFor(scope)
		if bonus != 0 {
			total, err := checkedAdd64(res.Total, bonus)
			if err != nil {
				return nil, err
			}
			res.Total = total
		}
		for _, m := range req.Modifiers.NumericModifiers {
			if m.Applies == scope || m.Applies == "all" || scope == "all" {
				applied = append(applied, AppliedModifier{
					Source: m.Source, Type: m.Type, Value: m.Value, Reason: m.Reason,
				})
			}
		}
		for _, m := range req.Modifiers.DiceModifiers {
			applied = append(applied, AppliedModifier{
				Source: m.Source, Type: m.Type, Reason: m.Reason + ": " + m.DiceSpec,
			})
		}
	}
	res.Modifiers = applied
	res.Timestamp = t.now().Unix()

	viewer := "actor:" + req.ActorID
	if v, _ := req.Metadata["viewer"].(string); v != "" {
		viewer = v
	}
	entry := &LogEntry{
		RollID:    res.RollID,
		ActorID:   req.ActorID,
		IntentID:  req.IntentID,
		Notation:  req.Notation,
		Seed:      append([]byte(nil), res.Seed...),
		Result:    res,
		Modifiers: req.Modifiers,
		Timestamp: res.Timestamp,
		Blind:     req.Blind,
		Broadcast: req.Broadcast,
		Envelope:  envelopeJSON(req, viewer),
	}
	entry.ViewerHash = viewerHash(viewer)
	if err := t.store.Save(ctx, entry); err != nil {
		return nil, err
	}
	// The store assigns the row id; propagate it so replay addresses match.
	res.RollID = entry.RollID
	entry.Result = res

	resp := &RollResponse{Result: res, LogID: entry.RollID}
	switch {
	case req.Blind:
		// GM-blind routing: only the GM role receives the full result.
		// The SSE layer (I2) fans out; anyone else gets RedactedCopy.
		resp.Broadcast = []BroadcastTarget{{ViewerHash: "role:gm", RollResult: res}}
	case req.Broadcast:
		resp.Broadcast = []BroadcastTarget{{ViewerHash: "party", RollResult: res}}
	}
	return resp, nil
}

// rollAdvantage rolls twice (split seeds when seeded, fresh entropy
// otherwise) and keeps the extreme total. Generic over any spec.
func (t *TransportService) rollAdvantage(ctx context.Context, spec *Spec, req RollRequest, adv int) (*RollResult, error) {
	s1, s2 := splitTransportSeed(req.Seed)
	first, err := t.rolled(ctx, spec, req.Notation, s1)
	if err != nil {
		return nil, err
	}
	second, err := t.rolled(ctx, spec, req.Notation, s2)
	if err != nil {
		return nil, err
	}
	winner, loser := first, second
	if adv == 1 && second.Total > first.Total || adv == -1 && second.Total < first.Total {
		winner, loser = second, first
	}
	winner.Discarded = loser
	return winner, nil
}

func (t *TransportService) rolled(ctx context.Context, spec *Spec, notation string, seed []byte) (*RollResult, error) {
	// Prefer the already-parsed spec (same symbols + maxima the transport
	// checked). Rollers without RollSpec re-parse; symbols must then be
	// installed on the roller itself (NewRollerWithSymbols).
	if sr, ok := t.roller.(specRoller); ok {
		return sr.RollSpec(ctx, spec, notation, seed)
	}
	if seed == nil {
		return t.roller.Roll(ctx, notation)
	}
	return t.roller.RollWithSeed(ctx, notation, seed)
}

func splitTransportSeed(seed []byte) ([]byte, []byte) {
	if seed == nil {
		return nil, nil
	}
	h1 := sha256.Sum256(append(append([]byte(nil), seed...), 0x01))
	h2 := sha256.Sum256(append(append([]byte(nil), seed...), 0x02))
	return h1[:], h2[:]
}

// Replay returns the stored result for rollID (never re-rolls) after
// re-authorizing the presenting viewer against the current entry.
func (t *TransportService) Replay(ctx context.Context, rollID string, viewerHash string) (*RollResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if rollID == "" {
		return nil, fmt.Errorf("dice: roll id required")
	}
	if viewerHash == "" {
		return nil, fmt.Errorf("dice: viewer required for replay")
	}
	entry, err := t.store.Get(ctx, rollID)
	if err != nil {
		return nil, err
	}
	if entry == nil || entry.Result == nil {
		return nil, fmt.Errorf("dice: roll %q not found", rollID)
	}
	auth := t.Authorize
	if auth == nil {
		auth = DefaultAuthorize
	}
	if err := auth(ctx, entry, viewerHash); err != nil {
		return nil, err
	}
	return entry.Result, nil
}

// DefaultAuthorize re-checks a stored roll against the presenting viewer:
// open rolls replay for any identified viewer; blind rolls replay only for
// the original viewer key or the GM role. Callers needing real ACLs (party
// membership, revoked sessions) set TransportService.Authorize.
func DefaultAuthorize(_ context.Context, entry *LogEntry, viewer string) error {
	if !entry.Blind {
		return nil
	}
	if viewer == "role:gm" || viewerHash(viewer) == entry.ViewerHash {
		return nil
	}
	return fmt.Errorf("dice: blind roll not visible to this viewer")
}

func viewerHash(viewer string) string {
	sum := sha256.Sum256([]byte(viewer))
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 16)
	for i := 0; i < 8; i++ {
		out[2*i] = hexdigits[sum[i]>>4]
		out[2*i+1] = hexdigits[sum[i]&0x0f]
	}
	return string(out)
}

// RedactedCopy returns a result with dice values and totals stripped for
// viewers outside blind routing. The notation shape (count/faces) stays so
// clients can render "a roll happened" without learning anything.
func RedactedCopy(res *RollResult) *RollResult {
	if res == nil {
		return nil
	}
	cp := *res
	cp.Total = 0
	cp.Seed = nil
	cp.Dice = make([]DieResult, len(res.Dice))
	for i, d := range res.Dice {
		cp.Dice[i] = DieResult{Faces: d.Faces, Dropped: d.Dropped}
	}
	cp.Symbols = nil
	cp.Discarded = nil
	cp.Blind = true
	return &cp
}

func (t *TransportService) checkRate(req RollRequest) error {
	key := req.ActorID
	if key == "" {
		if ip, _ := req.Metadata["ip"].(string); ip != "" {
			key = "ip:" + ip
		} else {
			key = "anon"
		}
	}
	now := t.now().UnixNano()
	window := now - int64(time.Second)
	t.mu.Lock()
	defer t.mu.Unlock()
	kept := t.windows[key][:0]
	for _, ts := range t.windows[key] {
		if ts > window {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= t.config.MaxRolls {
		return fmt.Errorf("dice: rate limit exceeded (%d rolls/sec)", t.config.MaxRolls)
	}
	t.windows[key] = append(kept, now)
	return nil
}

// envelopeJSON builds the stored {intent, actor, targets, tool, context}
// envelope from request metadata. Unknown metadata keys are ignored. The
// resolved viewer routing key rides along as "viewer" (outside the frozen
// five) so replay can re-authorize the original viewer without a schema
// change; readers ignore unknown keys.
func envelopeJSON(req RollRequest, viewer string) map[string]any {
	env := map[string]any{
		"intent": req.IntentID,
		"actor":  req.ActorID,
	}
	if v, ok := req.Metadata["targets"]; ok {
		env["targets"] = v
	}
	if v, ok := req.Metadata["tool"]; ok {
		env["tool"] = v
	}
	if v, ok := req.Metadata["context"]; ok {
		env["context"] = v
	}
	if viewer != "" {
		env["viewer"] = viewer
	}
	return env
}
