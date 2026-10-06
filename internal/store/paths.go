package store

import (
	"fmt"
	"path"
	"strings"
)

// DataPaths derives the on-disk data files for one vault/campaign (Phase 0c:
// one process serves one vault; files live in <data-dir>, never in the vault).
//   - indexPath: <data-dir>/<name>.index.db (disposable index)
//   - appPath: <data-dir>/<name>.app.db (migrated app state)
//   - keyPath: <data-dir>/.sessionkey (0600, never logged)
//
// <name> is the vault directory base name, lowercased, with path separators
// and Windows-reserved characters replaced. An empty dataDir selects the
// default sibling <vault>-data/ (p09). Pure path computation; creates nothing.
func DataPaths(dataDir, vaultPath string) (indexPath, appPath, keyPath string) {
	name := dbName(vaultPath)
	if dataDir == "" {
		dataDir = vaultPath + "-data"
	}
	return dataDir + "/" + name + ".index.db",
		dataDir + "/" + name + ".app.db",
		dataDir + "/.sessionkey"
}

// dbName derives the <name> file stem from a vault path. Windows-safe:
// backslashes become slashes, the base is lowercased, and characters illegal
// or reserved on Windows (<>:\"/\|?* + control chars, trailing dots/spaces,
// reserved device names) become '_'. Never empty ("vault" fallback).
func dbName(vaultPath string) string {
	p := strings.ReplaceAll(vaultPath, "\\", "/")
	p = strings.TrimRight(p, "/")
	base := path.Base(p)
	if base == "" || base == "." || base == "/" {
		base = "vault"
	}
	base = strings.ToLower(base)
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	name := strings.Trim(b.String(), "._ ")
	name = strings.TrimRight(name, ".")
	if name == "" || reservedName(name) {
		name = "_" + name
		if name == "_" {
			name = "_vault"
		}
	}
	return name
}

// reservedName reports Windows reserved device names (CON, PRN, AUX, NUL,
// COM1-9, LPT1-9), matched on the stem before any dot, case-insensitively.
func reservedName(name string) bool {
	stem := name
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(stem) == 4 {
		head, tail := strings.ToUpper(stem[:3]), stem[3]
		if (head == "COM" || head == "LPT") && tail >= '1' && tail <= '9' {
			return true
		}
	}
	return false
}

// IndexTempPath returns the temp build path for atomic reindex: the temp DB
// is created in the same directory as the live index (same volume, so rename
// is atomic) and renamed over it. pid keeps concurrent builders apart.
func IndexTempPath(indexPath string, pid int) string {
	return fmt.Sprintf("%s.tmp.%d", indexPath, pid)
}
