package web

import (
	"archive/zip"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Vault-zip HTTP export (P09/P13 promise, B1 backlog item): GM-only, logged,
// one-click vault download served from the dashboard.
//
// Secret posture: GM-only means the requester is authorized for every page,
// so no per-page redaction applies (same rule as the `archive-vault` CLI,
// which ships every file regardless of frontmatter owner — the GM owns the
// files). "Secret-filtered" here is the gate itself (guest / player /
// preview-as contexts get 403 and learn nothing about vault contents) plus
// the data-artifact exclusion below: `<name>.index.db`, `<name>.app.db`
// and `.sessionkey` never cross the boundary, symlinks are skipped with a
// count, and files over the P13 10MB cap refuse before streaming starts.
// The download is logged (who + counts, never content).

// maxExportFileBytes mirrors the P13 per-file cap enforced by the
// `archive-vault` CLI: images 5MB, PDFs 10MB — the zip refuses above 10MB.
const maxExportFileBytes = 10 << 20

// VaultZip streams the live vault as a zip attachment. GET (read-only, no
// mutation, so no CSRF token — consistent with every other dashboard GET);
// the GM gate + no-store headers carry the safety. Served from VaultRoot,
// never from the index DB (vault filesystem is truth).
func (h *ReadHandlers) VaultZip(w http.ResponseWriter, r *http.Request) {
	viewer := viewerFromRequest(r)
	if viewer == nil || !viewer.IsGM || viewer.PreviewAs != "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.TrimSpace(h.VaultRoot) == "" {
		http.Error(w, "vault is not configured", http.StatusInternalServerError)
		return
	}
	entries, skipped, err := scanExportFiles(h.VaultRoot)
	if err != nil {
		slog.Warn("vault zip refused", "gm", viewer.UserID, "err", err)
		http.Error(w, "vault cannot be exported", http.StatusInternalServerError)
		return
	}
	name := exportZipName(h.VaultRoot)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method == http.MethodHead {
		slog.Info("vault zip download", "gm", viewer.UserID, "files", len(entries), "skipped", skipped, "head", true)
		return
	}
	zw := zip.NewWriter(w)
	files := 0
	for _, rel := range entries {
		if err := writeExportEntry(zw, h.VaultRoot, rel); err != nil {
			// Headers are already sent: log loudly, abort the stream.
			// The truncated zip fails its own checksum on open, so a
			// partial download never masquerades as complete.
			slog.Warn("vault zip truncated", "gm", viewer.UserID, "file", rel, "err", err)
			_ = zw.Close()
			return
		}
		files++
	}
	_ = zw.Close()
	slog.Info("vault zip download", "gm", viewer.UserID, "files", files, "skipped", skipped)
}

// scanExportFiles walks the vault and returns the zip entry list (sorted by
// walk order), failing fast on unreadable roots and oversized files so the
// handler can refuse BEFORE headers go out. Data artifacts (*.db,
// .sessionkey), symlinks, and non-regular files are skipped with a count,
// never stored — same exclusion rule as the `archive-vault` CLI.
func scanExportFiles(root string) (entries []string, skipped int, err error) {
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		base := filepath.Base(rel)
		if strings.HasSuffix(strings.ToLower(base), ".db") || base == ".sessionkey" {
			skipped++
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			skipped++
			return nil
		}
		if !d.Type().IsRegular() {
			if !d.IsDir() {
				skipped++
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxExportFileBytes {
			return fmt.Errorf("file too large for export (>10MB): %q", rel)
		}
		entries = append(entries, rel)
		return nil
	})
	if walkErr != nil {
		return nil, 0, walkErr
	}
	return entries, skipped, nil
}

// writeExportEntry stores one pre-scanned vault file into the open zip.
func writeExportEntry(zw *zip.Writer, root, rel string) error {
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	hdr.Name = rel
	hdr.Method = zip.Deflate
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}

// exportZipName builds the attachment filename from the vault dir base +
// UTC date. Sanitized for Windows (no `<>:"/\|?*`, no trailing dots/spaces,
// no reserved device names — same CleanRel-adjacent rules as vault paths).
func exportZipName(vaultRoot string) string {
	base := filepath.Base(filepath.Clean(vaultRoot))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	name := strings.Trim(strings.Trim(b.String(), "-"), ". ")
	if name == "" {
		name = "vault"
	}
	return name + "-" + time.Now().UTC().Format("20060102") + ".zip"
}
