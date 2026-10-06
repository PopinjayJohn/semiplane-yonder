// Command budgets enforces the CI quality-gate budgets that are checkable
// before the read path exists (P10): upload size caps (spec §7, P13),
// rendered-page weight (<100KB HTML/CSS excl. vendored JS), and the plugin
// CSS cap (20KB). Checks that have no subject yet report SKIP, never FAIL,
// so the gate stays green while other lanes build; each line prints its
// verdict for the CI log. Exit 1 on any FAIL.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/semiplane/yonder/internal/uploads"
)

const (
	// maxRenderedPageBytes is the P10 budget for one rendered page
	// (HTML + CSS, excluding vendored JS).
	maxRenderedPageBytes = 100 * 1024
	// maxPluginCSSBytes is the P06/P10 cap for one plugin's CSS.
	maxPluginCSSBytes = 20 * 1024
)

var failures int

func pass(name, detail string) {
	fmt.Printf("BUDGET %-22s PASS  %s\n", name, detail)
}

func skip(name, detail string) {
	fmt.Printf("BUDGET %-22s SKIP  %s\n", name, detail)
}

func fail(name, detail string) {
	fmt.Printf("BUDGET %-22s FAIL  %s\n", name, detail)
	failures++
}

// repoRoot finds the checkout root by walking up from the working directory
// until go.mod is found.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// dirSize sums regular file bytes under root, skipping dot-dirs.
func dirSize(root string) (int64, int, error) {
	var total int64
	var count int
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if p != root && len(info.Name()) > 0 && info.Name()[0] == '.' {
				return filepath.SkipDir
			}
			return nil
		}
		name := info.Name()
		if len(name) > 0 && name[0] == '.' {
			return nil // dotfiles (.gitkeep, .obsidian) are not shipped weight
		}
		total += info.Size()
		count++
		return nil
	})
	return total, count, err
}

func checkUploadCaps() {
	if uploads.MaxImageBytes != 5*1024*1024 {
		fail("upload-image-cap", fmt.Sprintf("MaxImageBytes=%d, want 5MB", uploads.MaxImageBytes))
	} else {
		pass("upload-image-cap", "images + webp capped at 5MB")
	}
	if uploads.MaxPDFBytes != 10*1024*1024 {
		fail("upload-pdf-cap", fmt.Sprintf("MaxPDFBytes=%d, want 10MB", uploads.MaxPDFBytes))
	} else {
		pass("upload-pdf-cap", "pdf capped at 10MB")
	}
	for _, mime := range []string{uploads.MIMEPNG, uploads.MIMEJPEG, uploads.MIMEWebP, uploads.MIMEPDF} {
		if uploads.MaxBytesFor(mime) < 0 {
			fail("upload-cap-coverage", "accepted MIME "+mime+" has no size cap")
			return
		}
	}
	pass("upload-cap-coverage", "every accepted MIME has a cap")
}

func checkStaticWeight(root string) {
	for _, tc := range []struct {
		name string
		dir  string
		cap  int64
	}{
		{"page-weight-proxy", "web/static/core", maxRenderedPageBytes},
		{"theme-weight", "web/static/themes", maxRenderedPageBytes},
	} {
		total, count, err := dirSize(filepath.Join(root, tc.dir))
		if err != nil || count == 0 {
			skip(tc.name, tc.dir+" empty or missing (no subject yet)")
			continue
		}
		if total > tc.cap {
			fail(tc.name, fmt.Sprintf("%s totals %d bytes over %d", tc.dir, total, tc.cap))
			continue
		}
		pass(tc.name, fmt.Sprintf("%s totals %d bytes in %d files", tc.dir, total, count))
	}
}

func checkTemplates(root string) {
	total, count, err := dirSize(filepath.Join(root, "web/templates"))
	if err != nil || count == 0 {
		skip("rendered-page", "no templates yet (F1); per-page <100KB enforced when pages exist")
		return
	}
	if total > maxRenderedPageBytes {
		fail("rendered-page", fmt.Sprintf("templates total %d bytes over %d", total, maxRenderedPageBytes))
		return
	}
	pass("rendered-page", fmt.Sprintf("templates total %d bytes in %d files", total, count))
}

// checkPluginCSS enforces the 20KB per-plugin CSS cap (P06/P10). Plugin style
// locations land with the plugin lanes; until a *.css file exists under a
// plugin dir this reports SKIP.
func checkPluginCSS(root string) {
	var offenders []string
	for _, dir := range []string{"web/static/plugins", "internal/plugins"} {
		_ = filepath.Walk(filepath.Join(root, dir), func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".css") {
				return nil
			}
			if info.Size() > maxPluginCSSBytes {
				offenders = append(offenders, fmt.Sprintf("%s (%d bytes)", p, info.Size()))
			}
			return nil
		})
	}
	if len(offenders) > 0 {
		fail("plugin-css-cap", "over 20KB: "+strings.Join(offenders, ", "))
		return
	}
	skip("plugin-css-cap", "no plugin CSS yet (I2); 20KB cap enforced when plugins land")
}

func main() {
	root, err := repoRoot()
	if err != nil {
		fmt.Println("BUDGET repo-root FAIL ", err)
		os.Exit(1)
	}
	checkUploadCaps()
	checkStaticWeight(root)
	checkTemplates(root)
	checkPluginCSS(root)
	if failures > 0 {
		os.Exit(1)
	}
}
