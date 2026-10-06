package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Zip safety shared by archive-vault (export), clone-vault, and
// init --template (import). P13 guards, applied on both sides:
// zip-slip (no absolute paths, no .. escapes), no symlinks, size caps
// (5MB image / 10MB PDF scale: 64MB total uncompressed cap for templates),
// and *.db/.sessionkey never cross the boundary.

const (
	maxTemplateBytes int64 = 64 << 20
	maxFileBytes     int64 = 10 << 20 // PDF cap; images are smaller (5MB)
)

// zipEntryName validates a zip entry name and returns the clean
// slash-separated relative path, or an error refusing it.
func zipEntryName(name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("refusing absolute/empty entry: %q", name)
	}
	clean := filepath.ToSlash(filepath.Clean(name))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("refusing escaping entry: %q", name)
	}
	base := filepath.Base(clean)
	if strings.HasSuffix(strings.ToLower(base), ".db") || base == ".sessionkey" {
		return "", fmt.Errorf("refusing data artifact entry: %q", name)
	}
	return clean, nil
}

// unzipInto extracts a downloaded template zip into dest (which must be an
// empty dir) with all import guards applied.
func unzipInto(zipPath, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer func() { _ = zr.Close() }()
	var total int64
	for _, f := range zr.File {
		rel, err := zipEntryName(f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink entry: %q", f.Name)
		}
		if f.UncompressedSize64 > uint64(maxFileBytes) {
			return fmt.Errorf("entry too large (>10MB): %q", f.Name)
		}
		total += int64(f.UncompressedSize64)
		if total > maxTemplateBytes {
			return fmt.Errorf("archive too large (>64MB uncompressed)")
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			_ = rc.Close()
			return err
		}
		_, copyErr := io.CopyN(out, rc, maxFileBytes+1)
		closeErr := out.Close()
		_ = rc.Close()
		if copyErr != nil && copyErr != io.EOF {
			return fmt.Errorf("extract %q: %w", rel, copyErr)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

// fetchTemplate downloads url (https only) into the cache dir, verifies
// sha256 when wantSHA is set, and extracts into the empty vault dir.
// Cache entries honor ttl; a stale entry is re-downloaded and re-verified.
func fetchTemplate(vaultDir, rawURL, wantSHA string, ttl time.Duration) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("template URL must be https (got %q)", rawURL)
	}
	empty, err := dirIsEmpty(vaultDir)
	if err != nil || !empty {
		return fmt.Errorf("template target %s must be an empty dir", vaultDir)
	}
	sum := sha256.Sum256([]byte(rawURL))
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("cache dir: %w", err)
	}
	cacheDir = filepath.Join(cacheDir, "yonder", "templates")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	cached := filepath.Join(cacheDir, hex.EncodeToString(sum[:])+".zip")
	if st, err := os.Stat(cached); err == nil && ttl > 0 && time.Since(st.ModTime()) < ttl {
		if wantSHA != "" {
			if ok, err := fileSHA256(cached, wantSHA); err != nil || !ok {
				if err != nil {
					return err
				}
			} else {
				return unzipInto(cached, vaultDir)
			}
		} else {
			return unzipInto(cached, vaultDir)
		}
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download template: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download template: %s", resp.Status)
	}
	tmp, err := os.CreateTemp(cacheDir, ".dl-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	h := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(tmp, h), resp.Body, maxTemplateBytes+1); err != nil && err != io.EOF {
		_ = tmp.Close()
		return fmt.Errorf("download template: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if wantSHA != "" && !strings.EqualFold(got, wantSHA) {
		return fmt.Errorf("template checksum mismatch (want %s, got %s)", wantSHA, got)
	}
	if err := os.Rename(tmpName, cached); err != nil {
		return err
	}
	return unzipInto(cached, vaultDir)
}

// fileSHA256 compares a file's sha256 against want (hex, case-insensitive).
func fileSHA256(path, want string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want), nil
}

// archiveVaultDir zips the vault at src into outZip. Data artifacts
// (*.db, .sessionkey) and symlinks are skipped with a count, never stored.
func archiveVaultDir(src, outZip string) (files, skipped int, err error) {
	out, err := os.Create(outZip)
	if err != nil {
		return 0, 0, err
	}
	zw := zip.NewWriter(out)
	walkErr := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
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
		if info.Size() > maxFileBytes {
			return fmt.Errorf("file too large for archive (>10MB): %q", rel)
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
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(w, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		files++
		return nil
	})
	if walkErr != nil {
		_ = zw.Close()
		_ = out.Close()
		return files, skipped, walkErr
	}
	if err := zw.Close(); err != nil {
		_ = out.Close()
		return files, skipped, err
	}
	if err := out.Close(); err != nil {
		return files, skipped, err
	}
	return files, skipped, nil
}

// cloneVaultDir copies regular vault files from src to dest (created when
// missing, must be empty otherwise). Data artifacts and symlinks are
// skipped, never copied.
func cloneVaultDir(src, dest string) (files, skipped int, err error) {
	if err := ensureDir(dest); err != nil {
		return 0, 0, err
	}
	empty, err := dirIsEmpty(dest)
	if err != nil {
		return 0, 0, err
	}
	if !empty {
		return 0, 0, fmt.Errorf("clone target %s is not empty", dest)
	}
	walkErr := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		base := d.Name()
		if strings.HasSuffix(strings.ToLower(base), ".db") || base == ".sessionkey" {
			skipped++
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			skipped++
			return nil
		}
		if !d.Type().IsRegular() {
			skipped++
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		files++
		return nil
	})
	return files, skipped, walkErr
}
