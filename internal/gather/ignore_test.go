package gather

import "testing"

func TestIgnored(t *testing.T) {
	yes := []string{"mix.lock", "deps/foo/bar.ex", "assets/node_modules/x.js", "lib/schema.pb.go", "web/app.min.js", "src/api.generated.ts", "priv/static/app.js.map"}
	no := []string{"lib/weheat/window.ex", ".github/workflows/ci.yml", "README.md", "src/lockbox.go", "config/deps.exs"}
	for _, p := range yes {
		if !Ignored(p) {
			t.Errorf("expected %s ignored", p)
		}
	}
	for _, p := range no {
		if Ignored(p) {
			t.Errorf("expected %s reviewed", p)
		}
	}
}
