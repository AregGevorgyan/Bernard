// Package web carries Bernard's front-end assets, embedded into the binary.
//
// The embeds live here rather than next to main because go:embed patterns
// cannot climb out of their own directory.
package web

import "embed"

// Portal is the built React submission portal and admin console.
// Run `make portal` before building the server or this will be a stub page.
//
//go:embed all:portal/dist
var Portal embed.FS

// Display is the TV page: one hand-written HTML file, no build step.
//
//go:embed display
var Display embed.FS
