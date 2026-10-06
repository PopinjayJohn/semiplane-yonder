package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/dice"
	"github.com/semiplane/yonder/internal/plugins"
	"github.com/semiplane/yonder/internal/ruleset"
	"github.com/semiplane/yonder/internal/store"
	"github.com/semiplane/yonder/internal/vault"
	"github.com/semiplane/yonder/internal/web"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	var (
		vaultPath   string
		dataDir     string
		addr        string
		initBare    bool
		initTmpl    string
		reindex     bool
		rotateKey   bool
		resetPass   string
		transferOwn string
		printVer    bool
	)
	flag.StringVar(&vaultPath, "vault", "", "Path to vault directory (required)")
	flag.StringVar(&dataDir, "data-dir", "", "Path to data directory (default: sibling <vault>-data/, never inside the vault)")
	flag.StringVar(&addr, "addr", ":8080", "Listen address")
	flag.BoolVar(&initBare, "init-bare", false, "Initialize bare vault + GM user")
	flag.StringVar(&initTmpl, "init-template", "", "Initialize from template (pinned URL)")
	flag.BoolVar(&reindex, "reindex", false, "Rebuild index database")
	flag.BoolVar(&rotateKey, "rotate-session-key", false, "Rotate session signing key")
	flag.StringVar(&resetPass, "reset-password", "", "Reset password for user")
	flag.StringVar(&transferOwn, "transfer-ownership", "", "Transfer page/character ownership old-user:new-user (Phase 0c; Lane E1 implements)")
	flag.BoolVar(&printVer, "version", false, "Print version and exit")
	flag.Parse()

	if printVer {
		fmt.Printf("yonder %s (%s) %s\n", version, commit, date)
		os.Exit(0)
	}

	if vaultPath == "" && !printVer {
		slog.Error("vault path required")
		flag.Usage()
		os.Exit(1)
	}

	// Phase 0c: one process serves one vault; data files live beside the
	// vault, never inside it. transfer-ownership dispatch lands in Lane E1.
	_ = transferOwn
	if dataDir == "" && vaultPath != "" {
		dataDir = vaultPath + "-data"
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Initialize vault
	v, err := vault.NewVault(vaultPath)
	if err != nil {
		slog.Error("vault init", "error", err)
		os.Exit(1)
	}

	// Initialize store (index DB + app DB)
	s, err := store.NewStore(dataDir, vaultPath)
	if err != nil {
		slog.Error("store init", "error", err)
		os.Exit(1)
	}
	defer s.Close()

	// Run app migrations (<name>.app.db; never rebuilt). Index schema
	// (migrations/index/) is applied by the reindex path to a temp DB.
	migrator := store.NewMigrationRunner(s.AppDB())
	if err := migrator.RunApp(ctx); err != nil {
		slog.Error("migrations", "error", err)
		os.Exit(1)
	}

	// Initialize session store (auth_sessions lives in the app DB per P04)
	sessionStore, err := auth.NewSessionStore(s.AppDB())
	if err != nil {
		slog.Error("session store init", "error", err)
		os.Exit(1)
	}

	// Generate or load session key
	_, err = auth.LoadOrGenerateKey(dataDir)
	if err != nil {
		slog.Error("session key", "error", err)
		os.Exit(1)
	}

	// Initialize dice transport (dice_logs lives in the app DB per P04)
	roller := dice.NewRoller()
	diceStore := dice.NewLogStore(s.AppDB())
	_ = dice.NewTransportService(roller, diceStore, dice.DefaultTransportConfig())

	// Initialize ruleset engine
	_ = ruleset.NewEngine()

	// Initialize plugins
	_ = plugins.NewRegistry(nil, nil)

	// Initialize slot and route registries
	slotRegistry := web.NewSlotRegistry()
	routeRegistry := web.NewRouteRegistry()

	// Initialize handlers
	readHandlers := &web.ReadHandlers{
		Store:        s,
		SlotRegistry: slotRegistry,
		SessionStore: sessionStore,
	}
	writeHandlers := &web.WriteHandlers{
		Store:        s,
		Vault:        v,
		SessionStore: sessionStore,
		SlotRegistry: slotRegistry,
	}

	// Register routes
	readHandlers.RegisterRoutes(routeRegistry)
	writeHandlers.RegisterRoutes(routeRegistry)

	// Build HTTP handler
	handler := routeRegistry.BuildHandler(sessionStore)

	// Start server
	server := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	go func() {
		slog.Info("starting server", "addr", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			cancel()
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	server.Shutdown(shutdownCtx)
}
