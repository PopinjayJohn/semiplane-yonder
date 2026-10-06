package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"
)

//go:embed static/print.css
var staticFS embed.FS

// Ops HTTP surface owned by Lane E1 (P09): /healthz + /version expose the
// binary version (ldflags-stamped) ONLY — never vault names, counts, or any
// vault-derived data. All other paths fall through to the Phase-0 route
// registry once Lane F1/F2 handlers land (currently a 501 placeholder).
//
// Contract note: ReadHandlers already registers /healthz + /version in the
// frozen registry with stub handlers. E1 serves these two paths on its own
// mux without touching internal/web (another lane's files). When F1
// implements them, one registration must go (amend request in report).

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

// newOpsMux builds the E1 serve mux: ops endpoints first, then the registry
// fallback (nil until Lane F1/F2 wire real handlers).
func newOpsMux(info buildInfo, fallback http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthzHandler(info))
	mux.HandleFunc("/version", versionHandler(info))
	mux.HandleFunc("/static/print.css", printCSSHandler)
	if fallback != nil {
		mux.Handle("/", fallback)
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not wired yet (read/write handlers land in Phase 2)", http.StatusNotImplemented)
		})
	}
	return mux
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

	mux := newOpsMux(opts.info, nil) // registry fallback wires in Phase 2
	server := &http.Server{
		Addr:              opts.addr,
		Handler:           mux,
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
