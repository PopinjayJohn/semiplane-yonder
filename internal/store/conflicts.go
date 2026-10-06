package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/semiplane/yonder/internal/auth"
)

// ConflictRow mirrors one conflicts row: a *.conflict-<ts>.md file indexed
// with its parent page's ACL. Conflict rows are never returned by Search or
// PageList — access is explicit via these helpers so ACL checks stay with the
// caller (server-side, every path).
type ConflictRow struct {
	Path       string
	ParentPath string
	Creator    string
	CreatedAt  int64
	Owner      string
	Secret     bool
	EditableBy []string
}

// ListConflicts returns conflict rows for a parent page (or all, when parent
// is empty), newest first.
func ListConflicts(ctx context.Context, db *sql.DB, parent string) ([]*ConflictRow, error) {
	q := `SELECT path,parent_path,creator,created_at,owner,secret,editable_by FROM conflicts`
	var args []any
	if parent != "" {
		q += ` WHERE parent_path = ?`
		args = append(args, parent)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*ConflictRow
	for rows.Next() {
		var c ConflictRow
		var secret int
		var editableBy string
		if err := rows.Scan(&c.Path, &c.ParentPath, &c.Creator, &c.CreatedAt, &c.Owner, &secret, &editableBy); err != nil {
			return nil, err
		}
		c.Secret = secret != 0
		_ = json.Unmarshal([]byte(editableBy), &c.EditableBy)
		out = append(out, &c)
	}
	return out, rows.Err()
}

// ConflictVisible reports whether viewer v may see the conflict row. A
// conflict inherits the parent ACL: same rule as PageVisible on the copied
// owner/secret/editable_by (orphans are secret + ownerless ⇒ GM-only).
func ConflictVisible(c *ConflictRow, v *auth.Viewer) bool {
	eb, _ := json.Marshal(c.EditableBy)
	return PageVisible(c.Secret, c.ParentPath, c.Owner, string(eb), v)
}
