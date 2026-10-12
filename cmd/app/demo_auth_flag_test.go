package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/web"
)

// ?as= retirement end-to-end through the PRODUCTION serve wiring
// (wireHandlers + withDemoViewer + the --demo-auth flag default, true per
// main.go runServeCommand/runServe paths — verified, unchanged).
//
//   - DemoAuth=false: the demo tier is dead over HTTP — secret ?as=gm is a
//     uniform 404 (byte-identical to the missing-page oracle), the landing
//     redirect drops ?as=/preview_as, open renders carry no ?as=.
//   - DemoAuth=true: shipped demo behavior holds (secret ?as=gm reads 200).
//
// httptest only — never binds :8080. Flips per-case and restores.
func TestServeDemoAuthRetiresAsTier(t *testing.T) {
	// Flag default check (no serve started): the kill-switch defaults to
	// the shipped behavior; false is opt-in sessions-only.
	if web.DemoAuth != true {
		t.Fatalf("web.DemoAuth default = %v, want true (serve --demo-auth default)", web.DemoAuth)
	}
	old := web.DemoAuth
	t.Cleanup(func() { web.DemoAuth = old })

	vault := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		p := filepath.Join(vault, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("welcome.md", "---\ntitle: Welcome\n---\n\nHello. See [[pact|The Pact]].\n")
	write("pact.md", "---\ntitle: Pact\nsecret: true\nowner: mira\n---\n\nSealed. Back [[welcome]].\n")
	write("campaign.yaml", "name: DemoAuth Serve\ncreated: 2026-10-10T00:00:00Z\n")

	dataDir := t.TempDir()
	if err := runReindex(vault, dataDir); err != nil {
		t.Fatal(err)
	}
	db, _, err := openAppDB(dataDir, vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sessionStore, err := auth.NewSessionStore(db)
	if err != nil {
		t.Fatal(err)
	}
	wiring := wireHandlers(buildInfo{Version: "demoauth"}, vault, dataDir, sessionStore, "", "")
	srv := httptest.NewServer(wiring.Handler)
	t.Cleanup(srv.Close)

	plain := &http.Client{}
	noFollow := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	get := func(c *http.Client, target string) (int, string, http.Header) {
		t.Helper()
		resp, err := c.Get(srv.URL + target)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body), resp.Header
	}

	web.DemoAuth = false
	if code, _, _ := get(plain, "/dashboard?as=gm"); code != http.StatusForbidden {
		t.Errorf("retired dashboard ?as=gm = %d, want 403", code)
	}
	secretCode, secretBody, _ := get(plain, "/p/pact.md?as=gm")
	missingCode, missingBody, _ := get(plain, "/p/no-such-page.md")
	if secretCode != http.StatusNotFound || missingCode != http.StatusNotFound {
		t.Fatalf("retired secret = %d, missing = %d; want 404/404", secretCode, missingCode)
	}
	if secretBody != missingBody {
		t.Errorf("retired secret 404 differs from missing 404 (oracle)")
	}
	for _, leak := range []string{"Pact", "Sealed"} {
		if strings.Contains(secretBody, leak) {
			t.Errorf("retired secret 404 leaked %q", leak)
		}
	}
	code, _, hdr := get(noFollow, "/?as=gm&preview_as=cass")
	if code != http.StatusSeeOther {
		t.Fatalf("retired / = %d, want 303", code)
	}
	if hdr.Get("Location") != "/p/welcome" {
		t.Errorf("retired /?as=gm Location = %q, want /p/welcome", hdr.Get("Location"))
	}
	if code, body, _ := get(plain, "/p/welcome.md?as=gm"); code != http.StatusOK {
		t.Errorf("retired open page = %d, want 200", code)
	} else {
		for _, leak := range []string{"?as=", "preview_as"} {
			if strings.Contains(body, leak) {
				t.Errorf("retired open render propagates %q", leak)
			}
		}
	}

	web.DemoAuth = true
	if code, _, _ := get(plain, "/p/pact.md?as=gm"); code != http.StatusOK {
		t.Errorf("default ?as=gm secret = %d, want 200 (behavior unchanged)", code)
	}
	code, _, hdr = get(noFollow, "/?as=gm")
	if code != http.StatusSeeOther {
		t.Fatalf("default / = %d, want 303", code)
	}
	if hdr.Get("Location") != "/p/welcome?as=gm" {
		t.Errorf("default /?as=gm Location = %q, want /p/welcome?as=gm", hdr.Get("Location"))
	}
}
