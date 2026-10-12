package web

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// Theme selection (T1). Themes are token-override stylesheets under
// web/static/themes/<id>.css, applied through [data-theme] / [data-scheme]
// attributes on <html> (design record: docs/plan/themes.md). The selection
// SOURCE is deliberately small for now: serve's --theme / --theme-scheme
// flags, which fail closed to unthemed. The campaign.yaml keys (theme /
// theme-scheme) are the pending amend (backlog T1) — when they land they
// resolve into these same validated strings, so every render surface (shell,
// standalone sheet page, VTT page) stays one funnel.
//
// Themes are never a secret surface: they carry no vault data, only colors,
// and every themed page still renders the same server-filtered HTML.
// ---------------------------------------------------------------------------

// Theme ids — each must have a matching web/static/themes/<id>.css (the
// binary embeds the mirror under cmd/app/static/themes/).
const (
	ThemeGrimoire     = "grimoire"
	ThemeCartographer = "cartographer"
)

// Scheme ids. Absent/empty means light, matching the CSS default; serve makes
// it explicit so a rendered page always states its scheme when themed.
const (
	SchemeLight = "light"
	SchemeDark  = "dark"
	SchemeAuto  = "auto"
)

// knownThemes is the shipped set (kept next to the embed so a theme cannot be
// selected without shipping).
var knownThemes = []string{ThemeGrimoire, ThemeCartographer}

// knownSchemes lists the accepted schemes.
var knownSchemes = []string{SchemeLight, SchemeDark, SchemeAuto}

// ThemeKnown reports whether id names a theme this binary ships.
func ThemeKnown(id string) bool {
	for _, t := range knownThemes {
		if t == id {
			return true
		}
	}
	return false
}

// SchemeKnown reports whether s is a valid scheme id.
func SchemeKnown(s string) bool {
	for _, v := range knownSchemes {
		if v == s {
			return true
		}
	}
	return false
}

// ThemeList returns the shipped theme ids (for flag help + docs).
func ThemeList() []string { return append([]string(nil), knownThemes...) }

// NormalizeTheme validates a theme id and fails closed: unknown or empty
// returns "" (unthemed — core tokens). Callers log the rejection; they never
// render a broken attribute.
func NormalizeTheme(id string) string {
	id = strings.TrimSpace(id)
	if !ThemeKnown(id) {
		return ""
	}
	return id
}

// NormalizeScheme validates a scheme id. An empty scheme is valid input for
// an unthemed page and returns ""; an unknown non-empty value fails closed
// to SchemeLight only when a theme is set (never to something broken).
func NormalizeScheme(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !SchemeKnown(s) {
		return SchemeLight
	}
	return s
}

// ResolveTheme is the single selection funnel: flags (or campaign.yaml once
// the amend lands) in, validated (theme, scheme) out. A scheme without a
// theme is meaningless — every theme selector is scoped under [data-theme] —
// so it returns ("", ""). An empty scheme normalizes to light once a theme
// is set, so a themed page always states its scheme explicitly.
func ResolveTheme(theme, scheme string) (string, string) {
	t := NormalizeTheme(theme)
	if t == "" {
		return "", ""
	}
	s := NormalizeScheme(scheme)
	if s == "" {
		s = SchemeLight
	}
	return t, s
}

// HTMLOpenTag renders the document's opening <html> tag for a theme +
// scheme pair. Unthemed returns the plain pre-T1 tag, so an unthemed
// document is byte-identical to one rendered before T1. The attribute order
// is fixed here (not via templ.Attributes, which renders in random Go map
// order) so every request's HTML is byte-stable.
func HTMLOpenTag(theme, scheme string) string {
	if theme == "" {
		return `<html lang="en">`
	}
	if scheme == "" {
		scheme = SchemeLight
	}
	return fmt.Sprintf(`<html lang="en" data-theme=%q data-scheme=%q>`, theme, scheme)
}

// ThemeStylesheetHRef is the served path of a theme stylesheet (empty when
// unthemed). The mount is /static/themes/ (cmd/app isPublicStatic).
func ThemeStylesheetHRef(theme string) string {
	if theme == "" {
		return ""
	}
	return "/static/themes/" + theme + ".css"
}

// ThemeStylesheetLink renders the <link> element for a theme stylesheet
// ("" when unthemed, newline-terminated when present). Shared by the
// standalone pages (VTT, sheet) that build their own heads; the shell builds
// the same tag in templates (which cannot import web — import cycle).
func ThemeStylesheetLink(theme string) string {
	href := ThemeStylesheetHRef(theme)
	if href == "" {
		return ""
	}
	return `<link rel="stylesheet" href="` + href + `">` + "\n"
}

// PluginAssets is the asset seam beside SlotProvider: the plugin registry
// (internal/plugins) implements it and serve injects the registry into
// ReadHandlers, so web never imports plugins (import cycle). Nil means no
// plugin ships assets; callers must tolerate nil.
type PluginAssets interface {
	CSS() string
	JS() string
}
