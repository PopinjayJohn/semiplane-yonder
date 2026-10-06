//go:build tools

// Package tools pins the frozen dependency set (spec §2) so `go mod tidy`
// retains exact versions even before product code imports them.
package tools

import (
	_ "github.com/a-h/templ/runtime"
	_ "github.com/fsnotify/fsnotify"
	_ "github.com/starfederation/datastar-go/datastar"
	// goldmark core pinned; the +obsidian assembly (wikilinks, embeds, tags,
	// callouts per p02) is explicitly deferred to Lane A, which owns
	// internal/markdown and selects the extension module(s) in Phase 1.
	// No other markdown lib may be added without an amend (frozen set, spec §2).
	_ "github.com/yuin/goldmark"
	_ "golang.org/x/crypto/argon2"
	// x/image draw only: scaling via draw.ApproxBiLinear etc.; no external
	// imaging lib (spec §2 "draw/resize only" = draw APIs, nothing else).
	_ "golang.org/x/image/draw"
	_ "modernc.org/sqlite"
)
