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
		`aria-hidden", "true"`,            // DOM board hidden, never removed
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
