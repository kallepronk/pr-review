// Package gather turns a PR into the deterministic inputs every ticket needs:
// per-file hunks with changed line numbers, project instruction files, and the
// list of changed paths. No model is involved here.
package gather

import (
	"strconv"
	"strings"
)

// Hunk is one file's diff (or a chunk of it), plus the new-side line numbers
// the PR touched. GitHub only accepts inline comments on those lines.
type Hunk struct {
	Path         string
	Patch        string
	ChangedLines []int
}

// MaxHunkLines is the chunking threshold; a small model reviews a 300-line
// hunk far better than a 3000-line one.
const MaxHunkLines = 300

// SplitDiff splits a unified diff into per-file hunks, chunking big files at
// @@ boundaries. Deleted and binary files are dropped: nothing to comment on.
func SplitDiff(diff string) []Hunk {
	var hunks []Hunk
	for _, file := range strings.Split(diff, "\ndiff --git ") {
		file = strings.TrimPrefix(file, "diff --git ")
		if strings.TrimSpace(file) == "" {
			continue
		}
		path := newPath(file)
		if path == "" || strings.Contains(file, "\nBinary files ") {
			continue
		}
		header, body, ok := strings.Cut(file, "\n@@")
		if !ok {
			continue
		}
		header = "diff --git " + header + "\n"
		var chunk strings.Builder
		var lines []int
		flush := func() {
			if chunk.Len() > 0 {
				hunks = append(hunks, Hunk{Path: path, Patch: header + chunk.String(), ChangedLines: lines})
				chunk.Reset()
				lines = nil
			}
		}
		for _, h := range strings.Split("@@"+body, "\n@@") {
			h = "@@" + strings.TrimPrefix(h, "@@")
			hl := changedLines(h)
			if chunk.Len() > 0 && strings.Count(chunk.String(), "\n")+strings.Count(h, "\n") > MaxHunkLines {
				flush()
			}
			chunk.WriteString(h)
			if !strings.HasSuffix(h, "\n") {
				chunk.WriteString("\n")
			}
			lines = append(lines, hl...)
		}
		flush()
	}
	return hunks
}

func newPath(file string) string {
	for _, l := range strings.SplitN(file, "\n", 10) {
		if strings.HasPrefix(l, "+++ b/") {
			return strings.TrimPrefix(l, "+++ b/")
		}
		if strings.HasPrefix(l, "+++ /dev/null") {
			return ""
		}
	}
	// header line "a/x b/x" as fallback
	first, _, _ := strings.Cut(file, "\n")
	if i := strings.LastIndex(first, " b/"); i >= 0 {
		return first[i+3:]
	}
	return ""
}

// changedLines returns new-side numbers of '+' lines in one @@ hunk.
func changedLines(h string) []int {
	var out []int
	ls := strings.Split(h, "\n")
	if len(ls) == 0 {
		return nil
	}
	n := newStart(ls[0])
	if n == 0 {
		return nil
	}
	for _, l := range ls[1:] {
		switch {
		case strings.HasPrefix(l, "+"):
			out = append(out, n)
			n++
		case strings.HasPrefix(l, "-"):
		case strings.HasPrefix(l, "\\"):
		default:
			n++
		}
	}
	return out
}

// newStart parses "@@ -a,b +c,d @@" and returns c.
func newStart(header string) int {
	i := strings.Index(header, " +")
	if i < 0 {
		return 0
	}
	rest := header[i+2:]
	end := strings.IndexAny(rest, ", @")
	if end < 0 {
		end = len(rest)
	}
	n, _ := strconv.Atoi(rest[:end])
	return n
}

// Nearest returns the changed line closest to want, or 0 if none.
func (h Hunk) Nearest(want int) int {
	best, dist := 0, 1<<30
	for _, l := range h.ChangedLines {
		d := l - want
		if d < 0 {
			d = -d
		}
		if d < dist {
			best, dist = l, d
		}
	}
	return best
}

// Paths returns unique changed paths, in diff order.
func Paths(hunks []Hunk) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range hunks {
		if !seen[h.Path] {
			seen[h.Path] = true
			out = append(out, h.Path)
		}
	}
	return out
}
