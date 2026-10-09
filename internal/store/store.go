package store

import (
	"context"
	"database/sql"

	"github.com/semiplane/yonder/internal/auth"
)

// Store defines the interface for the SQLite databases.
// Two files per process (Phase 0c, P04):
//   - <name>.index.db — disposable index (pages, blocks, links, tags, optionals),
//     rebuilt from vault via reindex (temp + rename).
//   - <name>.app.db — migrated app state (users, auth_sessions, login_attempts,
//     edits_log, characters, sessions, dice_logs, vtt_*), never rebuilt.
//
// Migrations in migrations/{index,app}/*.sql (Lane B owns contents, Lane E2
// owns the runner): index/ → <name>.index.db applied at reindex build time,
// app/ → <name>.app.db applied at serve/migrate time. Independent
// user_version streams from 1.
type Store interface {
	// Page operations. Paths are vault-relative posix page IDs: lookup is
	// case-insensitive, display preserves source case (Phase 0c).
	PageGet(ctx context.Context, path string) (*Page, error)
	PageList(ctx context.Context, opts PageListOptions) ([]*Page, error)
	PageUpsert(ctx context.Context, page *Page) error
	PageDelete(ctx context.Context, path string) error

	// Search operations
	Search(ctx context.Context, query string, opts SearchOptions) ([]*SearchResult, error)

	// Graph/backlink operations
	Backlinks(ctx context.Context, path string) ([]string, error)
	ForwardLinks(ctx context.Context, path string) ([]string, error)

	// Asset operations
	AssetGet(ctx context.Context, path string) (*Asset, error)
	AssetList(ctx context.Context, opts AssetListOptions) ([]*Asset, error)
	AssetUpsert(ctx context.Context, asset *Asset) error
	AssetDelete(ctx context.Context, path string) error

	// Transaction support for batch operations
	Transact(ctx context.Context, fn func(tx Store) error) error

	// IndexDB returns the disposable index database (<name>.index.db).
	IndexDB() *sql.DB

	// AppDB returns the migrated app database (<name>.app.db).
	AppDB() *sql.DB

	// RefreshIndex closes and reopens the index database connection.
	// Called after reindex to pick up the new index file (which was
	// atomically renamed over the old one).
	RefreshIndex(ctx context.Context) error

	// Close closes the database connections.
	Close() error
}

// NewStore is implemented in sqlite.go (Open over index + app files).
// Files: <data-dir>/<name>.index.db (disposable) + <data-dir>/<name>.app.db
// (migrated). dataDir defaults to sibling <vault>-data/ (never inside vault).

// Page represents an indexed markdown page (index DB row).
// Intentional projection of markdown.Page (not a shared type): Lane B owns
// this index shape and uses markdown.Parse fakes in Phase 1, never importing
// Lane A's package. Lane F1 owns the parse→index conversion at write time.
type Page struct {
	Path        string
	Title       string
	Content     string
	Frontmatter map[string]any
	Secret      bool
	Owner       string
	EditableBy  []string
	UpdatedAt   int64
	Hash        string
}

// PageListOptions controls page listing.
type PageListOptions struct {
	Prefix        string
	Limit         int
	Offset        int
	IncludeSecret bool // for GM/owner only
}

// SearchOptions controls search behavior.
type SearchOptions struct {
	Query         string
	Limit         int
	Offset        int
	Viewer        *auth.Viewer // for secret filtering
	IncludeSecret bool
}

// SearchResult represents a search hit (index-DB projection).
// secrets.SearchResult mirrors this shape for filtering for the same
// lane-isolation reason as Page above; Lane F1 converts at the read path.
type SearchResult struct {
	Path    string
	Title   string
	Snippet string
	Score   float64
	Secret  bool
}

// Asset represents a vault asset (image, pdf, etc.).
type Asset struct {
	Path      string
	MIMEType  string
	Size      int64
	Hash      string
	UpdatedAt int64
}

// AssetListOptions controls asset listing.
type AssetListOptions struct {
	Prefix string
	Limit  int
	Offset int
}
