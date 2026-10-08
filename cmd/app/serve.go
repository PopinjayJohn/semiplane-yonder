package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/campaign"
	_ "github.com/semiplane/yonder/internal/markdown" // ensure parser registration
	"github.com/semiplane/yonder/internal/plugins"
	"github.com/semiplane/yonder/internal/store"
	"github.com/semiplane/yonder/internal/vault"
	"github.com/semiplane/yonder/internal/web"
)

//go:embed static/print.css
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
func wireHandlers(info buildInfo, vaultDir, dataDir string, sessionStore auth.SessionStore) *serverWiring {
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
	// Gate G3: plugin registries live in serve (E1 file, amend-authorized).
	// web owns the append-only slot/route registries; plugins.Registry adds
	// enablement gating on top. Shell read paths render slots through
	// plugReg.SlotsFor (via the web.SlotProvider seam), so a disabled plugin
	// leaves no UI trace. No plugins are compiled in yet (random-tables
	// ships data + slot shape, no Plugin impl), so the registry starts
	// empty; campaign.yaml enabled-plugins resolve against it below.
	slotReg := web.NewSlotRegistry()
	reg := web.NewRouteRegistry()
	plugReg := plugins.NewRegistry(slotReg, reg)
	enablePlugins(vaultDir, plugReg)
	readH := web.ReadHandlers{Store: st, SlotRegistry: slotReg, Slots: plugReg, SessionStore: sessionStore}
	writeH := web.WriteHandlers{Store: st, Vault: v, SlotRegistry: slotReg, SessionStore: sessionStore}

	// Gate G3 registration order (DELIBERATE first-wins): I1's wizard +
	// sheet handlers register BEFORE F1's read handlers so GET /me serves
	// MeSheet (retiring F1's placeholder deliberately, not accidentally —
	// buildRegistryHandler keeps the first handler per pattern). F1's Me
	// stays reachable via ReadHandlers.NewMux only. The same first-wins
	// rule keeps F2's write-path stubs (/wizard/{step}, /encounter) behind
	// any same-pattern lane handler; none collide today.
	sheetH := web.SheetHandlers{Store: st, Vault: v, SessionStore: sessionStore, VaultRoot: vaultDir}
	drafts, err := web.NewDraftStore(st.AppDB())
	if err != nil {
		panic(err)
	}
	wizH := web.WizardHandlers{Store: st, Vault: v, SessionStore: sessionStore, Drafts: drafts, VaultRoot: vaultDir}
	wizH.RegisterRoutes(reg)
	sheetH.RegisterRoutes(reg)
	readH.RegisterRoutes(reg)
	writeH.RegisterRoutes(reg)

	// Ops mux handles /healthz, /version, /static/print.css first,
	// but ONLY if the registry doesn't already have them (F1 registers both).
	mux := http.NewServeMux()
	opsMux := http.NewServeMux()
	opsMux.HandleFunc("/healthz", healthzHandler(info))
	opsMux.HandleFunc("/version", versionHandler(info))
	opsMux.HandleFunc("/static/print.css", printCSSHandler)

	// Install ops handlers for paths NOT owned by the registry.
	regRoutes := reg.Routes()
	// ownedByRegistry matches request paths against registered patterns:
	// exact patterns match exactly, prefix patterns (mapped through
	// toStdlibPattern, e.g. "/p/") match their subtree. Handlers extract
	// params from r.URL.Path themselves.
	ownedByRegistry := func(path string) bool {
		for _, rt := range regRoutes {
			stdpat := toStdlibPattern(rt.Path)
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

	// Build registry handler with middleware chain.
	regHandler := reg.BuildHandler(sessionStore)
	if regHandler == nil {
		// Fallback: build from routes directly (Phase 2: BuildHandler is stub).
		regHandler = buildRegistryHandler(regRoutes, sessionStore)
	}

	// Chain: ops-first for unowned paths, then registry.
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ownedByRegistry(r.URL.Path) {
			regHandler.ServeHTTP(w, r)
		} else {
			opsMux.ServeHTTP(w, r)
		}
	}))
	return &serverWiring{Handler: mux, Plugins: plugReg, Store: st, Vault: v}
}

// buildRegistryHandler constructs an http.Handler from registered routes
// with session/viewer middleware (inline until RouteRegistry.BuildHandler
// is implemented). stdlib ServeMux (Go 1.22+) supports {name} and {name...}
// only at pattern END; frozen routes use {path...} mid-pattern. We map them
// to prefix patterns. Duplicate method+pattern pairs keep the first
// registered handler — that first-wins rule is what retires F1's GET /me
// placeholder in favor of I1's MeSheet (deliberate registration order in
// wireHandlers, never accidental).
//
// Gate G3: dedup keys on METHOD + pattern (stdlib "METHOD /path" patterns).
// The old pattern-only collapse shadowed every POST twin behind its GET
// registration (POST /wizard/setup, POST /c/..., F2 PageSave) — writes were
// unreachable over the mux. Method-aware routing restores them; CSRF +
// login still gate every write.
func buildRegistryHandler(routes []web.Route, sessionStore auth.SessionStore) http.Handler {
	mux := http.NewServeMux()
	seen := make(map[string]bool)
	for _, rt := range routes {
		h := rt.Handler
		if h == nil {
			continue
		}
		// For Phase 2, let handlers resolve their own viewer (demo fallback).
		// Real auth middleware lands in P11.
		pattern := toStdlibPattern(rt.Path)
		if rt.Method != "" {
			pattern = rt.Method + " " + pattern
		}
		if seen[pattern] {
			continue // skip duplicate method+pattern; first handler wins
		}
		seen[pattern] = true
		mux.HandleFunc(pattern, h)
	}
	return withDemoViewer(mux)
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

// toStdlibPattern converts frozen route patterns (using chi-style {path...}
// mid-pattern) to stdlib ServeMux-compatible patterns (Go 1.22+).
// Handlers extract the full path from r.URL.Path themselves.
func toStdlibPattern(pattern string) string {
	// Patterns with {path...} not at end → register the static prefix.
	// Handlers do exact matching on r.URL.Path.
	switch pattern {
	case web.RoutePageView: // "/p/{path...}" → "/p/"
		return "/p/"
	case web.RoutePageEdit: // "/p/{path...}/edit" → handled by PageEdit via /p/
		return "/p/"
	case web.RoutePageHistory: // "/p/{path...}/history" → handled by PageView via /p/
		return "/p/"
	case web.RoutePageHistory + "/revert": // "/p/{path...}/history/revert" → "/p/"
		return "/p/"
	case web.RouteAssets: // "/assets/{path...}" → "/assets/"
		return "/assets/"
	case web.RouteVTT: // "/vtt/{mapID}" → "/vtt/"
		return "/vtt/"
	case web.RouteWizard: // "/wizard/{step}" → "/wizard/"
		return "/wizard/"
	default:
		return pattern // exact patterns like /healthz, /search, /p/new, /upload, etc.
	}
}

type serveOptions struct {
	vault   string
	dataDir string
	addr    string
	info    buildInfo
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

	mux := wireHandlers(opts.info, opts.vault, dataDir, sessionStore)
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
