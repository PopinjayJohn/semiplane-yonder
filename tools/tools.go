//go:build tools

// Package tools pins the frozen dependency set (spec §2) so `go mod tidy`
// retains exact versions even before product code imports them.
package tools

import (
	_ "github.com/a-h/templ/runtime"
	_ "github.com/fsnotify/fsnotify"
	_ "github.com/starfederation/datastar-go/datastar"
	_ "github.com/yuin/goldmark"
	_ "golang.org/x/crypto/argon2"
	_ "golang.org/x/image/draw"
	_ "modernc.org/sqlite"
)
