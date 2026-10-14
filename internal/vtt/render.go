package vtt

import (
	"fmt"
	"html"
	"math"
	"strings"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/plugins"
	"github.com/semiplane/yonder/internal/web"
)

// FragmentInput is the already-filtered render input (Assemble-style: the
// caller ran SnapshotFor, so hidden tokens are already gone for players).
type FragmentInput struct {
	MapID     string
	Title     string
	Snap      plugins.StateSnapshot
	Cal       Calibration
	Fog       FogView
	Viewer    *auth.Viewer // effective viewer (preview-as already folded)
	IsGM      bool
	AsQuery   string            // opaque "?as=.." passthrough for asset/SSE URLs ("" when none)
	CSRF      string            // session CSRF token for fetch clients ("" when unwired)
	CharLinks map[string]string // tokenID -> viewable character page (absent = no link)
}

// RenderFragment returns the inner HTML for #vtt-map (the frozen
// MapFragmentTarget). All strings escaped; tokens positioned by grid cell;
// fog + tokens carry text equivalents (never color-only); controls are ≥44px
// (CSS) and keyboard-reachable; SSE updates announce via aria-live regions.
// Morphs must never steal focus: the client script skips morphs while focus
// sits inside the fragment and announces instead.
func RenderFragment(in FragmentInput) string {
	var b strings.Builder
	g := in.Cal.Grid
	fmt.Fprintf(&b, `<div data-vtt-board data-map-id="%s" data-cols="%d" data-rows="%d" data-fragment-version="%d">`+"\n",
		html.EscapeString(in.MapID), g.Cols, g.Rows, plugins.FragmentVersion)
	b.WriteString(renderBoard(in))
	b.WriteString(renderInitiative(in))
	b.WriteString(renderDiceTray(in))
	b.WriteString(renderTokenList(in))
	if in.IsGM && len(in.Snap.Doors) > 0 {
		b.WriteString(renderDoors(in))
	}
	if in.IsGM {
		b.WriteString(renderFogControls(in))
	}
	b.WriteString(`<p data-sse-state role="status" aria-live="polite">Live updates: connecting.</p>` + "\n")
	b.WriteString("</div>\n")
	return b.String()
}

func renderBoard(in FragmentInput) string {
	var b strings.Builder
	g := in.Cal.Grid
	title := in.Title
	if title == "" {
		title = in.MapID
	}
	fmt.Fprintf(&b, `<section aria-label="Battle map: %s" data-board`, html.EscapeString(title))
	if in.Cal.Background == "" {
		// Link-only map (imported calibration, art not on disk yet): give the
		// board its grid's intrinsic aspect ratio (with a floor for extreme
		// grids) so the overlay grid, tokens, and fog still have a board area;
		// dropping art in later fills the same box.
		fmt.Fprintf(&b, ` style="aspect-ratio:%d / %d;min-height:240px"`, g.Cols, g.Rows)
	}
	b.WriteString(">" + "\n")
	// Background through the ACL-checked asset handler (bytes never served
	// here; unauthorized viewers 404 at /assets, fail closed). The sidecar
	// value is vault-relative (amend A-VTT2: assets/maps/<mapID>.<ext>) and
	// the handler serves /assets/<vault-path>, so the URL keeps the /assets/
	// prefix; bundle-dir art inherits the sidecar ACL (assetVisible).
	if in.Cal.Background != "" {
		fmt.Fprintf(&b, `<div data-board-image><img src="/assets/%s%s" alt="Map background for %s"></div>`+"\n",
			html.EscapeString(in.Cal.Background), html.EscapeString(in.AsQuery), html.EscapeString(title))
	} else {
		b.WriteString(`<div data-board-image><p>No background image set. Ask your GM to set one in the map sidecar.</p></div>` + "\n")
	}
	// Grid overlay for nogrid art (aria-hidden: token labels already carry
	// text coordinates). Grid-baked art carries its own lines — skip.
	if !in.Cal.GridBaked {
		fmt.Fprintf(&b, `<div data-grid aria-hidden="true" style="background-size:%.4f%% %.4f%%"></div>`+"\n",
			100/float64(g.Cols), 100/float64(g.Rows))
	}
	// Fog overlay + text equivalent.
	b.WriteString(renderFog(in))
	// Tokens as real buttons (keyboard-reachable, 44px, text labels). The
	// button carries NO DOM text: its face (initial + HP) is CSS-generated
	// (--face/--hp), so WCAG 2.5.3's visible-text check has nothing to
	// mismatch against the full-text aria-label, and the sheet link / hidden
	// tag are SIBLINGS (a link inside a button is invalid HTML).
	fmt.Fprintf(&b, `<div data-tokens role="group" aria-label="Tokens on %s">`+"\n", html.EscapeString(title))
	for _, t := range in.Snap.Tokens {
		left := (float64(t.X) + 0.5) * 100 / float64(g.Cols)
		top := (float64(t.Y) + 0.5) * 100 / float64(g.Rows)
		label := fmt.Sprintf("%s, column %d row %d, %d of %d hit points", t.Name, t.X, t.Y, t.HP, t.MaxHP)
		if t.Hidden {
			label += ", hidden from party"
		}
		face := strings.ToUpper(string(runeOf(t.Name)))
		hp := fmt.Sprintf("%d/%d", t.HP, t.MaxHP)
		fmt.Fprintf(&b, `<span data-token-wrap style="left:%.2f%%;top:%.2f%%">`, left, top)
		fmt.Fprintf(&b, `<button type="button" data-token="%s" data-x="%d" data-y="%d" data-name="%s" aria-label="%s" aria-pressed="false" style="--face:%s;--hp:%s"></button>`,
			html.EscapeString(t.ID), t.X, t.Y, html.EscapeString(t.Name),
			html.EscapeString(label), cssContent(face), cssContent(hp))
		if t.Hidden {
			b.WriteString(`<span data-tag>hidden</span>`)
		}
		if page, ok := in.CharLinks[t.ID]; ok && page != "" {
			fmt.Fprintf(&b, `<a href="/p/%s%s" data-char-link>sheet</a>`, html.EscapeString(page), html.EscapeString(in.AsQuery))
		}
		b.WriteString("</span>\n")
	}
	b.WriteString("</div>\n</section>\n")
	return b.String()
}

// cssContent renders a CSS custom-property value (single-quoted, quotes
// stripped) for the token face / HP pseudo-content.
func cssContent(s string) string {
	s = strings.ReplaceAll(s, "'", "")
	if s == "" {
		return `""`
	}
	return "'" + s + "'"
}

func runeOf(s string) rune {
	for _, r := range s {
		return r
	}
	return '?'
}

func renderFog(in FragmentInput) string {
	var b strings.Builder
	if in.Fog.Hidden {
		b.WriteString(`<div data-fog data-hidden="true"><p>Fog: the map is fully hidden.</p></div>` + "\n")
		return b.String()
	}
	fmt.Fprintf(&b, `<div data-fog aria-hidden="true">`+"\n")
	for _, r := range in.Fog.Rects {
		left := float64(r.X) * 100 / float64(in.Cal.Grid.Cols)
		top := float64(r.Y) * 100 / float64(in.Cal.Grid.Rows)
		w := float64(r.W) * 100 / float64(in.Cal.Grid.Cols)
		h := float64(r.H) * 100 / float64(in.Cal.Grid.Rows)
		fmt.Fprintf(&b, `<div data-fog-rect style="left:%.2f%%;top:%.2f%%;width:%.2f%%;height:%.2f%%"></div>`+"\n",
			left, top, w, h)
	}
	b.WriteString("</div>\n")
	// Text equivalent (screen-reader + never-color-only rule).
	if len(in.Fog.Rects) == 0 {
		b.WriteString(`<p data-fog-text>Fog: no hidden areas.</p>` + "\n")
	} else {
		var parts []string
		for _, r := range in.Fog.Rects {
			parts = append(parts, fmt.Sprintf("columns %d–%d, rows %d–%d", r.X, r.X+r.W-1, r.Y, r.Y+r.H-1))
		}
		fmt.Fprintf(&b, `<p data-fog-text>Fog hides %d areas: %s.</p>`+"\n",
			len(parts), html.EscapeString(strings.Join(parts, "; ")))
	}
	return b.String()
}

func renderInitiative(in FragmentInput) string {
	var b strings.Builder
	b.WriteString(`<section aria-label="Initiative" data-initiative><h2>Initiative</h2>` + "\n")
	b.WriteString(`<ol aria-live="polite">` + "\n")
	if len(in.Snap.Initiative) == 0 {
		b.WriteString(`<li>No initiative order yet.</li>` + "\n")
	}
	for i, e := range in.Snap.Initiative {
		hp := ""
		for _, t := range in.Snap.Tokens {
			if t.ID == e.ID {
				hp = fmt.Sprintf(` <span>%d/%d HP</span>`, t.HP, t.MaxHP)
				break
			}
		}
		fmt.Fprintf(&b, `<li data-entry="%s"><span>%d. %s</span> <span>order %d</span>%s</li>`+"\n",
			html.EscapeString(e.ID), i+1, html.EscapeString(e.Name), e.Order, hp)
	}
	b.WriteString("</ol>\n")
	if in.IsGM {
		b.WriteString(`<form data-init-form><label>Reorder (comma-separated token ids, first acts first) ` +
			`<input name="order" size="40" autocomplete="off"></label> ` +
			`<button type="submit">Set order</button></form>` + "\n")
	}
	b.WriteString("</section>\n")
	return b.String()
}

func renderDiceTray(in FragmentInput) string {
	var b strings.Builder
	b.WriteString(`<section aria-label="Dice tray" data-dice><h2>Dice tray</h2>` + "\n")
	b.WriteString(`<form data-roll-form><label>Notation (e.g. 1d20+5) ` +
		`<input name="notation" size="12" autocomplete="off" spellcheck="false"></label> ` +
		`<label><input type="checkbox" name="blind" value="1"> Blind (GM only)</label> ` +
		`<button type="submit">Roll</button></form>` + "\n")
	b.WriteString(`<ol data-dice-log aria-live="polite" aria-label="Recent rolls"></ol>` + "\n")
	b.WriteString("</section>\n")
	return b.String()
}

func renderTokenList(in FragmentInput) string {
	var b strings.Builder
	b.WriteString(`<section aria-label="Token list" data-tokens-list><h2>Tokens</h2>` + "\n")
	b.WriteString(`<p>Keyboard: Tab to the list, arrows choose a token, ` +
		`Control plus arrows move it one cell. Coordinates below move by text.</p>` + "\n")
	fmt.Fprintf(&b, `<div role="listbox" aria-label="Tokens, %d total" data-tokenbox tabindex="0">`+"\n", len(in.Snap.Tokens))
	for i, t := range in.Snap.Tokens {
		tab := "-1"
		sel := "false"
		if i == 0 {
			tab, sel = "0", "true"
		}
		hidden := ""
		if t.Hidden {
			hidden = " (hidden from party)"
		}
		fmt.Fprintf(&b, `<div role="option" tabindex="%s" aria-selected="%s" data-token-option="%s">%s at column %d, row %d, %d of %d hit points%s</div>`+"\n",
			tab, sel, html.EscapeString(t.ID), html.EscapeString(t.Name), t.X, t.Y, t.HP, t.MaxHP, hidden)
	}
	b.WriteString("</div>\n")
	// Text-coordinate mover (P08: arrow-key move + text coordinates).
	b.WriteString(`<form data-move-form><label>Token <input name="token" size="16" autocomplete="off"></label> ` +
		`<label>Column <input name="x" type="number" min="0" inputmode="numeric"></label> ` +
		`<label>Row <input name="y" type="number" min="0" inputmode="numeric"></label> ` +
		`<button type="submit">Move</button></form>` + "\n")
	b.WriteString(`<p data-move-state role="status" aria-live="polite"></p>` + "\n")
	b.WriteString("</section>\n")
	return b.String()
}

// renderDoors renders the GM door panel (A-VTT2 D10): one row per sidecar
// portal with its effective live state as TEXT (never color-only) and a
// toggle button posting {op:"door"}. Players never see this section.
func renderDoors(in FragmentInput) string {
	var b strings.Builder
	b.WriteString(`<section aria-label="Doors" data-doors><h2>Doors (GM)</h2>` + "\n")
	b.WriteString(`<ol data-door-list aria-live="polite">` + "\n")
	for _, d := range in.Snap.Doors {
		col := int(math.Floor((d.A[0] + d.B[0]) / 2))
		row := int(math.Floor((d.A[1] + d.B[1]) / 2))
		state, action := "closed", "Open"
		if !d.Closed {
			state, action = "open", "Close"
		}
		fmt.Fprintf(&b, `<li data-door-row="%d" data-open="%t"><span>Door %d (column %d, row %d): </span><span data-door-state>%s</span> <button type="button" data-door-toggle="%d" data-open="%t" aria-pressed="%t">%s</button></li>`+"\n",
			d.Idx, !d.Closed, d.Idx+1, col, row, state, d.Idx, !d.Closed, !d.Closed, action)
	}
	b.WriteString("</ol>\n</section>\n")
	return b.String()
}

func renderFogControls(in FragmentInput) string {
	var b strings.Builder
	b.WriteString(`<section aria-label="Fog controls" data-fog-controls><h2>Fog (GM)</h2>` + "\n")
	b.WriteString(`<p><button type="button" data-fog-all data-mode="reveal">Reveal all</button> ` +
		`<button type="button" data-fog-all data-mode="hide">Hide all</button> ` +
		`<button type="button" data-fog-reset>Reset to sidecar</button></p>` + "\n")
	b.WriteString(`<form data-fog-form><label>Column <input name="x" type="number" min="0" inputmode="numeric"></label> ` +
		`<label>Row <input name="y" type="number" min="0" inputmode="numeric"></label> ` +
		`<label>Width <input name="w" type="number" min="1" inputmode="numeric"></label> ` +
		`<label>Height <input name="h" type="number" min="1" inputmode="numeric"></label> ` +
		`<button type="submit" name="mode" value="hide">Hide area</button> ` +
		`<button type="submit" name="mode" value="reveal">Reveal area</button></form>` + "\n")
	b.WriteString(`<p data-fog-state role="status" aria-live="assertive"></p>` + "\n")
	b.WriteString("</section>\n")
	return b.String()
}

// PageInput extends FragmentInput with page chrome data.
type PageInput struct {
	FragmentInput
	ViewerLabel string
	StateURL    string
	StreamURL   string
	// T1: resolved theme selection ("" = unthemed). The VTT page is a
	// standalone document, so it carries the theme attributes and links
	// itself — same values the shell and sheet page render.
	Theme  string
	Scheme string
}

// RenderPage returns the full standalone map document. It is deliberately
// self-contained (no web/templates dependency — Lane F1 owns the shell):
// landmarks, skip link, inline CSS (44px targets, visible focus, reduced
// motion), the fragment, and the vanilla client (EventSource + debounced
// fetch; morphs never steal focus). T1 adds the theme attributes plus the
// core/theme/print stylesheet links so a themed campaign covers the table
// too; unthemed output is byte-identical to pre-T1.
func RenderPage(in PageInput) string {
	var b strings.Builder
	g := in.Cal.Grid
	b.WriteString("<!DOCTYPE html>\n" + web.HTMLOpenTag(in.Theme, in.Scheme) + "\n<head>\n" + `<meta charset="utf-8">` + "\n")
	b.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">` + "\n")
	fmt.Fprintf(&b, "<title>Table: %s</title>\n", html.EscapeString(displayTitle(in.Title, in.MapID)))
	if in.CSRF != "" {
		fmt.Fprintf(&b, `<meta name="csrf-token" content="%s">`+"\n", html.EscapeString(in.CSRF))
	}
	// T1: core tokens (so theme tokens resolve) + theme + print stylesheets.
	// app.css is shell structure this page does not use, but its :root tokens
	// are what themes re-point — without it a themed table would render
	// board chrome in default colors.
	b.WriteString(`<link rel="stylesheet" href="/static/core/app.css">` + "\n")
	b.WriteString(web.ThemeStylesheetLink(in.Theme))
	b.WriteString(`<link rel="stylesheet" href="/static/print.css" media="print">` + "\n")
	b.WriteString(`<style>` + pageCSS() + `</style>` + "\n</head>\n<body>\n")
	b.WriteString(`<a href="#vtt-map">Skip to map</a>` + "\n")
	b.WriteString("<header><nav aria-label=\"Table\"><p>")
	fmt.Fprintf(&b, `Table: %s · %s`, html.EscapeString(displayTitle(in.Title, in.MapID)), html.EscapeString(in.ViewerLabel))
	b.WriteString("</p></nav></header>\n<main>\n")
	fmt.Fprintf(&b, `<div id="%s" data-state-url="%s" data-stream-url="%s" data-csrf="%s">`+"\n",
		plugins.MapFragmentTarget, html.EscapeString(in.StateURL), html.EscapeString(in.StreamURL), html.EscapeString(in.CSRF))
	b.WriteString(RenderFragment(in.FragmentInput))
	b.WriteString("</div>\n</main>\n")
	fmt.Fprintf(&b, `<script data-vtt-client data-map-id="%s" data-cols="%d" data-rows="%d">`+"\n%s\n</script>\n",
		html.EscapeString(in.MapID), g.Cols, g.Rows, clientJS())
	// Pixi loader (amend A-VTT2, D8/D9): one module script, zero vendor bytes
	// fetched unless ?renderer=pixi is present. The DOM board below stays the
	// operable/a11y truth; the canvas is an enhancement that is aria-hidden on
	// mount and dropped (never partially shown) on any failure.
	fmt.Fprintf(&b, `<script type="module" data-pixi-loader data-map-id="%s" data-cols="%d" data-rows="%d" data-state-url="%s" data-stream-url="%s" data-vendor="/static/vendor/pixi.js" data-loader="/static/vtt/pixi-board.js">`+"\n%s\n</script>\n",
		html.EscapeString(in.MapID), g.Cols, g.Rows,
		html.EscapeString(in.StateURL), html.EscapeString(in.StreamURL), pixiLoaderJS())
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

// pixiLoaderJS gates the Pixi canvas mount on ?renderer=pixi (D9: per-load
// query param, no sticky preference). It fetches the same per-viewer-
// filtered state snapshot the DOM path uses, imports the pinned vendor ESM
// plus the first-party loader, and mounts. Every failure path leaves the
// SSR'd DOM board untouched and announces through the existing polite live
// region — the canvas never blank-replaces the operable board.
func pixiLoaderJS() string {
	return `(function () {
"use strict";
var el = document.querySelector("[data-pixi-loader]");
if (!el) return;
var d = el.dataset;
var q = new URLSearchParams(location.search);
if (q.get("renderer") !== "pixi") return; // DOM is the default path: no vendor bytes fetched
var board = document.querySelector("[data-board]");
var status = document.querySelector("[data-sse-state]");
var reduceMotion = !!(window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches);
function say(msg) { if (status) status.textContent = msg; }
if (!board) return;
fetch(d.stateUrl, {headers: {"Accept": "application/json"}}).then(function (r) {
  if (!r.ok) throw new Error("state " + r.status);
  return r.json();
}).then(function (snap) {
  return Promise.all([import(d.vendor), import(d.loader)]).then(function (mods) {
    // Re-resolve the board at call time: the SSE hello resync may have
    // morphed the fragment (replacing this element) while the bundles loaded.
    var live = document.querySelector("[data-board]");
    if (!live) throw new Error("board element gone");
    return mods[1].bootPixi(mods[0], live, {
      reduceMotion: reduceMotion, snapshot: snap,
      cols: parseInt(d.cols, 10), rows: parseInt(d.rows, 10),
      stateURL: d.stateUrl, streamURL: d.streamUrl,
    });
  });
}).then(function (view) {
  if (!view) throw new Error("pixi boot declined");
  // The canvas is a decorative overlay: it is aria-hidden (pixi-board.js)
  // and pointer-transparent (CSS), so the DOM board underneath stays the
  // operable, keyboard-reachable truth — hiding it would take the token
  // buttons out of the a11y tree while they remain the only write path.
  document.documentElement.classList.add("pixi-on");
  say("Canvas board active. Keyboard controls remain available.");
}).catch(function () {
  // No WebGL/Canvas, import failed, asset load failed, or boot threw:
  // stay on the shipped DOM renderer, announced via the status line.
  document.documentElement.classList.remove("pixi-on");
  say("Canvas unavailable, DOM board kept.");
});
})();`
}

func displayTitle(title, mapID string) string {
	if title != "" {
		return title
	}
	return mapID
}

func pageCSS() string {
	return `:root{color-scheme:light dark}
body{font-family:system-ui,sans-serif;line-height:1.4;margin:0 auto;max-width:72rem;padding:1rem}
a[href="#vtt-map"]{position:absolute;left:-9999px}
a[href="#vtt-map"]:focus{position:static}
:focus-visible{outline:3px solid currentColor;outline-offset:2px}
button,input[type=submit]{min-width:44px;min-height:44px}
[data-board]{position:relative;border:2px solid currentColor;max-width:100%}
[data-board-image] img{display:block;width:100%;height:auto}
/* Pixi parity canvas (A-VTT2): a DECORATIVE overlay of the same state —
   aria-hidden in pixi-board.js and pointer-transparent, so the DOM board
   underneath stays the operable, keyboard-reachable, screen-reader truth
   (spec §6). The canvas never intercepts clicks or focus. */
[data-pixi-canvas]{position:absolute;inset:0;width:100%;height:100%;z-index:5;pointer-events:none}
[data-grid]{position:absolute;inset:0;pointer-events:none;opacity:.3;background-image:linear-gradient(to right,currentColor 1px,transparent 1px),linear-gradient(to bottom,currentColor 1px,transparent 1px)}
[data-tokens]{position:absolute;inset:0}
[data-token-wrap]{position:absolute;transform:translate(-50%,-50%);display:flex;align-items:center;gap:.25em}
[data-token]{display:flex;align-items:center;justify-content:center;gap:.15em;min-width:44px;min-height:44px;border-radius:50%;border:2px solid currentColor;background:ButtonFace;font-weight:bold}
/* Token face is CSS-generated (never DOM text): the aria-label is the full
   text equivalent, so WCAG 2.5.3 has no visible text to mismatch. */
[data-token]::before{content:var(--face,"?")}
[data-token]::after{content:var(--hp,"")}
[data-token][aria-pressed=true]{outline:3px solid currentColor}
[data-fog]{position:absolute;inset:0;pointer-events:none}
[data-fog-rect]{position:absolute;background:rgba(0,0,0,.72);border:1px dashed #fff}
[data-fog][data-hidden=true]{position:static;background:rgba(0,0,0,.85);color:#fff;padding:1rem}
[data-token-option]{padding:.5rem;border:1px solid transparent}
[data-token-option][aria-selected=true]{border-color:currentColor}
[data-tag]{border:1px solid currentColor;padding:0 .4em;font-size:.85em}
ol[data-dice-log]{list-style:none;padding:0}
@media (prefers-reduced-motion:reduce){*{transition:none!important;animation:none!important}}`
}

// clientJS is the vanilla VTT client: EventSource over the frozen /events
// stream (per-viewer filtered server-side), click-to-move + debounced PATCH,
// keyboard list, resync-on-reconnect. It keeps NO authoritative state: every
// change refetches the fragment/snapshot, so clients cannot desync.
func clientJS() string {
	return `(function () {
"use strict";
var root = document.getElementById("vtt-map");
if (!root) return;
var script = document.querySelector("[data-vtt-client]");
var mapID = script.getAttribute("data-map-id");
var COLS = parseInt(script.getAttribute("data-cols"), 10);
var ROWS = parseInt(script.getAttribute("data-rows"), 10);
var stateURL = root.getAttribute("data-state-url");
var streamURL = root.getAttribute("data-stream-url");
var csrf = root.getAttribute("data-csrf") || "";
var statusEl = root.querySelector("[data-sse-state]");
var moveState = root.querySelector("[data-move-state]");
var diceLog = root.querySelector("[data-dice-log]");
function say(el, msg) { if (el) el.textContent = msg; }
function headers(extra) {
  var h = {"Content-Type": "application/json", "X-CSRF-Token": csrf};
  for (var k in extra) h[k] = extra[k];
  return h;
}
function focusedInside() { return root.contains(document.activeElement) && document.activeElement !== document.body; }
var pendingMorph = null;
function applyMorph(html) {
  if (focusedInside()) { pendingMorph = html; return; }
  root.innerHTML = html;
  reattachPixi();
  rebind();
  pendingMorph = null;
}
// The pixi canvas lives inside [data-board] (inside the fragment), so an SSE
// morph would drop it. When a pixi view is live, re-attach the SAME canvas
// node (WebGL context intact) to the new board. The board is NOT aria-hidden:
// the canvas is a decorative overlay (aria-hidden + pointer-events:none) and
// the DOM stays the operable truth.
function reattachPixi() {
  var pv = window.__pixiView;
  if (!pv || !pv.canvasEl) return;
  var nb = root.querySelector("[data-board]");
  if (!nb) return;
  if (pv.canvasEl.parentNode !== nb) nb.appendChild(pv.canvasEl);
}
document.addEventListener("focusout", function () {
  if (pendingMorph !== null && !focusedInside()) { var h = pendingMorph; pendingMorph = null; applyMorph(h); }
});
function refetchFragment() {
  // The state URL may carry the demo identity query (?as=…): rewrite the
  // PATH only, then re-attach the query, or the /state→/fragment rewrite
  // silently misses and the SSE resync 404s on the demo tier.
  var qi = stateURL.indexOf("?");
  var base = qi >= 0 ? stateURL.slice(0, qi) : stateURL;
  var query = qi >= 0 ? stateURL.slice(qi) : "";
  var fragURL = base.replace(/\/api\/vtt\//, "/vtt/").replace(/\/state$/, "/fragment") + query;
  fetch(fragURL, {headers: {"Accept": "text/html"}}).then(function (r) {
    if (!r.ok) throw new Error("fragment " + r.status);
    return r.text();
  }).then(function (html) {
    // The fragment endpoint returns inner HTML for #vtt-map.
    applyMorph(html);
    say(statusEl, "Live updates: board refreshed.");
  }).catch(function () { say(statusEl, "Live updates: refresh failed, retrying on next event."); });
}
// --- debounced moves (P08: debounced PATCH + SSE broadcast + resync) ---
var moveTimer = null;
var lastMove = null;
function sendMove(token, x, y) {
  lastMove = {token: token, x: x, y: y, at: Date.now()};
  if (moveTimer) clearTimeout(moveTimer);
  moveTimer = setTimeout(function () {
    var m = lastMove; lastMove = null; moveTimer = null;
    fetch(stateURL, {method: "PATCH", headers: headers(), body: JSON.stringify({op: "move", token: m.token, x: m.x, y: m.y})})
      .then(function (r) {
        if (!r.ok) { say(moveState, "Move rejected, board unchanged."); refetchFragment(); return; }
        return r.json();
      }).then(function (tok) {
        if (tok) say(moveState, "Moved " + tok.name + " to column " + tok.x + ", row " + tok.y + ".");
      }).catch(function () { say(moveState, "Move failed, will resync."); refetchFragment(); });
  }, 250);
}
// --- token selection + click-to-move (degrades gracefully from drag) ---
var selected = null;
function rebind() {
  statusEl = root.querySelector("[data-sse-state]");
  moveState = root.querySelector("[data-move-state]");
  diceLog = root.querySelector("[data-dice-log]");
  root.querySelectorAll("[data-token]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      root.querySelectorAll("[data-token]").forEach(function (o) { o.setAttribute("aria-pressed", "false"); });
      btn.setAttribute("aria-pressed", "true");
      selected = btn.getAttribute("data-token");
      say(moveState, "Selected " + btn.getAttribute("data-name") + ". Click the board to move, or use the token list.");
      var box = root.querySelector("[data-tokenbox]");
      if (box) syncBox(box, selected);
    });
  });
  var board = root.querySelector("[data-board]");
  if (board) board.addEventListener("click", function (ev) {
    // Token buttons and their sibling sheet links own their clicks; only a
    // bare board click moves the selected token.
    if (ev.target.closest("[data-token]") || ev.target.closest("a") || !selected) return;
    var rect = board.getBoundingClientRect();
    var x = Math.floor((ev.clientX - rect.left) / rect.width * COLS);
    var y = Math.floor((ev.clientY - rect.top) / rect.height * ROWS);
    if (x < 0 || y < 0 || x >= COLS || y >= ROWS) return;
    sendMove(selected, x, y);
  });
  bindBox(); bindForms();
}
function syncBox(box, id) {
  box.querySelectorAll("[data-token-option]").forEach(function (opt) {
    var on = opt.getAttribute("data-token-option") === id;
    opt.setAttribute("aria-selected", on ? "true" : "false");
    opt.tabIndex = on ? 0 : -1;
  });
}
function boxSelected(box) {
  var cur = box.querySelector('[data-token-option][aria-selected="true"]');
  return cur ? cur.getAttribute("data-token-option") : null;
}
function bindBox() {
  var box = root.querySelector("[data-tokenbox]");
  if (!box || box.__bound) return;
  box.__bound = true;
  box.addEventListener("click", function (ev) {
    var opt = ev.target.closest("[data-token-option]");
    if (!opt) return;
    selected = opt.getAttribute("data-token-option");
    syncBox(box, selected);
  });
  box.addEventListener("keydown", function (ev) {
    var opts = Array.prototype.slice.call(box.querySelectorAll("[data-token-option]"));
    if (!opts.length) return;
    var idx = opts.findIndex(function (o) { return o.getAttribute("aria-selected") === "true"; });
    function choose(i) {
      i = (i + opts.length) % opts.length;
      selected = opts[i].getAttribute("data-token-option");
      syncBox(box, selected);
      opts[i].focus();
    }
    var step = {x: 0, y: 0};
    if (ev.ctrlKey || ev.metaKey) {
      if (ev.key === "ArrowLeft") step.x = -1;
      else if (ev.key === "ArrowRight") step.x = 1;
      else if (ev.key === "ArrowUp") step.y = -1;
      else if (ev.key === "ArrowDown") step.y = 1;
      else return;
      ev.preventDefault();
      var id = selected || boxSelected(box);
      if (!id) return;
      var cur = root.querySelector('[data-token="' + id + '"]');
      var cx = cur ? parseInt(cur.getAttribute("data-x"), 10) : 0;
      var cy = cur ? parseInt(cur.getAttribute("data-y"), 10) : 0;
      var nx = Math.min(COLS - 1, Math.max(0, cx + step.x));
      var ny = Math.min(ROWS - 1, Math.max(0, cy + step.y));
      sendMove(id, nx, ny);
      return;
    }
    if (ev.key === "ArrowDown" || ev.key === "ArrowRight") { ev.preventDefault(); choose(idx + 1); }
    else if (ev.key === "ArrowUp" || ev.key === "ArrowLeft") { ev.preventDefault(); choose(idx - 1); }
    else if (ev.key === "Home") { ev.preventDefault(); choose(0); }
    else if (ev.key === "End") { ev.preventDefault(); choose(opts.length - 1); }
  });
}
function bindForms() {
  var mf = root.querySelector("[data-move-form]");
  if (mf && !mf.__bound) {
    mf.__bound = true;
    mf.addEventListener("submit", function (ev) {
      ev.preventDefault();
      var fd = new FormData(mf);
      sendMove(String(fd.get("token") || selected || ""), parseInt(fd.get("x"), 10), parseInt(fd.get("y"), 10));
    });
  }
  var rf = root.querySelector("[data-roll-form]");
  var rollForm = root.querySelector("[data-roll-form], [data-dice] form");
  if (rollForm && !rollForm.__bound) {
    rollForm.__bound = true;
    rollForm.addEventListener("submit", function (ev) {
      ev.preventDefault();
      var fd = new FormData(rollForm);
      fetch(stateURL, {method: "POST", headers: headers(), body: JSON.stringify({op: "roll", notation: String(fd.get("notation") || ""), blind: !!fd.get("blind")})})
        .then(function (r) {
          if (!r.ok) say(statusEl, "Roll rejected.");
          rollForm.reset();
        }).catch(function () { say(statusEl, "Roll failed."); });
    });
  }
  root.querySelectorAll("[data-fog-all]").forEach(function (btn) {
    if (btn.__bound) return;
    btn.__bound = true;
    btn.addEventListener("click", function () {
      fetch(stateURL, {method: "POST", headers: headers(), body: JSON.stringify({op: "fog", mode: btn.getAttribute("data-mode")})})
        .then(function (r) { say(root.querySelector("[data-fog-state]"), r.ok ? "Fog updated." : "Fog change rejected."); })
        .catch(function () { say(root.querySelector("[data-fog-state]"), "Fog change failed."); });
    });
  });
  var ff = root.querySelector("[data-fog-form]");
  if (ff && !ff.__bound) {
    ff.__bound = true;
    ff.addEventListener("submit", function (ev) {
      ev.preventDefault();
      var fd = new FormData(ff);
      var mode = (document.activeElement && document.activeElement.value) || "hide";
      fetch(stateURL, {method: "POST", headers: headers(), body: JSON.stringify({op: "fog", mode: mode,
        rect: {x: parseInt(fd.get("x"), 10) || 0, y: parseInt(fd.get("y"), 10) || 0, w: parseInt(fd.get("w"), 10) || 1, h: parseInt(fd.get("h"), 10) || 1}})})
        .then(function (r) { say(root.querySelector("[data-fog-state]"), r.ok ? "Fog updated." : "Fog change rejected."); })
        .catch(function () { say(root.querySelector("[data-fog-state]"), "Fog change failed."); });
    });
  }
  var init = root.querySelector("[data-init-form]");
  if (init && !init.__bound) {
    init.__bound = true;
    init.addEventListener("submit", function (ev) {
      ev.preventDefault();
      var order = String(new FormData(init).get("order") || "").split(",").map(function (s) { return s.trim(); }).filter(Boolean);
      fetch(stateURL, {method: "POST", headers: headers(), body: JSON.stringify({op: "initiative", order: order})})
        .then(function (r) { if (!r.ok) say(statusEl, "Initiative change rejected."); })
        .catch(function () { say(statusEl, "Initiative change failed."); });
    });
  }
  bindDoors();
  void rf; void mf;
}
// --- door toggles (A-VTT2: GM-only; optimistic text + fog-update refetch) ---
function bindDoors() {
  root.querySelectorAll("[data-door-toggle]").forEach(function (btn) {
    if (btn.__bound) return;
    btn.__bound = true;
    btn.addEventListener("click", function () {
      var idx = parseInt(btn.getAttribute("data-door-toggle"), 10);
      var open = btn.getAttribute("data-open") !== "true";
      fetch(stateURL, {method: "POST", headers: headers(), body: JSON.stringify({op: "door", idx: idx, open: open})})
        .then(function (r) {
          if (!r.ok) { say(statusEl, "Door change rejected."); return; }
          var row = root.querySelector('[data-door-row="' + idx + '"]');
          var st = row ? row.querySelector("[data-door-state]") : null;
          if (st) st.textContent = open ? "open" : "closed";
          if (row) row.setAttribute("data-open", open ? "true" : "false");
          btn.setAttribute("data-open", open ? "true" : "false");
          btn.setAttribute("aria-pressed", open ? "true" : "false");
          btn.textContent = open ? "Close" : "Open";
          say(statusEl, "Door " + (idx + 1) + (open ? " opened." : " closed."));
        })
        .catch(function () { say(statusEl, "Door change failed."); });
    });
  });
}
// --- SSE: hello resyncs, board events refetch, dice appends, flips reload ---
function connect() {
  var src;
  try { src = new EventSource(streamURL); }
  catch (e) { say(statusEl, "Live updates: unavailable."); return; }
  src.addEventListener("hello", function () { say(statusEl, "Live updates: connected, resyncing."); refetchFragment(); });
  ["token-move", "token-hp", "fog-update", "initiative-update", "encounter-spawn", "session-recap"].forEach(function (name) {
    src.addEventListener(name, function () { refetchFragment(); });
  });
  ["dice-roll", "dice-blind"].forEach(function (name) {
    src.addEventListener(name, function (ev) {
      try {
        var d = JSON.parse(ev.data);
        var li = document.createElement("li");
        var total = (d.total === null || d.total === undefined) ? "redacted" : String(d.total);
        li.textContent = (d.actor || "Someone") + " rolled " + (d.notation || "?") + ": " + total + (d.blind ? " (blind)" : "");
        if (diceLog) { diceLog.prepend(li); while (diceLog.children.length > 20) diceLog.lastChild.remove(); }
        say(statusEl, li.textContent);
      } catch (e) { /* malformed payload: ignore, next event resyncs */ }
    });
  });
  src.addEventListener("secret-flip", function () { say(statusEl, "Live updates: visibility changed, reloading."); setTimeout(function () { location.reload(); }, 500); });
  src.onerror = function () { say(statusEl, "Live updates: disconnected, retrying."); };
}
rebind();
connect();
})();`
}
