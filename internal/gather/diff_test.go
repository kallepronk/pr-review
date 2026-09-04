package gather

import (
	"reflect"
	"strings"
	"testing"
)

const sample = `diff --git a/a.go b/a.go
index 1..2 100644
--- a/a.go
+++ b/a.go
@@ -1,3 +1,4 @@
 package a
+import "fmt"

-func x() {}
+func x() { fmt.Println() }
@@ -10,2 +11,3 @@
 func y() {
+	return
 }
diff --git a/gone.go b/gone.go
deleted file mode 100644
--- a/gone.go
+++ /dev/null
@@ -1 +0,0 @@
-package gone
diff --git a/img.png b/img.png
Binary files a/img.png and b/img.png differ
`

func TestSplitDiff(t *testing.T) {
	hunks := SplitDiff(sample)
	if len(hunks) != 1 {
		t.Fatalf("want 1 hunk (deleted+binary dropped), got %d: %+v", len(hunks), hunks)
	}
	h := hunks[0]
	if h.Path != "a.go" {
		t.Errorf("path = %q", h.Path)
	}
	if want := []int{2, 4, 12}; !reflect.DeepEqual(h.ChangedLines, want) {
		t.Errorf("changed lines = %v, want %v", h.ChangedLines, want)
	}
	if !strings.HasPrefix(h.Patch, "diff --git a/a.go") || strings.Count(h.Patch, "@@") != 4 {
		t.Errorf("patch lost header or hunks:\n%s", h.Patch)
	}
	if h.Nearest(3) != 2 && h.Nearest(3) != 4 {
		t.Errorf("nearest(3) = %d", h.Nearest(3))
	}
}

func TestSplitDiffChunks(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n")
	for i := 0; i < 4; i++ {
		b.WriteString("@@ -1,1 +1,1 @@\n")
		for j := 0; j < 100; j++ {
			b.WriteString("+x\n")
		}
	}
	hunks := SplitDiff(b.String())
	if len(hunks) < 2 {
		t.Fatalf("expected chunking above %d lines, got %d hunk(s)", MaxHunkLines, len(hunks))
	}
	for _, h := range hunks {
		if !strings.HasPrefix(h.Patch, "diff --git a/big.go") {
			t.Errorf("chunk lost file header")
		}
	}
}
