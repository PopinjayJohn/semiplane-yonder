package vtt

// Pixi loader wiring (amend A-VTT2 D8/D9): the page carries exactly one
// module script that gates the vendored import on ?renderer=pixi, names the
// pinned vendor file, and never removes the DOM board. The DOM path stays
// byte-identical apart from the added script (budget: <100KB excl. vendored
// JS — the loader is ~1KB inline and fetches nothing unless asked).
import (
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/plugins"
)

func TestRenderPagePixiLoader(t *testing.T) {
	in := PageInput{
		FragmentInput: FragmentInput{
			MapID: "arena", Title: "Arena",
			Cal:  Calibration{Grid: Grid{Cols: 20, Rows: 14}},
			Snap: plugins.StateSnapshot{Version: plugins.FragmentVersion, MapID: "arena"},
		},
		ViewerLabel: "GM gm",
		StateURL:    "/api/vtt/arena/state?as=gm",
		StreamURL:   "/events?map=arena&as=gm",
	}
	page := RenderPage(in)
	for _, want := range []string{
		`data-pixi-loader`,
		`data-vendor="/static/vendor/pixi.js"`,
		`data-loader="/static/vtt/pixi-board.js"`,
		`data-state-url="/api/vtt/arena/state?as=gm"`,
		`data-stream-url="/events?map=arena&amp;as=gm"`,
		`"renderer") !== "pixi") return;`, // DOM stays the default path
		`data-vtt-client`,                 // the DOM client still ships
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// Exactly one loader script element, and it is a module (import() needs one).
	if n := strings.Count(page, `<script type="module" data-pixi-loader`); n != 1 {
		t.Errorf("pixi loader script elements = %d, want 1", n)
	}
	if !strings.Contains(page, `<script type="module" data-pixi-loader`) {
		t.Error("loader script is not a module")
	}
	// The vendor URL is never fetched by the server: no <script src> for it.
	if strings.Contains(page, `src="/static/vendor/pixi.js"`) || strings.Contains(page, `src="/static/vtt/pixi-board.js"`) {
		t.Error("vendor/loader bytes must load via dynamic import only, never a blocking script src")
	}
	// The DOM fragment (the operable truth) is always fully rendered.
	if !strings.Contains(page, `data-vtt-board`) || !strings.Contains(page, `role="listbox"`) {
		t.Error("DOM board missing from the SSR page")
	}
}

// jsBalanced walks JS source (string/comment/regex-literal aware enough for
// our own scripts) and reports unbalanced (), {}, [].
func jsBalanced(src string) bool {
	depth := map[byte]int{}
	var pairs = map[byte]byte{')': '(', '}': '{', ']': '['}
	inStr, strCh := false, byte(0)
	inLine, inBlock := false, false
	prevSig := byte(0) // last significant char before a '/': regex vs divide
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
			}
		case inBlock:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				inBlock = false
				i++
			}
		case inStr:
			switch c {
			case '\\':
				i++
			case strCh:
				inStr = false
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			inLine = true
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			inBlock = true
			i++
		case c == '/' && strings.IndexByte("(,=:[!&|?{;", prevSig) >= 0:
			// Regex literal (e.g. stateURL.replace(/\/api\/vtt\//, …)):
			// skip to the closing unescaped '/'.
			for j := i + 1; j < len(src); j++ {
				if src[j] == '\\' {
					j++
					continue
				}
				if src[j] == '/' {
					i = j
					break
				}
			}
		case c == '"' || c == '\'':
			inStr, strCh = true, c
		case c == '(' || c == '{' || c == '[':
			depth[c]++
			prevSig = c
		case c == ')' || c == '}' || c == ']':
			depth[pairs[c]]--
			if depth[pairs[c]] < 0 {
				return false
			}
			prevSig = c
		default:
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				prevSig = c
			}
		}
	}
	for _, n := range depth {
		if n != 0 {
			return false
		}
	}
	return !inStr && !inLine && !inBlock
}

// A parse error in the inline client kills EVERYTHING (SSE, moves, dice,
// fog) — it shipped once (the fog-form fetch was missing a paren) and only a
// browser console caught it. Pin both emitted scripts as balanced.
func TestRenderPageScriptsBalanced(t *testing.T) {
	in := PageInput{
		FragmentInput: FragmentInput{
			MapID: "arena", Title: "Arena",
			Cal:  Calibration{Grid: Grid{Cols: 20, Rows: 14}},
			Snap: plugins.StateSnapshot{Version: plugins.FragmentVersion, MapID: "arena"},
		},
		ViewerLabel: "GM gm",
		StateURL:    "/api/vtt/arena/state",
		StreamURL:   "/events?map=arena",
	}
	page := RenderPage(in)
	if !jsBalanced(clientJS()) {
		t.Error("clientJS() is not balanced (a parse error kills the whole VTT client)")
	}
	if !jsBalanced(pixiLoaderJS()) {
		t.Error("pixiLoaderJS() is not balanced (a parse error kills the canvas mount)")
	}
	if !strings.Contains(page, `h: parseInt(fd.get("h"), 10) || 1}})})`) {
		t.Error("fog-form fetch call lost its balanced closer (regression of the shipped syntax error)")
	}
	// The SSE resync rewrites /api/vtt/<id>/state -> /vtt/<id>/fragment; the
	// demo identity query (?as=…) must survive the rewrite or every live
	// update on the demo tier 404s on the state route instead.
	for _, want := range []string{
		`var qi = stateURL.indexOf("?");`,
		`var base = qi >= 0 ? stateURL.slice(0, qi) : stateURL;`,
		`var fragURL = base.replace(/\/api\/vtt\//, "/vtt/").replace(/\/state$/, "/fragment") + query;`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("client missing the query-preserving refetch rewrite: %q", want)
		}
	}
	// A morph rewrites #vtt-map's innerHTML, which drops an appended pixi
	// canvas with it: applyMorph re-attaches the live view's canvas node.
	for _, want := range []string{
		`var pv = window.__pixiView;`,
		`if (pv.canvasEl.parentNode !== nb) nb.appendChild(pv.canvasEl);`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("client missing the pixi morph re-attach seam: %q", want)
		}
	}
	// The canvas is a DECORATIVE overlay: aria-hidden + pointer-transparent.
	// The DOM board is NOT hidden — hiding it would take the focusable token
	// buttons out of the a11y tree while they remain the only write path.
	if strings.Contains(page, `board.setAttribute("aria-hidden"`) {
		t.Error("the pixi mount must not aria-hide the DOM board (focusable content)")
	}
	if !strings.Contains(page, `[data-pixi-canvas]{position:absolute;inset:0;width:100%;height:100%;z-index:5;pointer-events:none}`) {
		t.Error("pageCSS missing the decorative pixi canvas overlay rule")
	}
	// The loader re-resolves the board element at call time: the hello
	// resync may have replaced it while the vendor bundle loaded.
	if !strings.Contains(page, `var live = document.querySelector("[data-board]");`) {
		t.Error("pixi loader must re-resolve the board after the bundles load")
	}
}

// A link-only map (imported calibration, no art on disk) still needs a board
// area: the section carries its grid's intrinsic aspect ratio so the grid
// overlay, tokens, and fog render; art-backed boards are unchanged.
func TestRenderBoardAspectRatioWithoutArt(t *testing.T) {
	in := FragmentInput{MapID: "tomb", Title: "tomb", Cal: Calibration{Grid: Grid{Cols: 96, Rows: 72, CellPx: 100}}}
	html := RenderFragment(in)
	if !strings.Contains(html, `<section aria-label="Battle map: tomb" data-board style="aspect-ratio:96 / 72;min-height:240px">`) {
		t.Errorf("art-less board missing its grid aspect ratio:\n%s", html)
	}
	in.Cal.Background = "maps/arena.png"
	html = RenderFragment(in)
	if !strings.Contains(html, `<section aria-label="Battle map: tomb" data-board>`) {
		t.Errorf("art-backed board must stay unstyled:\n%s", html)
	}
}
