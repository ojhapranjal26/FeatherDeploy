// Package web holds the compiled frontend assets embedded into the binary.
// During development, web/dist/index.html is a placeholder page.
// The build script (build.sh) overwrites web/dist/ with the real Vite output
// before running go build, producing a self-contained production binary.
package web

import "embed"

// Dist contains the compiled React frontend.
// Access files via fs.Sub(Dist, "dist") to strip the "dist/" prefix.
//
//go:embed all:dist
var Dist embed.FS
