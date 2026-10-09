package web

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"
	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/campaign"
	"github.com/semiplane/yonder/internal/markdown"
	"github.com/semiplane/yonder/internal/secrets"
	"github.com/semiplane/yonder/internal/store"
	"github.com/semiplane/yonder/web/templates"
)

// ReadHandlers contains all read-path HTTP handlers.
// Owned by Lane F1 (read path). Consumes markdown.Parse, Store, secrets.Filter.
// Touches no write handlers.
type ReadHandlers struct {
	Store        store.Store
	SlotRegistry *SlotRegistry
	Slots        SlotProvider // gate G3: enabled-aware slot source (serve injects the plugin registry); nil = raw-registry test fallback
	SessionStore auth.SessionStore
	UserStore    auth.UserStore     // for dashboard user management
	Vault        VaultWriter        // for dashboard campaign save
	VaultRoot    string             // vault dir for the GM-only zip export (serve sets it; empty = export unavailable)
	Campaign     *campaign.Campaign // campaign.yaml data (name, landing-page, etc.)
}

// csrfTokenForForm returns the session's CSRF token for embedding in forms
// ("" when sessions are unwired or the request carries no session).
func (h *ReadHandlers) csrfTokenForForm(r *http.Request) string {
	if h.SessionStore == nil {
		return ""
	}
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return ""
	}
	sessID := c.Value
	if i := strings.IndexByte(sessID, '|'); i >= 0 {
		sessID = sessID[:i]
	}
	if !auth.ValidateSessionID(sessID) {
		return ""
	}
	sess, err := h.SessionStore.Get(r.Context(), sessID)
	if err != nil || sess == nil {
		return ""
	}
	return sess.CSRFToken
}

// csrfOK validates the CSRF token from the request (form field or header)
// against the session's token. Returns true if valid or if SessionStore is nil.
func (h *ReadHandlers) csrfOK(r *http.Request) bool {
	if h.SessionStore == nil {
		return true
	}
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return false
	}
	sessID := c.Value
	if i := strings.IndexByte(sessID, '|'); i >= 0 {
		sessID = sessID[:i]
	}
	if !auth.ValidateSessionID(sessID) {
		return false
	}
	sess, err := h.SessionStore.Get(r.Context(), sessID)
	if err != nil || sess == nil {
		return false
	}
	return auth.ValidateCSRFToken(sess.CSRFToken, auth.CSRFTokenFromRequest(r))
}

// RegisterRoutes registers read-path routes with the registry.
func (h *ReadHandlers) RegisterRoutes(reg *RouteRegistry) {
	reg.Register(Route{
		Method:       http.MethodGet,
		Path:         RouteHealthz,
		Handler:      h.Healthz,
		ReadOnly:     true,
		AuthRequired: false,
	})
	reg.Register(Route{
		Method:       http.MethodGet,
		Path:         RouteVersion,
		Handler:      h.Version,
		ReadOnly:     true,
		AuthRequired: false,
	})
	reg.Register(Route{
		Method:         http.MethodGet,
		Path:           RouteIndex,
		Handler:        h.Index,
		ReadOnly:       true,
		AuthRequired:   false,
		SecretFiltered: true,
	})

	reg.Register(Route{
		Method:         http.MethodGet,
		Path:           RoutePageView,
		Handler:        h.PageView,
		ReadOnly:       true,
		AuthRequired:   false, // guest readable, secrets filtered in handler
		SecretFiltered: true,
	})
	reg.Register(Route{
		Method:         http.MethodGet,
		Path:           RouteSearch,
		Handler:        h.Search,
		ReadOnly:       true,
		AuthRequired:   false,
		SecretFiltered: true,
	})
	reg.Register(Route{
		Method:         http.MethodGet,
		Path:           RouteGraph,
		Handler:        h.Graph,
		ReadOnly:       true,
		AuthRequired:   false,
		SecretFiltered: true,
	})
	reg.Register(Route{
		Method:         http.MethodGet,
		Path:           RouteAutocomplete,
		Handler:        h.Autocomplete,
		ReadOnly:       true,
		AuthRequired:   false,
		SecretFiltered: true,
	})
	reg.Register(Route{
		Method:         http.MethodGet,
		Path:           RouteAssets,
		Handler:        h.Assets,
		ReadOnly:       true,
		AuthRequired:   false,
		SecretFiltered: true,
	})
	reg.Register(Route{
		Method:         http.MethodGet,
		Path:           RouteSSE,
		Handler:        h.SSE,
		ReadOnly:       true,
		AuthRequired:   true,
		SecretFiltered: true,
	})
	reg.Register(Route{
		Method:         http.MethodPost,
		Path:           RouteSSE,
		Handler:        h.SSE, // same handler: POST = GM flip broadcast
		ReadOnly:       false,
		AuthRequired:   true,
		SecretFiltered: true,
	})
	reg.Register(Route{
		Method:       http.MethodGet,
		Path:         RouteMe,
		Handler:      h.Me,
		ReadOnly:     true,
		AuthRequired: true,
	})
	reg.Register(Route{
		Method:       http.MethodGet,
		Path:         RouteDashboard,
		Handler:      h.Dashboard,
		ReadOnly:     true,
		AuthRequired: true,
		GMOnly:       true,
	})
	reg.Register(Route{
		Method:       http.MethodGet,
		Path:         RouteDashboardExport,
		Handler:      h.VaultZip,
		ReadOnly:     true,
		AuthRequired: true,
		GMOnly:       true,
	})
	reg.Register(Route{
		Method:       http.MethodPost,
		Path:         RouteDashboard,
		Handler:      h.DashboardSave,
		ReadOnly:     false,
		AuthRequired: true,
		GMOnly:       true,
	})
	reg.Register(Route{
		Method:       http.MethodPost,
		Path:         "/dashboard/user/create",
		Handler:      h.DashboardUserCreate,
		ReadOnly:     false,
		AuthRequired: true,
		GMOnly:       true,
	})
	reg.Register(Route{
		Method:       http.MethodPost,
		Path:         "/dashboard/user/reset-password",
		Handler:      h.DashboardUserResetPassword,
		ReadOnly:     false,
		AuthRequired: true,
		GMOnly:       true,
	})
	reg.Register(Route{
		Method:       http.MethodPost,
		Path:         "/dashboard/user/revoke-sessions",
		Handler:      h.DashboardUserRevokeSessions,
		ReadOnly:     false,
		AuthRequired: true,
		GMOnly:       true,
	})
	reg.Register(Route{
		Method:       http.MethodPost,
		Path:         "/dashboard/user/make-gm",
		Handler:      h.DashboardUserMakeGM,
		ReadOnly:     false,
		AuthRequired: true,
		GMOnly:       true,
	})
	reg.Register(Route{
		Method:       http.MethodPost,
		Path:         "/dashboard/user/remove-gm",
		Handler:      h.DashboardUserRemoveGM,
		ReadOnly:     false,
		AuthRequired: true,
		GMOnly:       true,
	})
	reg.Register(Route{
		Method:       http.MethodGet,
		Path:         RouteVTT,
		Handler:      h.VTT,
		ReadOnly:     true,
		AuthRequired: true,
	})
}

// Version is the binary version reported by /version (E1 stamps it via
// ldflags; "dev" until then). Version endpoint exposes the version ONLY.
var Version = "dev"

// VaultDir names the vault root for ACL-checked asset serving. Set by wiring
// (Phase 2; E1's runServe passes its --vault). Empty = assets unavailable.
var VaultDir = ""

// DemoAuth gates the `?as=` demo identity tier (B3 backlog, P11 follow-up).
// True (default) preserves the shipped behavior: `?as=gm` / `?as=<name>`
// resolve to demo viewers when no session is present, so `make dev`,
// serve-smoke, and axe keep working with zero login surface. False retires
// the tier entirely: `?as=` is ignored on every path and only session
// cookies authenticate. Serve sets this from `--demo-auth` (default true);
// tests flip it per-case and restore. preview_as is NOT separately gated:
// it only ever activates on a GM viewer, which under false is a real
// session GM (the Phase-0c bannered+logged impersonation contract).
var DemoAuth = true

// demoUsers are hardcoded stand-ins for auth (real auth is P11, Lane C).
// `?as=gm` is the GM; any other `?as=<name>` is that player; absent = guest.
// Ownership is resolved dynamically from page frontmatter (owner /
// editable-by), so no per-page hardcoding is needed.
var demoUsers = map[string]*auth.Viewer{
	"gm":   {UserID: "gm", IsGM: true},
	"mira": {UserID: "mira"},
	"bram": {UserID: "bram"},
	"cass": {UserID: "cass"},
}

// viewerFromRequest resolves the effective viewer: real auth context first
// (P11 middleware, via auth.ViewerFromContext), hardcoded demo users second.
// GM-only `preview_as` impersonation filters as the previewed user, renders a
// persistent banner, and is logged (Phase 0c contract).
//
// ViewerForRequest is the exported gate-G3 seam for serve wiring: the serve
// middleware injects this into request contexts so session-only handlers
// (I1 wizard/sheets, F2 writes) resolve the same demo identity live. There
// is no HTTP login surface in v1; `?as=` is the de-facto live-server
// identity tier (M1 smoke precedent) while DemoAuth holds. State-changing
// routes still require a session-backed CSRF token — identity alone never
// authorizes a write.
func ViewerForRequest(r *http.Request) *auth.Viewer {
	return viewerFromRequest(r)
}
func viewerFromRequest(r *http.Request) *auth.Viewer {
	if v, ok := auth.ViewerFromContext(r.Context()); ok && v != nil {
		return v
	}
	if !DemoAuth {
		return nil // demo tier retired: ?as= ignored, sessions only
	}
	as := strings.TrimSpace(r.URL.Query().Get("as"))
	if as == "" || strings.EqualFold(as, "guest") {
		return nil // guest (also stands in for revoked sessions)
	}
	var v *auth.Viewer
	if u, ok := demoUsers[strings.ToLower(as)]; ok {
		cp := *u
		v = &cp
	} else {
		v = &auth.Viewer{UserID: as}
	}
	if preview := strings.TrimSpace(r.URL.Query().Get("preview_as")); preview != "" && v.IsGM {
		v.PreviewAs = preview
		slog.Info("gm preview", "gm", v.UserID, "as", preview, "path", r.URL.Path)
	}
	return v
}

// asParam preserves the demo identity across links. Empty for guests, for
// real-auth requests (no `as` query present), and whenever the demo tier is
// retired (DemoAuth=false) so sessions-only deployments never propagate it.
func asParam(r *http.Request, v *auth.Viewer) string {
	if !DemoAuth {
		return ""
	}
	as := strings.TrimSpace(r.URL.Query().Get("as"))
	if as == "" || v == nil {
		return ""
	}
	q := "?as=" + urlQueryEscape(as)
	if v.PreviewAs != "" {
		q += "&preview_as=" + urlQueryEscape(v.PreviewAs)
	}
	return q
}

func urlQueryEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "%", "%25"), "&", "%26")
}

// cleanPagePath normalizes a vault-relative page ID and rejects traversal.
// Lookup is case-insensitive elsewhere; display preserves source case.
func cleanPagePath(p string) (string, bool) {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(strings.TrimSpace(p), "/")
	if p == "" || strings.ContainsRune(p, 0) {
		return "", false
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}

// loadFilteredPage is the single funnel for page reads: index lookup, parse,
// server-side secret filter, then link/embed post-processing. Any failure —
// missing page, unauthorized secret, bad path — returns an error the caller
// maps to a UNIFORM 404 (no title, path confirmation, or distinctive timing).
func loadFilteredPage(ctx context.Context, st store.Store, viewer *auth.Viewer, pagePath string) (*markdown.Page, error) {
	sp, err := st.PageGet(ctx, pagePath)
	if err != nil {
		return nil, secrets.ErrNotFound
	}
	page, err := markdown.Parse(ctx, sp.Content, sp.Path)
	if err != nil {
		return nil, secrets.ErrNotFound
	}
	filtered, err := secrets.Filter(viewer, page)
	if err != nil {
		return nil, err
	}
	postProcessLinks(ctx, st, viewer, filtered)
	return filtered, nil
}

// postProcessLinks rewrites wikilink hrefs to read-path routes, redacts links
// pointing at secret pages the viewer cannot see (alias ALWAYS dropped), and
// expands page embeds one level (nested embeds stay links: no cycles).
// Image embeds get ACL-checked /assets srcs. Operates on already-filtered HTML.
func postProcessLinks(ctx context.Context, st store.Store, viewer *auth.Viewer, page *markdown.Page) {
	asQ := ""
	if viewer != nil && viewer.UserID != "" {
		asQ = "?as=" + urlQueryEscape(viewer.UserID)
		if viewer.PreviewAs != "" {
			asQ += "&preview_as=" + urlQueryEscape(viewer.PreviewAs)
		}
	}
	htmlOut := page.HTML
	for _, l := range page.Links {
		target := strings.TrimSpace(l.Target)
		if target == "" {
			continue
		}
		base := target
		if i := strings.LastIndex(base, "#"); i >= 0 {
			base = base[:i]
		}
		anchorPrefix := `<a class="wikilink" data-target="` + html.EscapeString(target) + `"`
		i := strings.Index(htmlOut, anchorPrefix)
		if i < 0 {
			continue
		}
		end := strings.Index(htmlOut[i:], "</a>")
		if end < 0 {
			continue
		}
		end += i + len("</a>")
		if secretTarget(ctx, st, viewer, base) {
			htmlOut = htmlOut[:i] + `<span class="redacted" role="note" aria-label="Redacted secret link">` +
				secrets.RedactedPage + `</span>` + htmlOut[end:]
			continue
		}
		alias := l.Alias
		if alias == "" {
			alias = target
		}
		hrefPath := base
		if sp := resolveTarget(ctx, st, target); sp != nil {
			hrefPath = sp.Path
		}
		// Strip .md extension for clean URLs
		hrefPath = strings.TrimSuffix(hrefPath, ".md")
		hrefPath = strings.TrimSuffix(hrefPath, ".markdown")
		rebuilt := `<a class="wikilink" data-target="` + html.EscapeString(target) +
			`" href="/p/` + html.EscapeString(hrefPath) + asQ + `">` + html.EscapeString(alias) + `</a>`
		htmlOut = htmlOut[:i] + rebuilt + htmlOut[end:]
	}
	for _, e := range page.Embeds {
		target := strings.TrimSpace(e.Target)
		if target == "" {
			continue
		}
		spanPrefix := `<span class="embed embed-page" data-target="` + html.EscapeString(target) + `"`
		if i := strings.Index(htmlOut, spanPrefix); i >= 0 {
			end := strings.Index(htmlOut[i:], "</span>")
			if end < 0 {
				continue
			}
			end += i + len("</span>")
			htmlOut = htmlOut[:i] + expandEmbed(ctx, st, viewer, target) + htmlOut[end:]
		}
	}
	htmlOut = rewriteAssetSrcs(htmlOut)
	page.HTML = htmlOut
}

// secretTarget reports whether target names a secret page the viewer may not
// see. Unknown targets (dangling links) are NOT secret: they render as links.
func secretTarget(ctx context.Context, st store.Store, viewer *auth.Viewer, target string) bool {
	sp := resolveTarget(ctx, st, target)
	if sp == nil {
		return false
	}
	return sp.Secret && !secrets.CanViewPage(viewer, sp.Secret, sp.Path, sp.Owner, sp.EditableBy)
}

// resolveTarget maps a wikilink target to an index row. Targets are usually
// extensionless (`[[cinder-pact]]` → `cinder-pact.md`); lookup is
// case-insensitive (store.PageGet folds).
func resolveTarget(ctx context.Context, st store.Store, target string) *store.Page {
	base := strings.TrimSpace(strings.TrimPrefix(target, "./"))
	if i := strings.LastIndex(base, "#"); i >= 0 {
		base = base[:i]
	}
	if base == "" {
		return nil
	}
	for _, cand := range []string{base, base + ".md", base + ".markdown"} {
		if sp, err := st.PageGet(ctx, cand); err == nil {
			return sp
		}
	}
	return nil
}

// expandEmbed renders a `![[page]]` transclusion: redacted span when the
// viewer may not see the target, else the target's filtered body (depth 1).
func expandEmbed(ctx context.Context, st store.Store, viewer *auth.Viewer, target string) string {
	sp := resolveTarget(ctx, st, target)
	if sp == nil {
		return `<span class="redacted" role="note" aria-label="Redacted secret link">` + secrets.RedactedPage + `</span>`
	}
	embedded, err := markdown.Parse(ctx, sp.Content, sp.Path)
	if err != nil {
		return `<span class="redacted" role="note" aria-label="Redacted secret link">` + secrets.RedactedPage + `</span>`
	}
	filtered, err := secrets.Filter(viewer, embedded)
	if err != nil {
		return `<span class="redacted" role="note" aria-label="Redacted secret link">` + secrets.RedactedPage + `</span>`
	}
	return `<div class="embed-expanded" data-target="` + html.EscapeString(target) + `">` + filtered.HTML + `</div>`
}

// rewriteAssetSrcs points renderer-relative image srcs at the ACL-checked
// /assets handler. Absolute URLs and roots are left alone.
func rewriteAssetSrcs(htmlOut string) string {
	const prefix = `<img class="embed" src="`
	var b strings.Builder
	b.Grow(len(htmlOut))
	rest := htmlOut
	for {
		i := strings.Index(rest, prefix)
		if i < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i+len(prefix)])
		rest = rest[i+len(prefix):]
		end := strings.Index(rest, `"`)
		if end < 0 {
			b.WriteString(rest)
			break
		}
		src := rest[:end]
		if !strings.HasPrefix(src, "/") && !strings.Contains(src, "://") {
			src = "/assets/" + src
		}
		b.WriteString(src)
		rest = rest[end:]
	}
	return b.String()
}

// navEntries builds the secret-filtered vault tree for the shell sidebar.
// Secret titles/paths are dropped for unauthorized viewers — never redacted
// here (a redacted row still signals existence in a listing).
func navEntries(ctx context.Context, st store.Store, viewer *auth.Viewer, active string) []templates.NavEntry {
	pages, err := st.PageList(ctx, store.PageListOptions{IncludeSecret: true, Limit: 500})
	if err != nil {
		return nil
	}
	var out []templates.NavEntry
	for _, p := range pages {
		if p.Secret && !secrets.CanViewPage(viewer, p.Secret, p.Path, p.Owner, p.EditableBy) {
			continue
		}
		navPath := strings.TrimSuffix(strings.TrimSuffix(p.Path, ".md"), ".markdown")
		out = append(out, templates.NavEntry{
			Path:   navPath,
			Title:  p.Title,
			Secret: p.Secret,
			Active: strings.EqualFold(p.Path, active),
		})
	}
	return out
}

// visibleBacklinks resolves backlink sources to filtered {path, title} rows.
func visibleBacklinks(ctx context.Context, st store.Store, viewer *auth.Viewer, pagePath string) []templates.Backlink {
	srcs, err := st.Backlinks(ctx, pagePath)
	if err != nil {
		return nil
	}
	var out []templates.Backlink
	for _, s := range srcs {
		if len(out) >= 50 {
			break
		}
		sp, err := st.PageGet(ctx, s)
		if err != nil {
			continue
		}
		if sp.Secret && !secrets.CanViewPage(viewer, sp.Secret, sp.Path, sp.Owner, sp.EditableBy) {
			continue
		}
		backlinkPath := strings.TrimSuffix(strings.TrimSuffix(sp.Path, ".md"), ".markdown")
		out = append(out, templates.Backlink{Path: backlinkPath, Title: sp.Title})
	}
	return out
}

func (h *ReadHandlers) renderShell(w http.ResponseWriter, r *http.Request, status int, data templates.PageData, viewer *auth.Viewer, body templ.Component) {
	slog.Info("read", "path", r.URL.Path, "user", viewerLabel(viewer))
	// Include CSRF token for forms in shell (logout, etc.)
	data.CSRFToken = h.csrfTokenForForm(r)
	// Shell slot mounts resolve through the enabled-aware provider (gate
	// G3): disabled plugins contribute no mount points. The provider also
	// applies viewer/path gating (GM-only, grants, prefixes) server-side.
	if h != nil {
		for _, slot := range []string{SlotHeaderRight, SlotSidebarLeft, SlotSidebarRight, SlotFooter} {
			for _, c := range shellSlots(h.Slots, h.SlotRegistry, slot, viewer, data.Path) {
				data.Slots = append(data.Slots, templates.SlotMark{
					SlotName: slot, ID: c.ID, Component: c.Component, PluginID: c.PluginID,
				})
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Read output is per-viewer: never shared-cache, never stored.
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	_ = templates.Shell(data, viewer, body).Render(r.Context(), w)
}

func viewerLabel(v *auth.Viewer) string {
	if v == nil || v.UserID == "" {
		return "guest"
	}
	if v.PreviewAs != "" {
		return v.UserID + " preview-as " + v.PreviewAs
	}
	return v.UserID
}

// randomPassword generates a random password of the given length.
func randomPassword(length int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*"
	result := make([]byte, length)
	for i := 0; i < length; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		result[i] = chars[n.Int64()]
	}
	return string(result)
}

// Healthz returns health check endpoint (version only; E1's ops mux owns the
// canonical registration — see amend note in the final report).
func (h *ReadHandlers) Healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": Version})
}

// Version returns version endpoint (version only, never vault data).
func (h *ReadHandlers) Version(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"version": Version})
}

// Index redirects exactly "/" to the campaign landing page (from
// campaign.yaml landing-page, default "welcome"). Every other path reaching
// this handler (the registry's "/" pattern is the mux catch-all) gets a
// uniform content-free 404 — unknown paths and retired routes must never
// redirect to landing (that masked typos and dead features).
func (h *ReadHandlers) Index(w http.ResponseWriter, r *http.Request) {
	viewer := viewerFromRequest(r)
	if r.URL.Path != "/" {
		h.notFound(w, r, viewer)
		return
	}
	landingPage := h.landingPage()
	// Redirect to the landing page, preserving demo identity (?as=)
	redirectPath := "/p/" + landingPage
	if as := asParam(r, viewer); as != "" {
		redirectPath += as
	}
	http.Redirect(w, r, redirectPath, http.StatusSeeOther)
}

// landingPage returns the configured landing page from campaign.yaml,
// or "welcome" as the default fallback.
func (h *ReadHandlers) landingPage() string {
	if h.Campaign != nil && h.Campaign.LandingPage != "" {
		return h.Campaign.LandingPage
	}
	return "welcome"
}

// campaignName returns the campaign name from campaign.yaml, or "Yonder" as default.
func (h *ReadHandlers) campaignName() string {
	if h.Campaign != nil && h.Campaign.Name != "" {
		return h.Campaign.Name
	}
	return "Yonder"
}

// PageView renders a page view: uniform 404 for missing OR unauthorized
// secret pages; everything else secret-filtered server-side.
func (h *ReadHandlers) PageView(w http.ResponseWriter, r *http.Request) {
	viewer := viewerFromRequest(r)
	raw := strings.TrimPrefix(r.URL.Path, "/p/")
	pagePath, ok := cleanPagePath(raw)
	if !ok {
		h.notFound(w, r, viewer)
		return
	}
	page, err := loadFilteredPage(r.Context(), h.Store, viewer, pagePath)
	if err != nil {
		// Retry with .md extension — index stores paths with extension.
		page, err = loadFilteredPage(r.Context(), h.Store, viewer, pagePath+".md")
		if err != nil {
			h.notFound(w, r, viewer)
			return
		}
	}
	cleanPath := strings.TrimSuffix(strings.TrimSuffix(page.Path, ".md"), ".markdown")
	data := templates.PageData{
		Title:        page.Title,
		Path:         cleanPath,
		BodyHTML:     page.HTML,
		TOC:          page.TOC,
		Backlinks:    visibleBacklinks(r.Context(), h.Store, viewer, page.Path),
		Nav:          navEntries(r.Context(), h.Store, viewer, page.Path),
		Secret:       page.Secret,
		Owner:        page.Owner,
		Quarantined:  page.Quarantined,
		Quarantine:   page.QuarantineReason,
		ViewerLabel:  viewerLabel(viewer),
		AsParam:      asParam(r, viewer),
		CampaignName: h.campaignName(),
		LandingPage:  h.landingPage(),
	}
	if viewer != nil {
		data.PreviewAs = viewer.PreviewAs
	}
	h.renderShell(w, r, http.StatusOK, data, viewer, templates.PageBody(data))
}

func (h *ReadHandlers) notFound(w http.ResponseWriter, r *http.Request, viewer *auth.Viewer) {
	data := templates.PageData{
		Title:        "Not found",
		Nav:          navEntries(r.Context(), h.Store, viewer, ""),
		ViewerLabel:  viewerLabel(viewer),
		AsParam:      asParam(r, viewer),
		CampaignName: h.campaignName(),
		LandingPage:  h.landingPage(),
	}
	if viewer != nil {
		data.PreviewAs = viewer.PreviewAs
	}
	h.renderShell(w, r, http.StatusNotFound, data, viewer, templates.NotFound(data))
}

// Search handles search requests. The store filters by viewer ACL and cuts
// snippets from visible chunks only; a page-level re-check here is
// defense-in-depth (fail closed: drop on any doubt).
func (h *ReadHandlers) Search(w http.ResponseWriter, r *http.Request) {
	viewer := viewerFromRequest(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	var hits []templates.SearchHit
	if q != "" {
		results, err := h.Store.Search(r.Context(), q, store.SearchOptions{Viewer: viewer, Limit: 20})
		if err == nil {
			for _, res := range results {
				sp, err := h.Store.PageGet(r.Context(), res.Path)
				if err != nil {
					continue
				}
				if sp.Secret && !secrets.CanViewPage(viewer, sp.Secret, sp.Path, sp.Owner, sp.EditableBy) {
					continue
				}
				hits = append(hits, templates.SearchHit{
					Path:    res.Path,
					Title:   res.Title,
					Snippet: res.Snippet,
					Secret:  res.Secret,
				})
			}
		}
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, no-store")
		_ = json.NewEncoder(w).Encode(hits)
		return
	}
	data := templates.PageData{
		Title:        "Search",
		Nav:          navEntries(r.Context(), h.Store, viewer, ""),
		ViewerLabel:  viewerLabel(viewer),
		AsParam:      asParam(r, viewer),
		CampaignName: h.campaignName(),
		LandingPage:  h.landingPage(),
	}
	if viewer != nil {
		data.PreviewAs = viewer.PreviewAs
	}
	h.renderShell(w, r, http.StatusOK, data, viewer, templates.SearchBody(q, hits, data.AsParam))
}

// graphNode / graphEdge are the JSON graph (secret-filtered: invisible pages
// and their edges are dropped, never redacted).
type graphNode struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Secret bool   `json:"secret"`
}

type graphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Graph returns the page graph, secret-filtered server-side.
func (h *ReadHandlers) Graph(w http.ResponseWriter, r *http.Request) {
	viewer := viewerFromRequest(r)
	pages, err := h.Store.PageList(r.Context(), store.PageListOptions{IncludeSecret: true, Limit: 1000})
	if err != nil {
		http.Error(w, "graph unavailable", http.StatusInternalServerError)
		return
	}
	visible := map[string]string{}
	fold := map[string]string{} // lower(path) -> path: page IDs are case-insensitive (Phase 0c)
	var nodes []graphNode
	for _, p := range pages {
		if p.Secret && !secrets.CanViewPage(viewer, p.Secret, p.Path, p.Owner, p.EditableBy) {
			continue
		}
		visible[p.Path] = p.Title
		fold[strings.ToLower(p.Path)] = p.Path
		nodes = append(nodes, graphNode{ID: p.Path, Title: p.Title, Secret: p.Secret})
	}
	var edges []graphEdge
	for id := range visible {
		targets, err := h.Store.ForwardLinks(r.Context(), id)
		if err != nil {
			continue
		}
		for _, t := range targets {
			// The indexer stores RAW wikilink targets (`cinder-pact`)
			// while page IDs carry extensions (`cinder-pact.md`): a
			// direct map hit misses every live edge (F2-filed). Fix on
			// the handler side with the same extension candidates
			// resolveTarget uses — the index shape stays untouched.
			if to, ok := resolveGraphTarget(fold, t); ok {
				if _, ok := visible[to]; ok {
					edges = append(edges, graphEdge{From: id, To: to})
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"nodes": nodes, "edges": edges})
}

// resolveGraphTarget maps a raw stored wikilink target to a visible page
// path, mirroring resolveTarget's extension candidates (`[[cinder-pact]]`
// → `cinder-pact.md`) plus the case-insensitive page-ID rule. Unresolvable
// targets (missing pages, secret pages the viewer cannot see) return false
// and stay out of the edge list — never redacted, just absent.
func resolveGraphTarget(fold map[string]string, target string) (string, bool) {
	base := strings.TrimSpace(strings.TrimPrefix(target, "./"))
	if i := strings.LastIndex(base, "#"); i >= 0 {
		base = base[:i]
	}
	if base == "" {
		return "", false
	}
	for _, cand := range []string{base, base + ".md", base + ".markdown"} {
		if p, ok := fold[strings.ToLower(cand)]; ok {
			return p, true
		}
	}
	return "", false
}

// Autocomplete returns title suggestions, secret-filtered (invisible secret
// pages are excluded, never redacted: a redacted row leaks existence).
func (h *ReadHandlers) Autocomplete(w http.ResponseWriter, r *http.Request) {
	viewer := viewerFromRequest(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	type suggestion struct {
		Title string `json:"title"`
		Path  string `json:"path"`
	}
	out := []suggestion{}
	if q != "" {
		results, err := h.Store.Search(r.Context(), q, store.SearchOptions{Viewer: viewer, Limit: 8})
		if err == nil {
			for _, res := range results {
				sp, err := h.Store.PageGet(r.Context(), res.Path)
				if err != nil {
					continue
				}
				if sp.Secret && !secrets.CanViewPage(viewer, sp.Secret, sp.Path, sp.Owner, sp.EditableBy) {
					continue
				}
				out = append(out, suggestion{Title: res.Title, Path: res.Path})
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	_ = json.NewEncoder(w).Encode(out)
}

// Assets serves vault assets through the ACL-checked handler (never a raw
// static mount — pitfalls). Bundle rule: an asset sitting beside pages is
// served when at least one same-directory page is visible to the viewer (or
// the directory holds no pages: public attachments). Markdown sources are
// never served here. Traversal outside the vault is rejected.
func (h *ReadHandlers) Assets(w http.ResponseWriter, r *http.Request) {
	viewer := viewerFromRequest(r)
	raw := strings.TrimPrefix(r.URL.Path, "/assets/")
	assetPath, ok := cleanPagePath(raw)
	if !ok || VaultDir == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if strings.HasSuffix(strings.ToLower(assetPath), ".md") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if !assetVisible(r.Context(), h.Store, viewer, assetPath) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	fsPath := filepath.Join(VaultDir, filepath.FromSlash(assetPath))
	if rel, err := filepath.Rel(VaultDir, fsPath); err != nil || strings.HasPrefix(rel, "..") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	f, err := os.Open(fsPath)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// Never sniff SVG as image (pitfalls): serve svg as text with sandbox.
	lower := strings.ToLower(assetPath)
	if strings.HasSuffix(lower, ".svg") || strings.HasSuffix(lower, ".svgz") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	} else {
		var head [512]byte
		n, _ := f.Read(head[:])
		w.Header().Set("Content-Type", http.DetectContentType(head[:n]))
		_, _ = f.Seek(0, 0)
	}
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, path.Base(assetPath), fi.ModTime(), f)
}

// assetVisible implements the page-bundle rule for vault assets.
func assetVisible(ctx context.Context, st store.Store, viewer *auth.Viewer, assetPath string) bool {
	dir := path.Dir(assetPath)
	if dir == "." {
		dir = ""
	}
	prefix := dir
	if prefix != "" {
		prefix += "/"
	}
	pages, err := st.PageList(ctx, store.PageListOptions{Prefix: prefix, IncludeSecret: true, Limit: 100})
	if err != nil {
		return false
	}
	// PageList prefix-matches recursively; keep same-directory pages only.
	sameDir := 0
	for _, p := range pages {
		if path.Dir(p.Path) != dir {
			continue
		}
		sameDir++
		if !p.Secret || secrets.CanViewPage(viewer, p.Secret, p.Path, p.Owner, p.EditableBy) {
			return true
		}
	}
	return sameDir == 0
}

// SSE handles Server-Sent Events for live updates (P01 hello: GM `-`/`+`
// flips propagate to subscribed viewers, each re-filtered server-side).
// Hello carries only what the viewer may see: secret titles ride the stream
// solely to authorized viewers; everyone else gets visible:false.
func (h *ReadHandlers) SSE(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		h.publishFlip(w, r)
		return
	}
	viewer := viewerFromRequest(r)
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	pagePath, _ := cleanPagePath(r.URL.Query().Get("path"))
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	sub := hub.subscribe()
	defer hub.unsubscribe(sub)
	// Lane K amend: VTT broadcasts ride the same stream (frozen event
	// names). ?map= scopes one connection to one board; without it the
	// connection hears every map it may view (checked per event below).
	vsub := vttWireSubscribe()
	defer vttWireUnsubscribe(vsub)
	mapParam := strings.TrimSpace(r.URL.Query().Get("map"))
	if _, err := fmt.Fprintf(w, "event: hello\ndata: %s\n\n", helloPayload(r.Context(), h.Store, viewer, pagePath)); err != nil {
		return
	}
	flusher.Flush()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case flipped := <-sub.ch:
			if pagePath != "" && flipped != "" && !strings.EqualFold(flipped, pagePath) {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: secret-flip\ndata: %s\n\n", helloPayload(r.Context(), h.Store, viewer, flipped)); err != nil {
				return
			}
			flusher.Flush()
		case vev := <-vsub:
			data, ok := vttWirePayload(r.Context(), h.Store, viewer, vev, mapParam)
			if !ok {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", vev.Name, data); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			_, _ = w.Write([]byte(": heartbeat\n\n"))
			flusher.Flush()
		}
	}
}

func helloPayload(ctx context.Context, st store.Store, viewer *auth.Viewer, pagePath string) string {
	type payload struct {
		Path    string `json:"path"`
		Visible bool   `json:"visible"`
		Title   string `json:"title"`
		Secret  bool   `json:"secret"`
	}
	p := payload{Visible: false, Title: secrets.RedactedTitle}
	if pagePath != "" && st != nil {
		if page, err := loadFilteredPage(ctx, st, viewer, pagePath); err == nil {
			p = payload{Path: page.Path, Visible: true, Title: page.Title, Secret: page.Secret}
		}
	} else if pagePath == "" {
		p = payload{Visible: true}
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// publishFlip ingests GM `-`/`+` flips (POST /events, GM-only, no preview).
// The flip itself lands via the write path (Lane F2); this broadcast only
// wakes subscribers, each of which re-reads + re-filters server-side.
func (h *ReadHandlers) publishFlip(w http.ResponseWriter, r *http.Request) {
	viewer := viewerFromRequest(r)
	if viewer == nil || !viewer.IsGM || viewer.PreviewAs != "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pagePath, ok := cleanPagePath(r.Form.Get("path"))
	if !ok {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	slog.Info("secret flip broadcast", "gm", viewer.UserID, "path", pagePath)
	hub.publish(pagePath)
	w.WriteHeader(http.StatusAccepted)
}

// Me returns the current viewer's page (P11 will own this; minimal here).
func (h *ReadHandlers) Me(w http.ResponseWriter, r *http.Request) {
	viewer := viewerFromRequest(r)
	if viewer == nil || viewer.UserID == "" {
		http.Error(w, "login required", http.StatusUnauthorized)
		return
	}
	data := templates.PageData{
		Title:        "Me",
		Nav:          navEntries(r.Context(), h.Store, viewer, ""),
		ViewerLabel:  viewerLabel(viewer),
		AsParam:      asParam(r, viewer),
		CampaignName: h.campaignName(),
		LandingPage:  h.landingPage(),
	}
	h.renderShell(w, r, http.StatusOK, data, viewer, templates.Placeholder("Me", "Logged in as "+viewerLabel(viewer)+". Character sheets land in Phase 3 (Lane I1)."))
}

// Dashboard returns the GM dashboard with campaign settings and user management.
func (h *ReadHandlers) Dashboard(w http.ResponseWriter, r *http.Request) {
	viewer := viewerFromRequest(r)
	if viewer == nil || !viewer.IsGM || viewer.PreviewAs != "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Load current campaign settings
	var camp *campaign.Campaign
	if h.Campaign != nil {
		camp = h.Campaign
	} else {
		camp = &campaign.Campaign{}
	}

	// Load users for management
	var users []*auth.User
	if h.UserStore != nil {
		if list, err := h.UserStore.List(r.Context()); err == nil {
			users = list
		}
	}

	data := templates.PageData{
		Title:        "Dashboard",
		Nav:          navEntries(r.Context(), h.Store, viewer, ""),
		ViewerLabel:  viewerLabel(viewer),
		AsParam:      asParam(r, viewer),
		CampaignName: h.campaignName(),
		LandingPage:  h.landingPage(),
	}

	// Convert campaign.Campaign to template CampaignSettings
	var tmplCampaign *templates.CampaignSettings
	if camp != nil {
		tmplCampaign = &templates.CampaignSettings{
			Name:            camp.Name,
			Created:         camp.Created,
			Base:            camp.Base,
			BaseVersion:     camp.BaseVersion,
			Overlay:         camp.Overlay,
			OverlayVersion:  camp.OverlayVersion,
			EnabledFeatures: camp.EnabledFeatures,
			EnabledPlugins:  camp.EnabledPlugins,
			LandingPage:     camp.LandingPage,
		}
	}

	dashboardData := templates.CampaignDashboardData{
		Campaign:  tmplCampaign,
		Users:     users,
		CSRFToken: h.csrfTokenForForm(r),
		AsParam:   asParam(r, viewer),
		Dice:      h.dashboardDice(r.Context(), viewer),
		Encounter: h.dashboardEncounter(r),
	}

	h.renderShell(w, r, http.StatusOK, data, viewer, templates.DashboardBody(dashboardData))
}

// dashboardDice loads the recent dice log for the dashboard, newest last,
// filtered server-side per viewer (blind totals GM-only, redacted otherwise).
// A nil app DB (unwired fakes) yields an empty log, never an error.
func (h *ReadHandlers) dashboardDice(ctx context.Context, viewer *auth.Viewer) []templates.DiceLogRow {
	if h.Store == nil || h.Store.AppDB() == nil {
		return nil
	}
	rows := filterDiceRows(recentDiceLog(ctx, h.Store.AppDB(), recentDiceLimit), viewer)
	out := make([]templates.DiceLogRow, 0, len(rows))
	// recentDiceLog returns newest-first; the dashboard renders oldest-first.
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		out = append(out, templates.DiceLogRow{
			RollID: row.RollID, Actor: row.Actor, Notation: row.Notation,
			Total: row.Total, Blind: row.Blind,
		})
	}
	return out
}

// dashboardEncounter builds the dashboard encounter section: the closed
// compendium roster (HP resolved server-side), the map list, and the
// selected map's live tokens. GM-only callers; tokens carry positions +
// hidden flags because unauthorized viewers never reach this page.
func (h *ReadHandlers) dashboardEncounter(r *http.Request) templates.EncounterData {
	return buildEncounterData(r.Context(), h.Store, strings.TrimSpace(r.URL.Query().Get("map")))
}

// DashboardSave handles updating campaign settings.
func (h *ReadHandlers) DashboardSave(w http.ResponseWriter, r *http.Request) {
	viewer := viewerFromRequest(r)
	if viewer == nil || !viewer.IsGM || viewer.PreviewAs != "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !h.csrfOK(r) {
		writeDenied(w, http.StatusForbidden, "bad or missing CSRF token")
		return
	}

	c := &campaign.Campaign{
		Name:           strings.TrimSpace(r.FormValue("name")),
		Base:           strings.TrimSpace(r.FormValue("base")),
		BaseVersion:    strings.TrimSpace(r.FormValue("base-version")),
		Overlay:        strings.TrimSpace(r.FormValue("overlay")),
		OverlayVersion: strings.TrimSpace(r.FormValue("overlay-version")),
		LandingPage:    strings.TrimSpace(r.FormValue("landing-page")),
	}

	var features []string
	for _, line := range strings.Split(r.FormValue("features"), "\n") {
		if f := strings.TrimSpace(line); f != "" {
			features = append(features, f)
		}
	}
	c.EnabledFeatures = features

	var plugins []string
	for _, line := range strings.Split(r.FormValue("plugins"), "\n") {
		if p := strings.TrimSpace(line); p != "" {
			plugins = append(plugins, p)
		}
	}
	c.EnabledPlugins = plugins
	// The writer preserves given order; dashboard saves normalize to
	// sorted order (the historical UpsertCampaignYAML behavior).
	sort.Strings(c.EnabledFeatures)
	sort.Strings(c.EnabledPlugins)

	if err := campaign.Validate(c); err != nil {
		writeDenied(w, http.StatusUnprocessableEntity, "campaign: "+err.Error())
		return
	}

	// Preserve created timestamp from existing campaign
	if h.Campaign != nil && c.Created == "" {
		c.Created = h.Campaign.Created
	}
	if c.Created == "" {
		c.Created = time.Now().UTC().Format(time.RFC3339)
	}

	// Write campaign.yaml via vault (need VaultWriter)
	if h.Vault == nil {
		writeDenied(w, http.StatusInternalServerError, "vault is not configured")
		return
	}
	// Read existing content to preserve comments/order
	existing := ""
	if b, err := h.Vault.ReadFile(r.Context(), "campaign.yaml"); err == nil {
		existing = string(b)
	}
	// Use surgical upsert to preserve comments and order
	content := campaign.Upsert(existing, c)
	if err := h.Vault.WriteFile(r.Context(), "campaign.yaml", content); err != nil {
		writeDenied(w, http.StatusInternalServerError, "could not write campaign.yaml")
		return
	}
	// Update in-memory campaign data
	h.Campaign = c
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// DashboardUserCreate handles creating a new user.
func (h *ReadHandlers) DashboardUserCreate(w http.ResponseWriter, r *http.Request) {
	viewer := viewerFromRequest(r)
	if viewer == nil || !viewer.IsGM || viewer.PreviewAs != "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if h.UserStore == nil {
		writeDenied(w, http.StatusInternalServerError, "user store not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !h.csrfOK(r) {
		writeDenied(w, http.StatusForbidden, "bad or missing CSRF token")
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	isGM := r.FormValue("is_gm") == "1"

	if err := auth.ValidateUsername(username); err != nil {
		writeDenied(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := auth.ValidatePassword(password); err != nil {
		writeDenied(w, http.StatusBadRequest, err.Error())
		return
	}

	nowUnix := time.Now().Unix()
	_, err := auth.CreateUser(r.Context(), h.UserStore, username, password, isGM, nowUnix)
	if err != nil {
		if errors.Is(err, auth.ErrUserExists) {
			writeDenied(w, http.StatusConflict, "user already exists")
			return
		}
		writeDenied(w, http.StatusInternalServerError, "could not create user")
		return
	}

	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// DashboardUserResetPassword handles resetting a user's password.
func (h *ReadHandlers) DashboardUserResetPassword(w http.ResponseWriter, r *http.Request) {
	viewer := viewerFromRequest(r)
	if viewer == nil || !viewer.IsGM || viewer.PreviewAs != "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if h.UserStore == nil || h.SessionStore == nil {
		writeDenied(w, http.StatusInternalServerError, "user store not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !h.csrfOK(r) {
		writeDenied(w, http.StatusForbidden, "bad or missing CSRF token")
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	if username == "" {
		writeDenied(w, http.StatusBadRequest, "username required")
		return
	}

	// Generate a random password
	newPassword := randomPassword(16)
	nowUnix := time.Now().Unix()

	if err := auth.ResetPassword(r.Context(), h.UserStore, h.SessionStore, username, newPassword, nowUnix); err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			writeDenied(w, http.StatusNotFound, "user not found")
			return
		}
		writeDenied(w, http.StatusInternalServerError, "could not reset password")
		return
	}

	// Show the new password to the GM
	data := templates.PageData{
		Title:        "Password Reset",
		Nav:          navEntries(r.Context(), h.Store, viewer, ""),
		ViewerLabel:  viewerLabel(viewer),
		AsParam:      asParam(r, viewer),
		CampaignName: h.campaignName(),
		LandingPage:  h.landingPage(),
	}
	body := fmt.Sprintf(`<h1>Password Reset</h1><p>Password for <strong>%s</strong> has been reset.</p><p>New password: <code>%s</code></p><p><a href="/dashboard" class="btn">Back to Dashboard</a></p>`, html.EscapeString(username), html.EscapeString(newPassword))
	h.renderShell(w, r, http.StatusOK, data, viewer, templates.RawBody(body))
}

// DashboardUserRevokeSessions handles revoking all sessions for a user.
func (h *ReadHandlers) DashboardUserRevokeSessions(w http.ResponseWriter, r *http.Request) {
	viewer := viewerFromRequest(r)
	if viewer == nil || !viewer.IsGM || viewer.PreviewAs != "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if h.UserStore == nil || h.SessionStore == nil {
		writeDenied(w, http.StatusInternalServerError, "user store not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !h.csrfOK(r) {
		writeDenied(w, http.StatusForbidden, "bad or missing CSRF token")
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	if username == "" {
		writeDenied(w, http.StatusBadRequest, "username required")
		return
	}

	nowUnix := time.Now().Unix()
	if err := auth.RevokeAllSessions(r.Context(), h.UserStore, h.SessionStore, username, nowUnix); err != nil {
		writeDenied(w, http.StatusInternalServerError, "could not revoke sessions")
		return
	}

	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// DashboardUserMakeGM handles granting GM privileges to a user.
func (h *ReadHandlers) DashboardUserMakeGM(w http.ResponseWriter, r *http.Request) {
	viewer := viewerFromRequest(r)
	if viewer == nil || !viewer.IsGM || viewer.PreviewAs != "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if h.UserStore == nil {
		writeDenied(w, http.StatusInternalServerError, "user store not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !h.csrfOK(r) {
		writeDenied(w, http.StatusForbidden, "bad or missing CSRF token")
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	if username == "" {
		writeDenied(w, http.StatusBadRequest, "username required")
		return
	}

	u, err := h.UserStore.GetByUsername(r.Context(), username)
	if err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			writeDenied(w, http.StatusNotFound, "user not found")
			return
		}
		writeDenied(w, http.StatusInternalServerError, "could not load user")
		return
	}

	u.IsGM = true
	u.UpdatedAt = time.Now().Unix()
	if err := h.UserStore.Update(r.Context(), u); err != nil {
		writeDenied(w, http.StatusInternalServerError, "could not update user")
		return
	}

	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// DashboardUserRemoveGM handles removing GM privileges from a user.
func (h *ReadHandlers) DashboardUserRemoveGM(w http.ResponseWriter, r *http.Request) {
	viewer := viewerFromRequest(r)
	if viewer == nil || !viewer.IsGM || viewer.PreviewAs != "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if h.UserStore == nil {
		writeDenied(w, http.StatusInternalServerError, "user store not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !h.csrfOK(r) {
		writeDenied(w, http.StatusForbidden, "bad or missing CSRF token")
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	if username == "" {
		writeDenied(w, http.StatusBadRequest, "username required")
		return
	}

	// Prevent removing GM from the last GM
	users, err := h.UserStore.List(r.Context())
	if err == nil {
		gmCount := 0
		for _, u := range users {
			if u.IsGM {
				gmCount++
			}
		}
		if gmCount <= 1 {
			writeDenied(w, http.StatusBadRequest, "cannot remove the last GM")
			return
		}
	}

	u, err := h.UserStore.GetByUsername(r.Context(), username)
	if err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			writeDenied(w, http.StatusNotFound, "user not found")
			return
		}
		writeDenied(w, http.StatusInternalServerError, "could not load user")
		return
	}

	u.IsGM = false
	u.UpdatedAt = time.Now().Unix()
	if err := h.UserStore.Update(r.Context(), u); err != nil {
		writeDenied(w, http.StatusInternalServerError, "could not update user")
		return
	}

	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// VTT returns the VTT placeholder (real VTT is Lane K, Phase 4).
func (h *ReadHandlers) VTT(w http.ResponseWriter, r *http.Request) {
	viewer := viewerFromRequest(r)
	if viewer == nil {
		http.Error(w, "login required", http.StatusUnauthorized)
		return
	}
	data := templates.PageData{
		Title:        "Table",
		Nav:          navEntries(r.Context(), h.Store, viewer, ""),
		ViewerLabel:  viewerLabel(viewer),
		AsParam:      asParam(r, viewer),
		CampaignName: h.campaignName(),
		LandingPage:  h.landingPage(),
	}
	h.renderShell(w, r, http.StatusOK, data, viewer, templates.Placeholder("Table", "The virtual tabletop lands in Phase 4 (Lane K)."))
}

// flipHub fans out secret-flip broadcasts to SSE subscribers. Payloads carry
// only page paths; each connection re-reads + re-filters before writing, so
// no secret content crosses viewers here.
type flipHub struct {
	mu   sync.Mutex
	subs map[*flipSub]struct{}
}

type flipSub struct {
	ch chan string
}

var hub = &flipHub{subs: map[*flipSub]struct{}{}}

func (f *flipHub) subscribe() *flipSub {
	s := &flipSub{ch: make(chan string, 4)}
	f.mu.Lock()
	f.subs[s] = struct{}{}
	f.mu.Unlock()
	return s
}

func (f *flipHub) unsubscribe(s *flipSub) {
	f.mu.Lock()
	delete(f.subs, s)
	f.mu.Unlock()
}

// VTTWireEvent carries one Lane K (Phase 4 VTT) broadcast into the /events
// stream. Lane K amend: web must not import the vtt package (vtt imports web
// for the route registry — the reverse edge would cycle), so this is a dumb
// pipe. Lane K pre-renders both payload variants; the loop below picks per
// viewer (GM without preview sees Open, everyone else Redacted — the blind
// rule from the frozen fragment contract) and drops events for maps the
// viewer may not see (sidecar ACL re-checked per connection, server-side).
// Map events set Open == Redacted (payloads carry IDs only; subscribers
// re-read + re-filter server-side before rendering).
type VTTWireEvent struct {
	Name        string // frozen plugins.Event* name (written verbatim)
	MapID       string // info only (payloads carry it too)
	PagePath    string // sidecar page for the per-connection ACL check
	ScopedParam string // ?map= scoping value, "" = unscoped connection sees all
	Open        string // data payload for full-visibility viewers
	Redacted    string // data payload for everyone else
}

// vttWireHub fans out VTTWireEvents (same drop-on-slow policy as flipHub).
type vttWireHub struct {
	mu   sync.Mutex
	subs map[chan VTTWireEvent]struct{}
}

var vttWire = &vttWireHub{subs: map[chan VTTWireEvent]struct{}{}}

// PublishVTT enqueues a Lane K event for /events subscribers. Called by Lane
// K after each committed VTT write; never blocks (slow subscriber drops).
func PublishVTT(ev VTTWireEvent) {
	vttWire.mu.Lock()
	defer vttWire.mu.Unlock()
	for s := range vttWire.subs {
		select {
		case s <- ev:
		default:
		}
	}
}

func vttWireSubscribe() chan VTTWireEvent {
	ch := make(chan VTTWireEvent, 16)
	vttWire.mu.Lock()
	vttWire.subs[ch] = struct{}{}
	vttWire.mu.Unlock()
	return ch
}

func vttWireUnsubscribe(ch chan VTTWireEvent) {
	vttWire.mu.Lock()
	delete(vttWire.subs, ch)
	vttWire.mu.Unlock()
}

// vttWirePayload picks the per-viewer payload + visibility for one
// connection. Guests (nil viewer) never receive VTT events (login required
// on all live state, P08); GM previews filter as the previewed user.
func vttWirePayload(ctx context.Context, st store.Store, viewer *auth.Viewer, ev VTTWireEvent, mapParam string) (string, bool) {
	if viewer == nil || viewer.UserID == "" {
		return "", false
	}
	if mapParam != "" && ev.MapID != "" && !strings.EqualFold(ev.MapID, mapParam) {
		return "", false
	}
	if ev.PagePath != "" && st != nil {
		page, err := st.PageGet(ctx, ev.PagePath)
		if err != nil {
			return "", false
		}
		eff := viewer
		if viewer.PreviewAs != "" {
			preview := *viewer
			preview.IsGM = false
			preview.PreviewAs = ""
			eff = &preview
		}
		if !secrets.CanViewPage(eff, page.Secret, page.Path, page.Owner, page.EditableBy) {
			return "", false
		}
	}
	if viewer.IsGM && viewer.PreviewAs == "" {
		return ev.Open, true
	}
	return ev.Redacted, true
}

func (f *flipHub) publish(pagePath string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for s := range f.subs {
		select {
		case s.ch <- pagePath:
		default: // slow subscriber: drop, never block the broadcaster
		}
	}
}

// NewMux builds an http.ServeMux from the read registry for wiring (E1 calls
// this as the registry fallback; POST /events doubles as the GM flip
// broadcast — SSE plumbing owned by this lane's SSE task).
func (h *ReadHandlers) NewMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.Healthz)
	mux.HandleFunc("/version", h.Version)
	mux.HandleFunc("/p/", h.PageView)
	mux.HandleFunc("/search", h.Search)
	mux.HandleFunc("/graph", h.Graph)
	mux.HandleFunc("/autocomplete", h.Autocomplete)
	mux.HandleFunc("/assets/", h.Assets)
	mux.HandleFunc("/events", h.SSE)
	mux.HandleFunc("/me", h.Me)
	mux.HandleFunc("/dashboard", h.Dashboard)
	mux.HandleFunc("/dashboard/vault.zip", h.VaultZip)
	mux.HandleFunc("/vtt/", h.VTT)
	return mux
}
