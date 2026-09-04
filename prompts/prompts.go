// Package prompts embeds every ticket prompt so run-review ships as one binary.
package prompts

import "embed"

//go:embed *.md
var FS embed.FS
