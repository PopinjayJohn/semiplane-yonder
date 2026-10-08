package ruleset

// Gate G3 tests: H1 vault packs → executable Rulesets → engine evaluation.
// Temp vaults seeded from fixtures/rules (packs are files; the
// file-backed-DB pitfall applies to SQLite, not here).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gateVault(t *testing.T, homebrew bool) string {
	t.Helper()
	root := t.TempDir()
	if err := copyDir("../../fixtures/rules/base", filepath.Join(root, "rules", "base")); err != nil {
		t.Fatal(err)
	}
	if err := copyDir("../../fixtures/rules/overlay", filepath.Join(root, "rules", "overlay")); err != nil {
		t.Fatal(err)
	}
	if homebrew {
		if err := copyDir("../../fixtures/rules/homebrew", filepath.Join(root, "rules", "homebrew")); err != nil {
			t.Fatal(err)
		}
	}
	campaign := "name: Gate\ncreated: 2026-10-08T00:00:00Z\nbase: dnd\noverlay: 5e-2024\n"
	if err := os.WriteFile(filepath.Join(root, "campaign.yaml"), []byte(campaign), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		s, d := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(s, d); err != nil {
				return err
			}
			continue
		}
		b, err := os.ReadFile(s)
		if err != nil {
			return err
		}
		if err := os.WriteFile(d, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// The timebox packs convert lint-clean: hyphenated data keys (rest-short,
// con-mod) are legal keys, never expression variables.
func TestGateLoadPacksLintClean(t *testing.T) {
	root := gateVault(t, false)
	for _, dir := range []string{"rules/base/dnd", "rules/overlay/5e-2014", "rules/overlay/5e-2024"} {
		rs, err := LoadVaultPack(root, dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		if issues := LintErrors(LintRuleset(rs, nil)); len(issues) > 0 {
			t.Fatalf("%s: %+v", dir, issues)
		}
	}
}

// The symbolic grit hook stays display data: unparseable effects never
// reach the executable set (lint-green and load-green agree), while the
// grit-die optional still registers for campaign toggles.
func TestGateSymbolicHookDropped(t *testing.T) {
	root := gateVault(t, true)
	rs, err := LoadVaultPack(root, "rules/homebrew/grit")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range rs.IntentDefs {
		if len(id.Hooks) > 0 {
			t.Fatalf("symbolic hook loaded: %+v", id.Hooks)
		}
	}
	if _, ok := rs.Optionals["grit-die"]; !ok {
		t.Fatalf("grit-die optional lost: %+v", rs.Optionals)
	}
	if issues := LintErrors(LintRuleset(rs, nil)); len(issues) > 0 {
		t.Fatalf("grit: %+v", issues)
	}
}

// Rest intents evaluate clean on the resolved stack (the served sheet
// flows); undeclared intents wrap ErrUnknownIntent for TouchRecovery.
func TestGateStackRestClean(t *testing.T) {
	e, err := LoadVaultStack(gateVault(t, true))
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{"rest-short", "rest-long"} {
		m, err := e.Evaluate(context.Background(), Intent{Intent: in, Actor: "pc"})
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if m.Metadata.OverlayID != "5e-2024" || m.Metadata.BaseID != "dnd" {
			t.Fatalf("%s: bad metadata %+v", in, m.Metadata)
		}
	}
	_, err = e.Evaluate(context.Background(), Intent{Intent: "level-up", Actor: "pc"})
	if !errors.Is(err, ErrUnknownIntent) {
		t.Fatalf("level-up: want ErrUnknownIntent, got %v", err)
	}
}

// The symbolic base cover hook loads (it parses) and fails CLOSED at
// evaluation — wrong numbers are never rolled silently.
func TestGateSymbolicHookFailsClosed(t *testing.T) {
	e, err := LoadVaultStack(gateVault(t, false))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Evaluate(context.Background(), Intent{Intent: "attack", Actor: "pc"})
	if err == nil {
		t.Fatalf("attack with symbolic cover hook evaluated clean")
	}
}

// A homebrew diffing an inactive overlay is skipped (loudly) instead of
// sinking the valid base→overlay prefix.
func TestGateHomebrewSkipOnOverlaySwitch(t *testing.T) {
	root := gateVault(t, true)
	raw, err := os.ReadFile(filepath.Join(root, "campaign.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	swapped := strings.Replace(string(raw), "5e-2024", "5e-2014", 1)
	if err := os.WriteFile(filepath.Join(root, "campaign.yaml"), []byte(swapped), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := LoadVaultStack(root)
	if err != nil {
		t.Fatalf("2014 + 2024-homebrew must load the valid prefix: %v", err)
	}
	m, err := e.Evaluate(context.Background(), Intent{Intent: "rest-short", Actor: "pc"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Metadata.OverlayID != "5e-2014" {
		t.Fatalf("want 5e-2014, got %q", m.Metadata.OverlayID)
	}
}

// Overlay switch is a campaign.yaml edit: re-resolve, no rebuild. The
// engine metadata follows the active overlay in the same process.
func TestGateOverlaySwitchNoRebuild(t *testing.T) {
	root := gateVault(t, false)
	before, err := LoadVaultStack(root)
	if err != nil {
		t.Fatal(err)
	}
	m, err := before.Evaluate(context.Background(), Intent{Intent: "rest-short", Actor: "pc"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Metadata.OverlayID != "5e-2024" {
		t.Fatalf("want 5e-2024, got %q", m.Metadata.OverlayID)
	}
	// Switch overlays: one file edit, same process, no restart.
	raw, err := os.ReadFile(filepath.Join(root, "campaign.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	swapped := strings.Replace(string(raw), "5e-2024", "5e-2014", 1)
	if err := os.WriteFile(filepath.Join(root, "campaign.yaml"), []byte(swapped), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := LoadVaultStack(root)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := after.Evaluate(context.Background(), Intent{Intent: "rest-short", Actor: "pc"})
	if err != nil {
		t.Fatal(err)
	}
	if m2.Metadata.OverlayID != "5e-2014" {
		t.Fatalf("want 5e-2014 after switch, got %q", m2.Metadata.OverlayID)
	}
}
