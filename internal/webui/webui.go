// Package webui embeds the browser board.
//
// The assets are plain HTML/CSS/JS served straight out of the binary: no build
// step, no bundler, no node_modules. The board is a view over the same JSON API
// every other client uses, so a build pipeline would buy nothing but moving
// parts.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed assets
var assets embed.FS

// FS is the UI tree rooted at the asset directory, so callers open
// "index.html" rather than "assets/index.html".
var FS fs.FS = mustSub()

func mustSub() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic("webui: " + err.Error())
	}
	return sub
}
