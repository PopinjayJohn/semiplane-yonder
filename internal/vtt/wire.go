package vtt

import (
	"encoding/json"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/plugins"
	"github.com/semiplane/yonder/internal/web"
)

// Event is one VTT broadcast (committed-write fan-out; the /events loop in
// Lane F1's read path delivers it per viewer through web.PublishVTT — the
// reverse import would cycle, so this package projects payloads and the web
// package pipes them).
type Event struct {
	Name string // frozen plugins.Event* name
	Map  string // VTT map scope ("" when global: recap)
	// Dice fields (dice-roll / dice-blind only):
	RollID, Actor, Notation string
	Total                   *int64
	Blind                   bool
}

// Publish projects ev to wire payloads and enqueues it for /events
// subscribers. Map-scoped events carry only the map id (subscribers re-read
// + re-filter server-side before rendering — frozen fragment.go rule), so
// Open == Redacted for them. Dice events pre-render both variants; the loop
// serves Open to full GMs and Redacted to everyone else (blind routing).
func Publish(ev Event) {
	open, redacted := wirePayloads(ev)
	web.PublishVTT(web.VTTWireEvent{
		Name: ev.Name, MapID: ev.Map, PagePath: SidecarPath(ev.Map),
		Open: open, Redacted: redacted,
	})
}

func wirePayloads(ev Event) (open, redacted string) {
	switch ev.Name {
	case plugins.EventDiceRoll, plugins.EventDiceBlind:
		full := map[string]any{
			"roll_id": ev.RollID, "actor": ev.Actor, "notation": ev.Notation,
			"blind": ev.Blind, "total": ev.Total, "redacted": false, "map_id": ev.Map,
		}
		b, _ := json.Marshal(full)
		open = string(b)
		if ev.Blind {
			red := map[string]any{
				"roll_id": ev.RollID, "actor": ev.Actor, "notation": ev.Notation,
				"blind": true, "total": nil, "redacted": true, "map_id": ev.Map,
			}
			rb, _ := json.Marshal(red)
			redacted = string(rb)
		} else {
			redacted = open
		}
		return open, redacted
	default:
		b, _ := json.Marshal(map[string]string{"map_id": ev.Map})
		return string(b), string(b)
	}
}

// ProjectForViewer renders the data payload one SSE connection would receive
// for ev (pure helper for tests: mirrors the loop's GM-pick rule).
func ProjectForViewer(ev Event, viewer *auth.Viewer) (name, data string, ok bool) {
	switch ev.Name {
	case plugins.EventDiceRoll, plugins.EventDiceBlind,
		plugins.EventTokenMove, plugins.EventTokenHP, plugins.EventFogUpdate,
		plugins.EventInitiativeUpdate, plugins.EventEncounterSpawn,
		plugins.EventSessionRecap:
	default:
		return "", "", false
	}
	if viewer == nil || viewer.UserID == "" {
		return "", "", false
	}
	name = ev.Name
	if ev.Name == plugins.EventDiceRoll && ev.Blind {
		name = plugins.EventDiceBlind
	}
	open, redacted := wirePayloads(ev)
	if viewer.IsGM && viewer.PreviewAs == "" {
		return name, open, true
	}
	return name, redacted, true
}
