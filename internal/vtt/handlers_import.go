// UVTT import HTTP route (amend A-VTT2 §4, R1 review
// docs/plan/r1-http-cap-dos-review.md): POST /vtt/{mapID}/import.
//
// Auth surface, in the frozen order (same discipline as opSpawn/opReset):
// login → preview-as 403 (W2) → double-submit CSRF → GM-only → CanViewMap.
// The body is the raw .uvtt/.dd2vtt/.df2vtt JSON (application/json) with
// optional `grid_baked` (D12) and `objects_block_sight` (R6) fields; a 55MB
// cap answers 413 before any parse (larger files go through the CLI). Past
// the GM gate, the R1 admission guard admits 1 import in flight + 3 per 5
// minutes per GM (429 past it), so a compromised session or a retry loop
// cannot stack full rescans while each holds ~200MB transient. The
// `?source=` query param carries the
// original filename for audit metadata only (sanitized to a base name,
// never a path).
//
// CanViewMap gates an EXISTING sidecar (the map's ACL). A brand-new map id
// is not an error: a GM may create a map by importing into a fresh id — the
// import writes the sidecar page itself (title + vtt-map calibration +
// generated body), so creation stays a deliberate calibration act rather
// than a side effect of browsing. A fresh sidecar inside a secret maps/
// tree is stamped secret:true (fail closed).
//
// On success: image + sidecar are written through the vault (atomic), live
// state is reseeded (ResetToSidecar + fog all-hidden, fail closed), the
// index is refreshed (OnAfterWrite), and one fog-update wakes open boards.

package vtt

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/semiplane/yonder/internal/plugins"
	"github.com/semiplane/yonder/internal/store"
)

// maxImportBody caps the UVTT import body (R1: 55MB HTTP, CLI uncapped).
// The paired HTTP image cap is maxHTTPImportImageBytes (40MB, the largest
// image encodable in a 55MB body) — the body cap alone would admit no real
// 5–50MB export, since bodies over ~8MB always failed the old 5MB image
// cap after paying parse + decode.
const maxImportBody = 55 << 20

// Import handles POST /vtt/{mapID}/import (GM-only, login + CanViewMap).
func (h *Handlers) Import(w http.ResponseWriter, r *http.Request) {
	mapID, ok := MapIDFromPath(r.URL.Path, "/vtt/", "/import")
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	v := h.viewer(r)
	if v == nil || v.UserID == "" {
		http.Error(w, "login required", http.StatusUnauthorized)
		return
	}
	if v.PreviewAs != "" {
		http.Error(w, "GM preview is read-only — exit preview to import", http.StatusForbidden)
		return
	}
	if !h.checkCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}
	if !isGM(v) {
		http.Error(w, "not allowed", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	if h.Vault == nil {
		http.Error(w, "vault is not configured", http.StatusInternalServerError)
		return
	}
	if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "application/json only", http.StatusUnsupportedMediaType)
		return
	}
	// Map resolution: an existing sidecar is gated by CanViewMap (the map's
	// ACL; GMs pass, and non-GMs were already rejected above). A MISSING
	// sidecar is not a 404 — a GM may create a map by importing into a fresh
	// id (the import writes the sidecar itself, so map creation is a
	// deliberate calibration act, not an accidental page write). A fresh
	// sidecar fails closed on a secret maps/ tree (stamped by the caller).
	sidecar, serr := Sidecar(ctx, h.Store, mapID)
	switch {
	case serr == nil:
		if !CanViewMap(ctx, h.Store, v, mapID) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
	case errors.Is(serr, sql.ErrNoRows):
		// new map: allowed for the GM (already gated above)
	default:
		http.Error(w, "cannot look up map", http.StatusInternalServerError)
		return
	}
	freshSecret := sidecar == nil && mapDirSecret(ctx, h.Store)
	// Known-oversize bodies 413 before spending admission budget (chunked
	// uploads report -1 and fall through to the post-read check).
	if r.ContentLength > maxImportBody {
		http.Error(w, "import body too large (55MB cap; use the import-uvtt CLI)", http.StatusRequestEntityTooLarge)
		return
	}
	// R1 admission guard: past the auth gates (rejected requests never
	// consume budget) and before the first body byte is read (rejected
	// requests never allocate). Every acquired slot releases via defer.
	lim := h.importLimiter()
	if err := lim.Acquire(v.UserID); err != nil {
		if errors.Is(err, ErrImportBusy) {
			http.Error(w, "another import is already running, try again shortly", http.StatusTooManyRequests)
		} else {
			http.Error(w, "too many imports, try again later", http.StatusTooManyRequests)
		}
		return
	}
	defer lim.Release()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxImportBody+1))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	if len(body) > maxImportBody {
		http.Error(w, "import body too large (55MB cap; use the import-uvtt CLI)", http.StatusRequestEntityTooLarge)
		return
	}
	source := strings.TrimSpace(r.URL.Query().Get("source"))
	if source == "" {
		source = "upload.uvtt"
	}
	m, err := ParseWithOpts(body, source, ImportOpts{})
	if err != nil {
		http.Error(w, importErrorMsg(err), http.StatusUnprocessableEntity)
		return
	}
	res, err := ApplyImport(ctx, ImportRequest{
		Vault: h.Vault, MapID: mapID, Map: m,
		ImageByteCap: maxHTTPImportImageBytes,
		FreshSecret:  freshSecret,
	})
	if err != nil {
		http.Error(w, importErrorMsg(err), http.StatusUnprocessableEntity)
		return
	}
	// Live state: reseed tokens/fog/doors from the freshly written sidecar,
	// then force fog all-hidden (fail closed — a hand-edited fog-default:
	// revealed must not open a just-imported board).
	cal := CalibrationOf(m, res.AssetPath)
	if err := ResetToSidecar(ctx, h.Store, mapID, cal); err != nil {
		http.Error(w, "import wrote calibration but could not reseed live state", http.StatusInternalServerError)
		return
	}
	if err := SetFog(ctx, h.Store, mapID, EncodeMask(cal.Grid, []Rect{{X: 0, Y: 0, W: cal.Grid.Cols, H: cal.Grid.Rows}})); err != nil {
		http.Error(w, "import wrote calibration but could not set fog", http.StatusInternalServerError)
		return
	}
	if h.OnAfterWrite != nil {
		if err := h.OnAfterWrite(); err != nil {
			// The vault write is durable (truth) and the watcher's next event
			// retries the refresh — but a swallowed error here is an
			// undebuggable flake (a stale index reads as a missing map), so
			// log it loud and continue.
			slog.Error("import: index refresh after import failed (watcher will retry)", "map", mapID, "err", err)
		}
	}
	Publish(Event{Name: plugins.EventFogUpdate, Map: mapID})
	writeImportJSON(w, mapID, res)
}

// mapDirSecret reports whether the maps/ directory itself is a secret tree
// (maps.md or maps/index.md secret). A freshly created sidecar inside a
// secret tree is stamped secret:true (fail closed) — otherwise the import
// would create an open page under a hidden folder.
func mapDirSecret(ctx context.Context, st store.Store) bool {
	if st == nil {
		return false
	}
	for _, cand := range []string{SidecarDir + ".md", SidecarDir + "/index.md"} {
		p, err := st.PageGet(ctx, cand)
		if err == nil && p != nil && p.Secret {
			return true
		}
	}
	return false
}

// importErrorMsg renders a rejection reason with no vault content: typed
// parser errors carry their own reason (map size / image size), everything
// else degrades to the generic corrupt message.
func importErrorMsg(err error) string {
	var sizeErr *MapSizeError
	if errors.As(err, &sizeErr) {
		return sizeErr.Error()
	}
	var imgErr *ImageSizeError
	if errors.As(err, &imgErr) {
		return imgErr.Error()
	}
	if errors.Is(err, ErrCorrupt) {
		return "uvtt: corrupt file"
	}
	return "import rejected: " + err.Error()
}

// writeImportJSON answers the import summary (calibration + counts only —
// all of it board-visible to the GM who just wrote it).
func writeImportJSON(w http.ResponseWriter, mapID string, res *ImportResult) {
	b, _ := json.Marshal(map[string]any{
		"ok": true, "map_id": mapID,
		"sidecar": res.SidecarPath, "asset": res.AssetPath, "image_format": res.ImageFormat,
		"grid":  map[string]any{"cols": res.Cols, "rows": res.Rows, "cell": res.Cell},
		"walls": res.Walls, "objects": res.Objects, "portals": res.Portals, "lights": res.Lights,
		"ambient": res.Ambient, "grid_baked": res.GridBaked, "objects_block_sight": res.ObjectsBlockSight,
		"baked_lighting": res.BakedLight, "uvtt_source": res.Source,
	})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(b)
}
