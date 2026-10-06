package main

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

//go:embed scaffold/campaign.yaml.tmpl scaffold/README.md.tmpl
var scaffoldFS embed.FS

// scaffoldFolders are the special vault folders created by init --bare.
// Each gets a .keep so empty folders survive git and sync tools.
var scaffoldFolders = []string{
	"characters",
	"sessions",
	"quests",
	"maps",
	"assets",
	"rules",
	"notes",
}

// vaultGitignore is the managed vault .gitignore (P09): ignores data
// artifacts with explicit !.keep negations. Dotfiles are otherwise
// respected — .obsidian/ and .keep keep working (never blanket-ignored).
const vaultGitignore = `# yonder vault (managed by ` + "`yonder init`" + ` — safe to extend below).
*.db
.sessionkey
!.keep
`

type initOptions struct {
	vault       string
	dataDir     string
	bare        bool
	templateURL string
	templateSHA string
	cacheTTL    time.Duration
	gmUser      string
	gmPassword  string // from flag/env/prompt; never logged
}

// runInit scaffolds a vault (bare or from template), ensures the data dir +
// session key, runs app migrations, and creates the GM account.
func runInit(opts initOptions) error {
	if opts.vault == "" {
		return fmt.Errorf("vault path required (--vault)")
	}
	vaultPath := filepath.Clean(opts.vault)

	if opts.templateURL != "" {
		if err := ensureDir(vaultPath); err != nil {
			return err
		}
		if err := fetchTemplate(vaultPath, opts.templateURL, opts.templateSHA, opts.cacheTTL); err != nil {
			return err
		}
	} else {
		if err := scaffoldBareVault(vaultPath); err != nil {
			return err
		}
	}

	dataDir := resolveDataDir(opts.dataDir, vaultPath)
	if err := ensureDir(dataDir); err != nil {
		return err
	}
	keyPath, err := ensureSessionKey(dataDir)
	if err != nil {
		return err
	}

	db, appPath, err := openAppDB(dataDir, vaultPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	gmUser := opts.gmUser
	if gmUser == "" {
		gmUser = "gm"
	}
	pw := opts.gmPassword
	if pw == "" {
		pw = os.Getenv("YONDER_GM_PASSWORD")
	}
	if pw == "" {
		var err error
		pw, err = promptPassword("GM password: ")
		if err != nil {
			return err
		}
	}
	hash, err := hashPassword(pw)
	if err != nil {
		return err
	}
	if err := createGMUser(db, gmUser, hash, time.Now().Unix()); err != nil {
		return err
	}

	fmt.Printf("initialized vault %s\n  data: %s\n  app db: %s\n  session key: %s\n  GM user: %s\n",
		vaultPath, dataDir, appPath, keyPath, gmUser)
	return nil
}

// scaffoldBareVault creates campaign.yaml, README.md, all special folders
// with .keep files, and the vault .gitignore. Refuses non-empty dirs.
func scaffoldBareVault(vaultPath string) error {
	if err := ensureDir(vaultPath); err != nil {
		return err
	}
	empty, err := dirIsEmpty(vaultPath)
	if err != nil {
		return err
	}
	if !empty {
		return fmt.Errorf("vault dir %s is not empty (refusing to scaffold over existing files)", vaultPath)
	}
	for _, folder := range scaffoldFolders {
		dir := filepath.Join(vaultPath, folder)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, ".keep"), nil, 0o644); err != nil {
			return err
		}
	}
	vars := map[string]string{
		"Name":    filepath.Base(filepath.Clean(vaultPath)),
		"Created": time.Now().UTC().Format(time.RFC3339),
	}
	for _, tmpl := range []struct{ src, dst string }{
		{"scaffold/campaign.yaml.tmpl", "campaign.yaml"},
		{"scaffold/README.md.tmpl", "README.md"},
	} {
		t, err := template.ParseFS(scaffoldFS, tmpl.src)
		if err != nil {
			return err
		}
		var b strings.Builder
		if err := t.Execute(&b, vars); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(vaultPath, tmpl.dst), []byte(b.String()), 0o644); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(vaultPath, ".gitignore"), []byte(vaultGitignore), 0o644); err != nil {
		return err
	}
	return nil
}
