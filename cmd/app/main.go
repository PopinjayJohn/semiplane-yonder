package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Single-binary entry point (Lane E1, P09). One process serves one vault.
//
// Usage:
//
//	yonder [--vault DIR] [--data-dir DIR] [--addr ADDR] <command> [flags]
//	yonder --vault DIR                       (serve; default command)
//
// Commands: serve, init, reindex, reset-password, rotate-session-key,
// transfer-ownership, archive-vault, clone-vault, rules lint,
// vendor verify, version.
//
// Flags beat env beat defaults. Logging is JSON (slog); secret material
// (passwords, keys) is never logged.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func build() buildInfo {
	return buildInfo{Version: version, Commit: commit, Date: date}
}

func main() {
	// slog JSON to stderr per spec §8 (no secret content in logs).
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(os.Args[1:]); err != nil {
		slog.Error("yonder", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	var (
		vaultPath string
		dataDir   string
		addr      string
		showVer   bool
		// Legacy Phase-0 flat flags (kept working; subcommands preferred).
		legacyInitBare  bool
		legacyInitTmpl  string
		legacyReindex   bool
		legacyRotateKey bool
		legacyResetPass string
		legacyTransfer  string
	)
	fs := flag.NewFlagSet("yonder", flag.ContinueOnError)
	fs.StringVar(&vaultPath, "vault", "", "Path to vault directory")
	fs.StringVar(&dataDir, "data-dir", "", "Data dir (default: sibling <vault>-data/, never inside the vault)")
	fs.StringVar(&addr, "addr", ":8080", "Listen address")
	fs.BoolVar(&showVer, "version", false, "Print version and exit")
	fs.BoolVar(&legacyInitBare, "init-bare", false, "Legacy: init bare vault (use `init --bare`)")
	fs.StringVar(&legacyInitTmpl, "init-template", "", "Legacy: init from template URL (use `init --template`)")
	fs.BoolVar(&legacyReindex, "reindex", false, "Legacy: rebuild index (use `reindex`)")
	fs.BoolVar(&legacyRotateKey, "rotate-session-key", false, "Legacy: rotate key (use `rotate-session-key`)")
	fs.StringVar(&legacyResetPass, "reset-password", "", "Legacy: user to reset (use `reset-password --user`)")
	fs.StringVar(&legacyTransfer, "transfer-ownership", "", "Legacy: old:new (use `transfer-ownership old:new`)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()

	if showVer || (len(rest) == 1 && rest[0] == "version") {
		info := build()
		fmt.Printf("yonder %s (%s) %s\n", info.Version, info.Commit, info.Date)
		return nil
	}

	// Legacy flat-flag compatibility.
	switch {
	case legacyInitBare || legacyInitTmpl != "":
		return runInit(initOptions{vault: vaultPath, dataDir: dataDir, bare: true, templateURL: legacyInitTmpl})
	case legacyReindex:
		return runReindex(vaultPath, dataDir)
	case legacyRotateKey:
		return runRotateSessionKey(vaultPath, dataDir)
	case legacyResetPass != "":
		return runResetPassword(vaultPath, dataDir, legacyResetPass, "")
	case legacyTransfer != "":
		oldUser, newUser := splitPair(legacyTransfer)
		return runTransferOwnership(vaultPath, oldUser, newUser)
	}

	if len(rest) == 0 {
		if vaultPath == "" {
			usage()
			return fmt.Errorf("vault path required")
		}
		return serveDefault(vaultPath, dataDir, addr)
	}

	switch rest[0] {
	case "serve":
		return runServeCommand(vaultPath, dataDir, addr, rest[1:])
	case "init":
		return runInitCommand(vaultPath, dataDir, rest[1:])
	case "reindex":
		return runReindexCommand(vaultPath, dataDir, rest[1:])
	case "reset-password":
		return runResetPasswordCommand(vaultPath, dataDir, rest[1:])
	case "rotate-session-key":
		return runRotateCommand(vaultPath, dataDir, rest[1:])
	case "transfer-ownership":
		return runTransferOwnershipCommand(vaultPath, rest[1:])
	case "archive-vault":
		return runArchiveCommand(vaultPath, rest[1:])
	case "clone-vault":
		return runCloneCommand(vaultPath, rest[1:])
	case "rules":
		return runRulesCommand(vaultPath, rest[1:])
	case "vendor":
		return runVendorCommand(rest[1:])
	default:
		usage()
		return fmt.Errorf("unknown command %q", rest[0])
	}
}

// vaultDataFlags binds the shared --vault/--data-dir flags to a subcommand
// FlagSet so both `yonder --vault V reindex` and `yonder reindex --vault V`
// (AGENTS.md form) work. Flags beat env beat defaults.
func vaultDataFlags(fs *flag.FlagSet, vaultPath, dataDir *string) {
	vaultFlag(fs, vaultPath)
	fs.StringVar(dataDir, "data-dir", *dataDir, "Data dir")
}

// vaultFlag binds only --vault (for commands with no data-dir use).
func vaultFlag(fs *flag.FlagSet, vaultPath *string) {
	fs.StringVar(vaultPath, "vault", *vaultPath, "Path to vault directory")
}

func serveDefault(vaultPath, dataDir, addr string) error {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	return runServe(serveOptions{vault: vaultPath, dataDir: dataDir, addr: addr, info: build()}, sig)
}

func runServeCommand(vaultPath, dataDir, addr string, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	vaultDataFlags(fs, &vaultPath, &dataDir)
	fs.StringVar(&addr, "addr", addr, "Listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return serveDefault(vaultPath, dataDir, addr)
}

func runInitCommand(vaultPath, dataDir string, args []string) error {
	var opts initOptions
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.StringVar(&vaultPath, "vault", vaultPath, "Path to vault directory")
	fs.StringVar(&opts.dataDir, "data-dir", dataDir, "Data dir")
	fs.BoolVar(&opts.bare, "bare", false, "Offline scaffold (campaign.yaml, folders, .gitignore, GM account)")
	fs.StringVar(&opts.templateURL, "template", "", "Template zip URL (https, checksum-verified)")
	fs.StringVar(&opts.templateSHA, "template-sha256", "", "Expected sha256 of template zip")
	fs.DurationVar(&opts.cacheTTL, "template-cache-ttl", 24*time.Hour, "Template download cache TTL")
	fs.StringVar(&opts.gmUser, "gm-user", "", "GM username (default gm)")
	fs.StringVar(&opts.gmPassword, "gm-password", "", "GM password (or YONDER_GM_PASSWORD, or prompt)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opts.vault = vaultPath
	if !opts.bare && opts.templateURL == "" {
		return fmt.Errorf("init requires --bare or --template <https-url>")
	}
	return runInit(opts)
}

func runReindexCommand(vaultPath, dataDir string, args []string) error {
	fs := flag.NewFlagSet("reindex", flag.ContinueOnError)
	vaultDataFlags(fs, &vaultPath, &dataDir)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runReindex(vaultPath, dataDir)
}

func runRotateCommand(vaultPath, dataDir string, args []string) error {
	fs := flag.NewFlagSet("rotate-session-key", flag.ContinueOnError)
	vaultDataFlags(fs, &vaultPath, &dataDir)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runRotateSessionKey(vaultPath, dataDir)
}

func runResetPasswordCommand(vaultPath, dataDir string, args []string) error {
	var user, newPass string
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	vaultDataFlags(fs, &vaultPath, &dataDir)
	fs.StringVar(&user, "user", "", "Username")
	fs.StringVar(&newPass, "new-password", "", "New password (or YONDER_NEW_PASSWORD, or prompt)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runResetPassword(vaultPath, dataDir, user, newPass)
}

func runTransferOwnershipCommand(vaultPath string, args []string) error {
	fs := flag.NewFlagSet("transfer-ownership", flag.ContinueOnError)
	var from, to string
	vaultFlag(fs, &vaultPath)
	fs.StringVar(&from, "from", "", "Current owner username")
	fs.StringVar(&to, "to", "", "New owner username")
	if err := fs.Parse(args); err != nil {
		return err
	}
	oldUser, newUser := from, to
	if rest := fs.Args(); len(rest) == 1 && from == "" && to == "" {
		oldUser, newUser = splitPair(rest[0])
	}
	return runTransferOwnership(vaultPath, oldUser, newUser)
}

func runArchiveCommand(vaultPath string, args []string) error {
	var out string
	fs := flag.NewFlagSet("archive-vault", flag.ContinueOnError)
	vaultFlag(fs, &vaultPath)
	fs.StringVar(&out, "out", "", "Output zip path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runArchiveVault(vaultPath, out)
}

func runCloneCommand(vaultPath string, args []string) error {
	var dest string
	fs := flag.NewFlagSet("clone-vault", flag.ContinueOnError)
	vaultFlag(fs, &vaultPath)
	fs.StringVar(&dest, "dest", "", "Destination directory (must be missing or empty)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 1 && vaultPath == "" {
		vaultPath = fs.Arg(0)
	}
	if fs.NArg() == 2 && vaultPath == "" && dest == "" {
		vaultPath, dest = fs.Arg(0), fs.Arg(1)
	}
	return runCloneVault(vaultPath, dest)
}

func runRulesCommand(vaultPath string, args []string) error {
	if len(args) == 0 || args[0] != "lint" {
		return fmt.Errorf("usage: rules lint [--vault DIR] [dir]")
	}
	fs := flag.NewFlagSet("rules lint", flag.ContinueOnError)
	vaultFlag(fs, &vaultPath)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	dir := ""
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	return runRulesLint(vaultPath, dir)
}

func runVendorCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: vendor (verify|sha256) [...]")
	}
	switch args[0] {
	case "verify":
		fs := flag.NewFlagSet("vendor verify", flag.ContinueOnError)
		vendorDir := fs.String("vendor-dir", "", "Vendor dir (default: web/static/vendor)")
		fetch := fs.Bool("fetch", false, "Also re-download URLs and compare (needs internet)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		dir, versionsPath, err := findVersions(*vendorDir)
		if err != nil {
			return err
		}
		return runVendorVerify(dir, versionsPath, *fetch)
	case "sha256":
		if len(args) != 2 {
			return fmt.Errorf("usage: vendor sha256 <https-url>")
		}
		sum, err := downloadSHA256(args[1])
		if err != nil {
			return err
		}
		fmt.Println(sum)
		return nil
	default:
		return fmt.Errorf("unknown vendor subcommand %q", args[0])
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `yonder — single-binary TTRPG wiki/campaign/VTT
usage: yonder [--vault DIR] [--data-dir DIR] [--addr ADDR] <command> [flags]
  serve | init --bare | reindex | reset-password | rotate-session-key |
  transfer-ownership | archive-vault | clone-vault | rules lint |
  vendor (verify|sha256) | version`)
}

func splitPair(s string) (string, string) {
	for i, c := range s {
		if c == ':' {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}
