package vtt

import (
	"strings"
	"testing"
)

// T1: the VTT page is a standalone document, so it carries the theme
// attributes and the core/theme/print stylesheet links itself. Unthemed
// output must stay byte-identical to the pre-T1 document shape.
func TestRenderPageThemeHead(t *testing.T) {
	in := PageInput{
		FragmentInput: FragmentInput{
			MapID: "arena",
			Cal:   Calibration{Grid: Grid{Cols: 4, Rows: 3, CellPx: 70}},
		},
	}

	plain := RenderPage(in)
	if !strings.HasPrefix(plain, "<!DOCTYPE html>\n<html lang=\"en\">\n<head>") {
		t.Errorf("unthemed VTT page lost its plain <html> tag:\n%.80s", plain)
	}
	// T1 gap fixes apply unthemed too: print.css was served but never linked
	// anywhere, and core tokens load so a themed session resolves.
	for _, want := range []string{
		`<link rel="stylesheet" href="/static/core/app.css">`,
		`<link rel="stylesheet" href="/static/print.css" media="print">`,
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("unthemed VTT page missing %q", want)
		}
	}
	for _, unwanted := range []string{"data-theme", "/static/themes/"} {
		if strings.Contains(plain, unwanted) {
			t.Errorf("unthemed VTT page carries %q", unwanted)
		}
	}

	in.Theme, in.Scheme = "cartographer", "dark"
	themed := RenderPage(in)
	for _, want := range []string{
		`<html lang="en" data-theme="cartographer" data-scheme="dark">`,
		`<link rel="stylesheet" href="/static/core/app.css">`,
		`<link rel="stylesheet" href="/static/themes/cartographer.css">`,
		`<link rel="stylesheet" href="/static/print.css" media="print">`,
	} {
		if !strings.Contains(themed, want) {
			t.Errorf("themed VTT page missing %q", want)
		}
	}
}
