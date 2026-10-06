package store

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
	return "", "", "" // not implemented
}
