package gather

import (
	"path"
	"strings"
)

// ignoredDirs and ignoredFiles are paths no reviewer should spend tokens on:
// lockfiles, build output, vendored and generated code.
var ignoredDirs = []string{"vendor/", "node_modules/", "_build/", "deps/", "dist/", "build/", "target/", ".yarn/", "__snapshots__/"}

var ignoredFiles = []string{
	"package-lock.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb", "mix.lock", "go.sum",
	"Cargo.lock", "composer.lock", "Gemfile.lock", "poetry.lock", "uv.lock", "Pipfile.lock",
}

var ignoredGlobs = []string{"*.lock", "*.min.js", "*.min.css", "*.map", "*.snap", "*.pb.go", "*_pb2.py", "*.generated.*", "*.g.dart"}

// Ignored reports whether a changed path should be skipped entirely.
func Ignored(p string) bool {
	for _, d := range ignoredDirs {
		if strings.HasPrefix(p, d) || strings.Contains(p, "/"+d) {
			return true
		}
	}
	base := path.Base(p)
	for _, f := range ignoredFiles {
		if base == f {
			return true
		}
	}
	for _, g := range ignoredGlobs {
		if ok, _ := path.Match(g, base); ok {
			return true
		}
	}
	return strings.Contains(strings.ToLower(base), "generated")
}

// FilterIgnored drops hunks on ignored paths and returns the skipped paths.
func FilterIgnored(hunks []Hunk) (kept []Hunk, skipped []string) {
	seen := map[string]bool{}
	for _, h := range hunks {
		if Ignored(h.Path) {
			if !seen[h.Path] {
				seen[h.Path] = true
				skipped = append(skipped, h.Path)
			}
			continue
		}
		kept = append(kept, h)
	}
	return kept, skipped
}
