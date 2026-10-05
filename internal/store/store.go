package store

import (
	"context"
	"database/sql"

	"github.com/semiplane/yonder/internal/auth"
)

// Store defines the interface for the SQLite index database.
// The index DB is disposable (rebuilt from vault on reindex).
// The app DB (auth, ownership, VTT live state) is separate and migrated.
type Store interface {
	// Page operations
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

	// Database access for migrations
	DB() *sql.DB

	// Close closes the database connections.
	Close() error
}

// NewStore creates a new store instance with index and app databases.
func NewStore(dataDir, vaultPath string) (Store, error) {
	return nil, nil // not implemented
}

// Page represents an indexed markdown page.
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

// SearchResult represents a search hit.
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
