// Package docs exposes the repository documentation to the embedded web reader.
package docs

import "embed"

// Files contains every Markdown document shipped with the server binary.
//
//go:embed *.md */*.md */*/*.md
var Files embed.FS
