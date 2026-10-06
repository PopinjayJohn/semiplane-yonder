package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Data-file layout (Phase 0c): one process serves one vault; the data dir
// holds <name>.index.db (disposable) + <name>.app.db (migrated) +
// .sessionkey (0600, never in the vault, never logged).
//
// This mirrors the store.DataPaths contract (still a Phase-0 stub) so
// init/serve/reindex work in Phase 1. Lane B owns the canonical
// implementation; this copy is deleted when store.DataPaths lands
// (tracked as a contract note in the Lane E1 report).

// resolveDataDir returns dataDir, or the default sibling <vault>-data/
// when empty. The data dir is never inside the vault.
func resolveDataDir(dataDir, vaultPath string) string {
	if dataDir != "" {
		return dataDir
	}
	return vaultPath + "-data"
}

// dataPaths derives index/app/key paths. Pure computation; creates nothing.
func dataPaths(dataDir, vaultPath string) (indexPath, appPath, keyPath string) {
	dir := resolveDataDir(dataDir, vaultPath)
	name := sanitizeDBName(filepath.Base(filepath.Clean(vaultPath)))
	return filepath.Join(dir, name+".index.db"),
		filepath.Join(dir, name+".app.db"),
		filepath.Join(dir, ".sessionkey")
}

// sanitizeDBName maps a vault base name to a safe <name> for data files:
// lowercased, path separators and Windows-reserved characters replaced,
// reserved device names prefixed, length capped. Windows-safe per AGENTS.md.
func sanitizeDBName(base string) string {
	lowered := strings.ToLower(base)
	var b strings.Builder
	for _, r := range lowered {
		switch {
		case r == '/' || r == '\\' || r == ':':
			b.WriteByte('-')
		case r == '<' || r == '>' || r == '"' || r == '|' || r == '?' || r == '*':
			b.WriteByte('-')
		case r < 0x20 || r == 0x7f:
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), ".- ")
	if out == "" {
		out = "vault"
	}
	if len(out) > 100 {
		out = out[:100]
	}
	if isWindowsReserved(stripExt(out)) {
		out = "_" + out
	}
	return out
}

func stripExt(s string) string {
	if i := strings.LastIndexByte(s, '.'); i > 0 {
		return s[:i]
	}
	return s
}

// isWindowsReserved reports DOS device names (CON, PRN, AUX, NUL,
// COM1-9, LPT1-9), case-insensitive, extension already stripped.
func isWindowsReserved(s string) bool {
	upper := strings.ToUpper(s)
	switch upper {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) {
		c := upper[3]
		return c >= '1' && c <= '9'
	}
	return false
}

// ensureDir creates dir (and parents) with 0755, or verifies an existing
// path is a directory.
func ensureDir(dir string) error {
	fi, err := os.Stat(dir)
	if err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("not a directory: %s", dir)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(dir, 0o755)
}

// dirIsEmpty reports whether dir exists and contains no entries.
func dirIsEmpty(dir string) (bool, error) {
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	_, err = f.Readdirnames(1)
	if err != nil {
		if err.Error() == "EOF" {
			return true, nil
		}
		return false, err
	}
	return false, nil
}
