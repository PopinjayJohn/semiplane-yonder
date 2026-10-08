package vtt

import (
	"fmt"
	"html"
	"strings"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/plugins"
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
	fmt.Fprintf(&b, `<section aria-label="Battle map: %s" data-board>`+"\n", html.EscapeString(title))
	// Background through the ACL-checked asset handler (bytes never served
	// here; unauthorized viewers 404 at /assets, fail closed).
	if in.Cal.Background != "" {
		fmt.Fprintf(&b, `<div data-board-image><img src="/assets/%s%s" alt="Map background for %s"></div>`+"\n",
			html.EscapeString(in.Cal.Background), html.EscapeString(in.AsQuery), html.EscapeString(title))
	} else {
		b.WriteString(`<div data-board-image><p>No background image set. Ask your GM to set one in the map sidecar.</p></div>` + "\n")
	}
	// Fog overlay + text equivalent.
	b.WriteString(renderFog(in))
	// Tokens as real buttons (keyboard-reachable, 44px, text labels).
	fmt.Fprintf(&b, `<div data-tokens role="group" aria-label="Tokens on %s">`+"\n", html.EscapeString(title))
	for _, t := range in.Snap.Tokens {
		left := (float64(t.X) + 0.5) * 100 / float64(g.Cols)
		top := (float64(t.Y) + 0.5) * 100 / float64(g.Rows)
		label := fmt.Sprintf("%s, column %d row %d, %d of %d hit points", t.Name, t.X, t.Y, t.HP, t.MaxHP)
		if t.Hidden {
			label += ", hidden from party"
		}
		initial := strings.ToUpper(string(runeOf(t.Name)))
		link := ""
		if page, ok := in.CharLinks[t.ID]; ok && page != "" {
			link = fmt.Sprintf(` <a href="/p/%s%s" data-char-link>sheet</a>`, html.EscapeString(page), html.EscapeString(in.AsQuery))
		}
		hiddenTag := ""
		if t.Hidden {
			hiddenTag = ` <span data-tag>hidden</span>`
		}
		fmt.Fprintf(&b, `<button type="button" data-token="%s" data-x="%d" data-y="%d" data-name="%s" style="left:%.2f%%;top:%.2f%%" aria-label="%s" aria-pressed="false"><span aria-hidden="true">%s</span><span data-hp>%d/%d</span>%s</button>%s`+"\n",
			html.EscapeString(t.ID), t.X, t.Y, html.EscapeString(t.Name), left, top,
			html.EscapeString(label), html.EscapeString(initial), t.HP, t.MaxHP, hiddenTag, link)
	}
	b.WriteString("</div>\n</section>\n")
	return b.String()
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
}

// RenderPage returns the full standalone map document. It is deliberately
// self-contained (no web/templates dependency — Lane F1 owns the shell):
// landmarks, skip link, inline CSS (44px targets, visible focus, reduced
// motion), the fragment, and the vanilla client (EventSource + debounced
// fetch; morphs never steal focus).
func RenderPage(in PageInput) string {
	var b strings.Builder
	g := in.Cal.Grid
	b.WriteString("<!DOCTYPE html>\n" + `<html lang="en">` + "\n<head>\n" + `<meta charset="utf-8">` + "\n")
	b.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">` + "\n")
	fmt.Fprintf(&b, "<title>Table: %s</title>\n", html.EscapeString(displayTitle(in.Title, in.MapID)))
	if in.CSRF != "" {
		fmt.Fprintf(&b, `<meta name="csrf-token" content="%s">`+"\n", html.EscapeString(in.CSRF))
	}
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
	b.WriteString("</body>\n</html>\n")
	return b.String()
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
[data-tokens]{position:absolute;inset:0}
[data-token]{position:absolute;transform:translate(-50%,-50%);min-width:44px;min-height:44px;border-radius:50%;border:2px solid currentColor;background:ButtonFace;font-weight:bold}
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
  rebind();
  pendingMorph = null;
}
document.addEventListener("focusout", function () {
  if (pendingMorph !== null && !focusedInside()) { var h = pendingMorph; pendingMorph = null; applyMorph(h); }
});
function refetchFragment() {
  var fragURL = stateURL.replace(/\/api\/vtt\//, "/vtt/").replace(/\/state$/, "/fragment");
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
    if (ev.target.closest("[data-token]") || !selected) return;
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
        rect: {x: parseInt(fd.get("x"), 10) || 0, y: parseInt(fd.get("y"), 10) || 0, w: parseInt(fd.get("w"), 10) || 1, h: parseInt(fd.get("h"), 10) || 1}}})
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
  void rf; void mf;
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
