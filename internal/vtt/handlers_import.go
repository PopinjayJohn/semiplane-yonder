// UVTT import HTTP route (amend A-VTT2 §4, D1): POST /vtt/{mapID}/import.
//
// Auth surface, in the frozen order (same discipline as opSpawn/opReset):
// login → preview-as 403 (W2) → double-submit CSRF → GM-only → CanViewMap.
// The body is the raw .uvtt/.dd2vtt/.df2vtt JSON (application/json) with an
// optional `grid_baked` field (D12); a 12MB cap answers 413 before any parse
// (larger files go through the CLI). The `?source=` query param carries the
// original filename for audit metadata only (sanitized to a base name,
// never a path).
//
// CanViewMap gates on an EXISTING sidecar (the map's ACL): a brand-new map
// id 404s here by design — create the sidecar page first (editor or the
// import-uvtt CLI), then import calibrates it. That keeps map creation a
// deliberate page-writing act rather than a side effect of a state post.
//
// On success: image + sidecar are written through the vault (atomic), live
// state is reseeded (ResetToSidecar + fog all-hidden, fail closed), the
// index is refreshed (OnAfterWrite), and one fog-update wakes open boards.

package vtt

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/semiplane/yonder/internal/plugins"
	"github.com/semiplane/yonder/internal/uploads"
)

// maxImportBody caps the UVTT import body (D1: 12MB HTTP, CLI uncapped).
const maxImportBody = 12 << 20

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
	if !CanViewMap(ctx, h.Store, v, mapID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if h.Vault == nil {
		http.Error(w, "vault is not configured", http.StatusInternalServerError)
		return
	}
	if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "application/json only", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxImportBody+1))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	if len(body) > maxImportBody {
		http.Error(w, "import body too large (12MB cap; use the import-uvtt CLI)", http.StatusRequestEntityTooLarge)
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
		ImageByteCap: uploads.MaxImageBytes,
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
			// The vault write is durable (truth); the index refresh failure
			// is retried by the watcher's next event. Log + continue.
			_ = err
		}
	}
	Publish(Event{Name: plugins.EventFogUpdate, Map: mapID})
	writeImportJSON(w, mapID, res)
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
		"ambient": res.Ambient, "grid_baked": res.GridBaked, "baked_lighting": res.BakedLight,
		"uvtt_source": res.Source,
	})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(b)
}
