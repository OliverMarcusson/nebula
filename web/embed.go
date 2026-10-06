// Package web embeds the built dashboard. Run `npm run build` in this
// directory before `go build` to include it; otherwise only a notice is served.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
