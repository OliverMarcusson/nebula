// Package mods embeds the Claude Code mod so `nebula setup` can install it
// without a checkout.
package mods

import "embed"

//go:embed nebula/.claude-plugin/plugin.json nebula/hooks
var Nebula embed.FS
