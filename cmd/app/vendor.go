package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Vendor pin flow (Lane E1). VERSIONS lines:
//   name version sha256 url [file=local-fallback]
// `vendor verify` checks committed fallbacks offline (sha256 of the file),
// and with --fetch also re-downloads each URL and compares. `vendor sha256
// <url>` prints the checksum of a download to author new pins.

type vendorPin struct {
	name    string
	version string
	sha256  string
	url     string
	file    string
}

func parseVersions(path string) ([]vendorPin, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var pins []vendorPin
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return nil, fmt.Errorf("bad VERSIONS line: %q (want: name version sha256 url [file=...])", line)
		}
		p := vendorPin{name: fields[0], version: fields[1], sha256: fields[2], url: fields[3]}
		for _, extra := range fields[4:] {
			if v, ok := strings.CutPrefix(extra, "file="); ok {
				p.file = v
			} else {
				return nil, fmt.Errorf("bad VERSIONS line: %q (unknown field %q)", line, extra)
			}
		}
		pins = append(pins, p)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(pins) == 0 {
		return nil, fmt.Errorf("no pins in %s", path)
	}
	return pins, nil
}

// findVersions locates web/static/vendor/VERSIONS from an explicit dir,
// the working tree, or the executable layout (installed binary).
func findVersions(explicit string) (vendorDir, versionsPath string, err error) {
	if explicit != "" {
		return explicit, filepath.Join(explicit, "VERSIONS"), nil
	}
	if _, statErr := os.Stat(filepath.Join("web", "static", "vendor", "VERSIONS")); statErr == nil {
		return filepath.Join("web", "static", "vendor"), filepath.Join("web", "static", "vendor", "VERSIONS"), nil
	}
	if exe, exeErr := os.Executable(); exeErr == nil {
		for dir := filepath.Dir(exe); ; dir = filepath.Dir(dir) {
			cand := filepath.Join(dir, "web", "static", "vendor", "VERSIONS")
			if _, statErr := os.Stat(cand); statErr == nil {
				return filepath.Dir(cand), cand, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
		}
	}
	return "", "", fmt.Errorf("VERSIONS not found (run from repo root or pass --vendor-dir)")
}

// runVendorVerify checks every pin. Without fetch it verifies committed
// fallback files offline; with fetch it re-downloads each URL (https) and
// compares checksums.
func runVendorVerify(vendorDir, versionsPath string, fetch bool) error {
	pins, err := parseVersions(versionsPath)
	if err != nil {
		return err
	}
	failed := 0
	for _, p := range pins {
		if p.file != "" {
			local := filepath.Join(vendorDir, p.file)
			ok, err := fileSHA256(local, p.sha256)
			if err != nil || !ok {
				fmt.Printf("FAIL %s %s: local %s mismatch (%v)\n", p.name, p.version, p.file, err)
				failed++
				continue
			}
			fmt.Printf("ok %s %s (local %s)\n", p.name, p.version, p.file)
		} else {
			fmt.Printf("skip %s %s: no committed fallback (fetch to verify URL)\n", p.name, p.version)
		}
		if fetch {
			got, err := downloadSHA256(p.url)
			if err != nil {
				fmt.Printf("FAIL %s %s: download: %v\n", p.name, p.version, err)
				failed++
				continue
			}
			if !strings.EqualFold(got, p.sha256) {
				fmt.Printf("FAIL %s %s: url checksum mismatch (want %s, got %s)\n", p.name, p.version, p.sha256, got)
				failed++
				continue
			}
			fmt.Printf("ok %s %s (url)\n", p.name, p.version)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d vendor checks failed", failed)
	}
	return nil
}

func downloadSHA256(rawURL string) (string, error) {
	if !strings.HasPrefix(rawURL, "https://") {
		return "", fmt.Errorf("refusing non-https URL %q", rawURL)
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: %s", resp.Status)
	}
	h := sha256.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
