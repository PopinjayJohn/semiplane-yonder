package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/campaign"
	_ "github.com/semiplane/yonder/internal/markdown" // ensure parser registration
	"github.com/semiplane/yonder/internal/plugins"
	"github.com/semiplane/yonder/internal/store"
	"github.com/semiplane/yonder/internal/vault"
	"github.com/semiplane/yonder/internal/vtt"
	"github.com/semiplane/yonder/internal/web"
)

//go:embed static/print.css static/core static/vendor static/themes static/vtt
var staticFS embed.FS

// Ops HTTP surface owned by Lane E1 (P09): /healthz + /version expose the
// binary version (ldflags-stamped) ONLY — never vault names, counts, or any
// vault-derived data. Read/write paths delegate to internal/web handlers
// (Lane F1/F2). When F1/F2 handlers register /healthz + /version, the ops
// mux must not double-register — see wireHandlers below.

type buildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

func writeVersionJSON(w http.ResponseWriter, info buildInfo) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(info)
}

// enablePlugins resolves campaign.yaml enabled-plugins (the I2 namespace —
// never enabled-features, which ruleset optionals own) against the plugin
// registry. Unknown-but-wellformed ids log loudly and stay disabled (the
// plugin may compile in later; serve must boot regardless). Malformed ids
// fail the parse and also stay disabled with a loud log — serve availability
// wins over plugin precision; `plugin check` is the strict gate.
func enablePlugins(vaultDir string, plugReg *plugins.Registry) {
	c, err := campaign.Load(vaultDir)
	if err != nil {
		return // no campaign.yaml yet (init --bare): no plugins
	}
	ids, err := plugins.ParseEnabledPlugins("", c.EnabledPlugins)
	if err != nil {
		slog.Warn("plugins: bad enabled-plugins list, all plugins stay disabled", "err", err)
		return
	}
	ctx := context.Background()
	for _, id := range ids {
		if err := plugReg.Enable(ctx, id); err != nil {
			slog.Warn("plugins: cannot enable, stays disabled", "plugin", id, "err", err)
		}
	}
}

// registerSlotPlugins compiles the v1 slot plugins into the registry.
// Enablement stays data-driven via campaign.yaml enabled-plugins; unknown
// ids stay disabled with a warning (never a crash). All six contribute
// secret-filtered slot mounts only (no routes): vault-tree (sidebar-left),
// slot-header (header-left/center/right), page-companions (sidebar-right),
// vault-colophon (footer), quick-actions (page-actions), sheet-tools
// (sheet-header, characters/ subtree only).
func registerSlotPlugins(plugReg *plugins.Registry) {
	for _, p := range []plugins.Plugin{
		plugins.NewVaultTreePlugin(),
		&plugins.HeaderPlugin{},
		&plugins.CompanionsPlugin{},
		plugins.NewColophonPlugin(),
		plugins.QuickActionsPlugin{},
		&plugins.SheetToolsPlugin{},
	} {
		if err := plugReg.Register(p); err != nil {
			slog.Warn("plugins: cannot register, stays unavailable", "plugin", p.ID(), "err", err)
		}
	}
}

// healthzHandler answers 200 with the version only. No vault info, no
// counts, no request reflection.
func healthzHandler(info buildInfo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeVersionJSON(w, info)
	}
}

func versionHandler(info buildInfo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeVersionJSON(w, info)
	}
}

// Cache directives for embedded public assets (runbook §5). Core CSS and
// theme stylesheets are unversioned, so they get the same day-long public
// cache as print.css. Vendor JS is version-pinned (web/static/vendor/VERSIONS,
// `yonder vendor verify`), so it is immutable for a year. The first-party
// VTT loader (web/static/vtt/) is unversioned like core: day-long public.
const (
	staticCoreCache   = "public, max-age=86400"
	staticVendorCache = "public, max-age=31536000, immutable"
	staticVTTCache    = "public, max-age=86400"
)

// staticDirHandler serves one embedded public-asset subtree (static/core,
// static/vendor, or static/themes) at its /static/... URL prefix with cache +
// nosniff headers. The embed FS holds only committed public files — vault bytes can
// never appear here (vault assets stay on the ACL-checked /assets handler).
// Directory roots and traversal escapes 404 (no listings, no escapes).
func staticDirHandler(urlPrefix, subdir, cacheControl string) http.Handler {
	sub, err := fs.Sub(staticFS, subdir)
	if err != nil {
		panic(fmt.Errorf("static %s: %w", subdir, err))
	}
	inner := http.StripPrefix(urlPrefix, http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := path.Clean(strings.TrimPrefix(r.URL.Path, urlPrefix))
		if name == "." || name == "/" || strings.HasPrefix(name, "..") {
			http.NotFound(w, r)
			return
		}
		if fi, err := fs.Stat(sub, name); err != nil || fi.IsDir() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", cacheControl)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		inner.ServeHTTP(w, r)
	})
}

func printCSSHandler(w http.ResponseWriter, r *http.Request) {
	css, err := staticFS.ReadFile("static/print.css")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// Static assets carry cache headers (P09 client caching); the binary
	// stays offline-first (no service worker in v1).
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(css)
}

// serverWiring is the product of wireHandlers: the HTTP surface plus the
// live registries the gate integration test drives (plugin enablement,
// store seeding). Serve uses Handler; tests use the rest.
type serverWiring struct {
	Handler http.Handler
	Plugins *plugins.Registry
	Store   store.Store
	Vault   *vault.Vault
}

// wireHandlers builds the complete handler chain: ops endpoints (deduped),
// then read/write handlers via the route registry. Data paths come from
// dataPaths (sibling <vault>-data dir, sanitized names) like every other
// command -- never Dir(vault)/Base(vault), which pointed serve at an empty
// index. (G2 amend: serve read an empty index and 404'd every page.)
func wireHandlers(info buildInfo, vaultDir, dataDir string, sessionStore auth.SessionStore, themeFlag, schemeFlag string) *serverWiring {
	// Build store, vault, and handlers.
	indexPath, appPath, _ := dataPaths(dataDir, vaultDir)
	st, err := store.Open(indexPath, appPath)
	if err != nil {
		panic(err)
	}
	v, err := vault.NewVault(vaultDir)
	if err != nil {
		panic(err)
	}
	// B6 backlog: startup rescan. Reconcile the OPEN index with the vault
	// before serving (external edits while offline, crash leftovers) via
	// the shared store.Rescan path — the same function the reindex bulk
	// phase and the watcher refresh ride, not a reimplementation.
	// Incremental (only new/changed/vanished files), so boot stays fast;
	// a failure logs loudly and serves the existing index (the reindex
	// CLI and the watcher recover from there).
	func() {
		ctx, cancel := contextTimeout(30 * time.Second)
		defer cancel()
		stats, err := store.Rescan(ctx, st.IndexDB(), vaultDir, web.ParsePage)
		if err != nil {
			slog.Warn("startup rescan failed, serving existing index", "err", err)
			return
		}
		slog.Info("startup rescan", "scanned", stats.Scanned, "updated", stats.Updated, "deleted", stats.Deleted)
	}()
	// Lane K amend (Phase 4): nothing ever assigned web.VaultDir, so the
	// ACL-checked asset handler 404'd every asset on a live server (map
	// backgrounds included). Point it at the served vault once, here.
	web.VaultDir = vaultDir
	// Gate G3: plugin registries live in serve (E1 file, amend-authorized).
	// web owns the append-only slot/route registries; plugins.Registry adds
	// enablement gating on top. Shell read paths render slots through
	// plugReg.SlotsFor (via the web.SlotProvider seam), so a disabled plugin
	// leaves no UI trace. Slot plugins below are compiled in and resolved
	// against campaign.yaml enabled-plugins below (unknown ids stay disabled).
	slotReg := web.NewSlotRegistry()
	reg := web.NewRouteRegistry()
	plugReg := plugins.NewRegistry(slotReg, reg)
	registerSlotPlugins(plugReg)
	enablePlugins(vaultDir, plugReg)

	// Load session keys for HMAC verification (key rotation support)
	sessionKeys, err := auth.LoadSessionKeys(dataDir)
	if err != nil {
		panic(fmt.Errorf("load session keys: %w", err))
	}

	// Create user store for auth middleware
	userStore, err := auth.NewUserStore(st.AppDB())
	if err != nil {
		panic(fmt.Errorf("create user store: %w", err))
	}

	// Load campaign.yaml for landing page and campaign name
	var campaignData *campaign.Campaign
	if c, err := campaign.Load(vaultDir); err == nil {
		campaignData = c
	}

	readH := web.ReadHandlers{Store: st, SlotRegistry: slotReg, Slots: plugReg, SessionStore: sessionStore, UserStore: userStore, Vault: v, VaultRoot: vaultDir, Campaign: campaignData}
	// T1: resolved theme selection, shared by every render surface (shell,
	// standalone sheet page, VTT page) so one flag themes the whole app.
	// Unknown ids fail closed to unthemed (logged), never a broken attribute.
	theme, scheme := web.ResolveTheme(themeFlag, schemeFlag)
	if themeFlag != "" && theme == "" {
		slog.Warn("theme: unknown --theme, staying unthemed", "theme", themeFlag, "known", strings.Join(web.ThemeList(), ","))
	}
	if theme != "" && schemeFlag != "" && scheme != schemeFlag {
		slog.Warn("theme: unknown --theme-scheme, defaulting", "scheme", schemeFlag, "using", scheme)
	}
	if theme == "" && schemeFlag != "" {
		slog.Warn("theme: --theme-scheme ignored without --theme")
	}
	readH.Theme, readH.Scheme = theme, scheme
	readH.PluginAssets = plugReg
	sessionConfig := auth.DefaultSessionConfig(sessionKeys[0])

	// Reindex function used by both write handlers and vault watcher
	reindexAndRefresh := func() error {
		indexPath, _, _ := dataPaths(dataDir, vaultDir)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := store.Reindex(ctx, vaultDir, indexPath, web.ParsePage); err != nil {
			slog.Error("reindex failed", "err", err)
			return err
		}
		slog.Info("reindex done, refreshing store")
		if err := st.RefreshIndex(ctx); err != nil {
			slog.Error("refresh failed", "err", err)
			return err
		}
		slog.Info("refresh done")
		return nil
	}

	// WriteHandlers gets a reindex callback to refresh the read Store after writes
	writeH := web.WriteHandlers{
		Store:         st,
		Vault:         v,
		SlotRegistry:  slotReg,
		SessionStore:  sessionStore,
		UserStore:     userStore,
		SessionConfig: sessionConfig,
		OnAfterWrite:  reindexAndRefresh,
	}

	// Vault watcher for external edits (Obsidian, text editor, etc.)
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_ = v.Watch(ctx, func(events []vault.Event) {
			slog.Info("vault changed externally, reindexing", "events", len(events))
			if err := reindexAndRefresh(); err != nil {
				slog.Error("external reindex failed", "err", err)
			}
		})
	}()

	// Gate G3 registration order (DELIBERATE first-wins): I1's wizard +
	// sheet handlers register BEFORE F1's read handlers so GET /me serves
	// MeSheet (retiring F1's placeholder deliberately, not accidentally —
	// buildRegistryHandler keeps the first handler per pattern). F1's Me
	// stays reachable via ReadHandlers.NewMux only. The same first-wins
	// rule keeps F2's write-path stubs (/wizard/{step}, /encounter) behind
	// any same-pattern lane handler; none collide today.
	sheetH := web.SheetHandlers{Store: st, Vault: v, SessionStore: sessionStore, VaultRoot: vaultDir, Theme: theme, Scheme: scheme}
	drafts, err := web.NewDraftStore(st.AppDB())
	if err != nil {
		panic(err)
	}
	wizH := web.WizardHandlers{Store: st, Vault: v, SessionStore: sessionStore, Drafts: drafts, VaultRoot: vaultDir}
	wizH.RegisterRoutes(reg)
	sheetH.RegisterRoutes(reg)
	// Lane K amend (Phase 4, P08): VTT fragment/snapshot/state routes register
	// BEFORE F1/F2 (first-wins registry rule, same G3 precedent as I1's /me):
	// GET /vtt/{mapID} retires F1's placeholder and POST /vtt/{mapID}/state
	// retires F2's stub deliberately. No dashboard code is registered here.
	// A-VTT2: the Vault + reindex seams let POST /vtt/{mapID}/import write
	// map art + sidecar calibration and refresh the index, same pattern as
	// WriteHandlers.
	vttH := vtt.Handlers{Store: st, Sessions: sessionStore, Vault: v, OnAfterWrite: reindexAndRefresh, Theme: theme, Scheme: scheme}
	vttH.RegisterRoutes(reg)
	readH.RegisterRoutes(reg)
	writeH.RegisterRoutes(reg)

	// Ops mux handles /healthz, /version, /static/print.css + the /static/core/
	// + /static/vendor/ subtrees first, but ONLY if the registry doesn't
	// already own them (F1 registers /healthz + /version).
	mux := http.NewServeMux()
	opsMux := http.NewServeMux()
	opsMux.HandleFunc("/healthz", healthzHandler(info))
	opsMux.HandleFunc("/version", versionHandler(info))
	opsMux.HandleFunc("/static/print.css", printCSSHandler)
	opsMux.Handle("/static/core/", staticDirHandler("/static/core/", "static/core", staticCoreCache))
	opsMux.Handle("/static/vendor/", staticDirHandler("/static/vendor/", "static/vendor", staticVendorCache))
	opsMux.Handle("/static/themes/", staticDirHandler("/static/themes/", "static/themes", staticCoreCache))
	opsMux.Handle("/static/vtt/", staticDirHandler("/static/vtt/", "static/vtt", staticVTTCache))

	// Install ops handlers for paths NOT owned by the registry.
	regRoutes := reg.Routes()
	// ownedByRegistry matches request paths against registered patterns:
	// exact patterns match exactly, prefix patterns (mapped through
	// toStdlibPattern, e.g. "/p/") match their subtree. Handlers extract
	// params from r.URL.Path themselves.
	//
	// NOTE: the registry's "/" catch-all (Index, which 404s every non-root
	// path itself) prefix-matches everything here, so the check below would
	// swallow the ops-owned static paths too — /static/print.css 404d over
	// live HTTP for the same reason before W3. isPublicStatic carves the
	// embedded public subtrees back out BEFORE this check runs.
	ownedByRegistry := func(path string) bool {
		for _, rt := range regRoutes {
			stdpat := web.ToStdlibPattern(rt.Path)
			if strings.HasSuffix(stdpat, "/") {
				if strings.HasPrefix(path, stdpat) {
					return true
				}
			} else if path == stdpat {
				return true
			}
		}
		return false
	}
	// Catch-all for paths no exact ops pattern matched: the outer mux only
	// sends registry-unowned paths here, so this is a plain 404. (It must
	// NOT re-serve opsMux: that recursed infinitely and stack-overflowed
	// serve on the first page request -- G2 amend.)
	opsMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	// Build registry handler with middleware chain (auth + demo fallback).
	regHandler := reg.BuildHandler(sessionStore, sessionKeys, userStore, st)
	// Demo viewer fallback for unwired mode: real sessions take precedence.
	regHandler = withDemoViewer(regHandler)

	// Chain: public static first (carved out of the registry catch-all),
	// then registry-owned paths, then ops leftovers.
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicStatic(r.URL.Path) {
			opsMux.ServeHTTP(w, r)
		} else if ownedByRegistry(r.URL.Path) {
			regHandler.ServeHTTP(w, r)
		} else {
			opsMux.ServeHTTP(w, r)
		}
	}))
	return &serverWiring{Handler: mux, Plugins: plugReg, Store: st, Vault: v}
}

// isPublicStatic reports the embedded public-asset paths served by the ops
// mux: the print stylesheet plus the core/vendor/themes/vtt subtrees (W3 +
// A-VTT2). The registry never registers these, but its "/" catch-all
// prefix-matches everything in ownedByRegistry — so the outer mux consults
// this FIRST and unknown paths (e.g. /bogus) keep flowing to the registry's
// styled 404 untouched.
func isPublicStatic(path string) bool {
	if path == "/static/print.css" {
		return true
	}
	for _, prefix := range []string{"/static/core/", "/static/vendor/", "/static/themes/", "/static/vtt/"} {
		if path == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// withDemoViewer injects the request's demo identity (`?as=`, same resolver
// every read handler uses) into the context when no real-auth viewer is
// present. Gate G3: session-only handlers (I1 wizard/sheets via viewerOf)
// would otherwise 401 every live request — there is no HTTP login surface
// in v1, so `?as=` is the live-server identity tier (M1 smoke precedent).
// Real-auth contexts always win; identity alone never passes CSRF, so
// state-changing routes still require a session cookie + token.
func withDemoViewer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.ViewerFromContext(r.Context()); !ok {
			if v := web.ViewerForRequest(r); v != nil {
				r = r.WithContext(auth.WithViewer(r.Context(), v))
			}
		}
		next.ServeHTTP(w, r)
	})
}

type serveOptions struct {
	vault       string
	dataDir     string
	addr        string
	demoAuth    bool
	theme       string // T1: theme id ("" = unthemed; unknown fails closed)
	themeScheme string // T1: light|dark|auto ("" = light when a theme is set)
	info        buildInfo
}

// runServe opens the app DB (migrating), ensures the session key, and serves
// until SIGINT/SIGTERM. Graceful shutdown drains SSE before exit (P09).
func runServe(opts serveOptions, shutdown <-chan os.Signal) error {
	if opts.vault == "" {
		return fmt.Errorf("vault path required (--vault)")
	}
	if fi, err := os.Stat(opts.vault); err != nil || !fi.IsDir() {
		return fmt.Errorf("vault dir not found: %s (run `init --bare` first)", opts.vault)
	}
	// B3: --demo-auth (default true) keeps the ?as= demo identity tier for
	// dev/smoke/axe; false retires it (sessions only). Wired here, read by
	// every handler through web.ViewerForRequest — one gate, all paths.
	web.DemoAuth = opts.demoAuth
	dataDir := resolveDataDir(opts.dataDir, opts.vault)
	if err := ensureDir(dataDir); err != nil {
		return err
	}
	if _, err := ensureSessionKey(dataDir); err != nil {
		return err
	}
	db, appPath, err := openAppDB(dataDir, opts.vault)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	sessionStore, err := auth.NewSessionStore(db)
	if err != nil {
		return err
	}

	mux := wireHandlers(opts.info, opts.vault, dataDir, sessionStore, opts.theme, opts.themeScheme)
	server := &http.Server{
		Addr:              opts.addr,
		Handler:           mux.Handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-shutdown
		slog.Info("shutting down, draining connections")
		ctx, cancel := contextTimeout(10 * time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	slog.Info("serving", "addr", opts.addr, "app", appPath)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
