package vtt

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/plugins"
	"github.com/semiplane/yonder/internal/secrets"
	"github.com/semiplane/yonder/internal/store"
)

// EffViewer maps a request viewer to the filtering identity: GM preview-as
// filters as the previewed user, never as GM (Phase 0c contract, same rule as
// plugins.ViewerOf). Guests stay nil.
func EffViewer(v *auth.Viewer) *auth.Viewer {
	if v == nil {
		return nil
	}
	if v.PreviewAs != "" {
		return &auth.Viewer{UserID: v.PreviewAs, OwnedSlugs: v.OwnedSlugs, Grants: v.Grants}
	}
	return v
}

// Sidecar loads the map sidecar page (index row). sql.ErrNoRows (or a nil
// store) means "no such map".
func Sidecar(ctx context.Context, st store.Store, mapID string) (*store.Page, error) {
	if st == nil {
		return nil, sql.ErrNoRows
	}
	return st.PageGet(ctx, SidecarPath(mapID))
}

// CanViewMap reports whether viewer may see the map at all: the sidecar
// page's own ACL (secret/owner/editable-by, nearest-ancestor rows already
// folded into the index row by the watcher). Guests (nil) never can — VTT
// requires login on every live surface (P08 red line).
func CanViewMap(ctx context.Context, st store.Store, viewer *auth.Viewer, mapID string) bool {
	if viewer == nil || viewer.UserID == "" {
		return false
	}
	page, err := Sidecar(ctx, st, mapID)
	if err != nil {
		return false
	}
	return secrets.CanViewPage(EffViewer(viewer), page.Secret, page.Path, page.Owner, page.EditableBy)
}

// CalibrationFor returns the sidecar calibration (defaults when the block is
// absent; the map must already be viewer-checked by the caller).
func CalibrationFor(ctx context.Context, st store.Store, mapID string) Calibration {
	page, err := Sidecar(ctx, st, mapID)
	if err != nil || page == nil {
		return Calibration{Grid: DefaultGrid(), FogDefault: "hidden"}
	}
	return ParseCalibration(page.Frontmatter[FrontmatterKey])
}

// SnapshotFor builds the per-viewer frozen StateSnapshot. Hidden tokens are
// dropped ENTIRELY for non-GMs (id, name, position, HP, character link —
// nothing leaks, not even existence). Character links on visible tokens are
// included only when the viewer may view the linked sheet. Fog geometry is
// board-visible to map viewers; a missing/corrupt mask fails closed to
// fully hidden for everyone including the GM.
func SnapshotFor(ctx context.Context, st store.Store, viewer *auth.Viewer, mapID string) (plugins.StateSnapshot, Calibration, error) {
	snap := plugins.StateSnapshot{Version: plugins.FragmentVersion, MapID: mapID}
	if !CanViewMap(ctx, st, viewer, mapID) {
		return snap, Calibration{}, sql.ErrNoRows
	}
	cal := CalibrationFor(ctx, st, mapID)
	live, err := LoadState(ctx, st, mapID)
	if err != nil {
		return snap, cal, err
	}
	eff := EffViewer(viewer)
	isGM := eff != nil && eff.IsGM
	fog := ResolveFog(live.FogMask, live.FogPresent, cal.Grid)
	snap.Fog = plugins.SnapshotFog{Hidden: fog.Hidden, Shape: fog.Shape}
	visible := map[string]plugins.SnapshotToken{}
	if !fog.Hidden {
		for _, t := range SortedTokens(live.Tokens) {
			if t.Hidden && !isGM {
				continue // hidden token: no id/name/position/HP/link leaks
			}
			// Hidden=true is only ever served to GMs (frozen contract).
			tok := plugins.SnapshotToken{
				ID: t.ID, Name: t.Name, X: t.X, Y: t.Y,
				HP: t.HP, MaxHP: t.MaxHP, Hidden: t.Hidden,
			}
			visible[t.ID] = tok
			snap.Tokens = append(snap.Tokens, tok)
		}
	}
	// Initiative follows token visibility: rows for unseen tokens are
	// dropped (their names would leak hidden-token existence otherwise).
	type row struct {
		id   string
		name string
		ord  int
	}
	var rows []row
	seen := map[string]bool{}
	for _, e := range live.Initiative {
		if seen[e.Token] {
			continue
		}
		seen[e.Token] = true
		tok, ok := visible[e.Token]
		if !ok {
			continue
		}
		rows = append(rows, row{id: e.Token, name: tok.Name, ord: e.Ord})
	}
	// Tokens without an initiative row still render (appended after, id order).
	for _, t := range snap.Tokens {
		if seen[t.ID] {
			continue
		}
		rows = append(rows, row{id: t.ID, name: t.Name, ord: 1 << 30})
	}
	for _, r := range rows {
		snap.Initiative = append(snap.Initiative, plugins.SnapshotInitEntry{ID: r.id, Name: r.name, Order: r.ord})
	}
	return snap, cal, nil
}

// CharacterLinkFor resolves a token's character link for one viewer: the
// vault-relative page, or "" when unset, unreadable, or unviewable (fail
// closed — secret sheet paths are content and never leak).
func CharacterLinkFor(ctx context.Context, st store.Store, viewer *auth.Viewer, character string) string {
	if character == "" || st == nil {
		return ""
	}
	eff := EffViewer(viewer)
	if eff == nil {
		return ""
	}
	if eff.IsGM {
		return character
	}
	page, err := st.PageGet(ctx, character)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ""
		}
		return ""
	}
	if !secrets.CanViewPage(eff, page.Secret, page.Path, page.Owner, page.EditableBy) {
		return ""
	}
	return character
}

// SnapshotJSON marshals a snapshot for the state endpoint.
func SnapshotJSON(snap plugins.StateSnapshot) ([]byte, error) {
	if snap.Tokens == nil {
		snap.Tokens = []plugins.SnapshotToken{}
	}
	if snap.Initiative == nil {
		snap.Initiative = []plugins.SnapshotInitEntry{}
	}
	return json.Marshal(snap)
}
