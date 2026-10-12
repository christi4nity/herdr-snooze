package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// captureStdout is what f printed: the plugin log.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { data, _ := io.ReadAll(r); done <- string(data) }()
	defer func() { os.Stdout = old }()
	f()
	w.Close()
	return <-done
}

func writeConfig(t *testing.T, text string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWrongTypesFallBackToDefaultsWithANote(t *testing.T) {
	// v0.2: "badge": 7 crashed every hook; a list in "views" too.
	writeConfig(t, `{"badge": 7, "views": ["x"], "sort": {"a": 1}, "notify": "yes", "durations": [], "auto_return": false}`)
	var cfg config
	out := captureStdout(t, func() { cfg = loadConfig() })
	if cfg.Badge != defaultBadge || cfg.Views != nil || cfg.Sort != nil || cfg.Notify ||
		!reflect.DeepEqual(cfg.Durations, defaultDurations) {
		t.Fatalf("%+v", cfg)
	}
	if cfg.AutoReturn {
		t.Fatal("the valid one wasn't kept")
	}
	if n := strings.Count(out, "\n"); n < 5 {
		t.Fatalf("%d notes:\n%s", n, out)
	}
}

func TestSortKeysMustBeOnesHerdrAccepts(t *testing.T) {
	// Any dict passed, and herdr then refused every agent.view.set.
	good := []any{map[string]any{"field": "attention"}, map[string]any{"field": map[string]any{"token": "rank"}, "order": "desc"}}
	for _, bad := range []string{`[{}]`, `[{"field": "nope"}]`, `[{"field": "seen", "order": "up"}]`, `[{"field": {"token": 1}}]`,
		`[{"field": {"token": "a", "extra": 1}}]`, `[{"field": ["seen"]}]`, `["seen"]`} {
		writeConfig(t, `{"sort": `+bad+`, "views": {"plugin:x": {"l": `+bad+`}}}`)
		var cfg config
		captureStdout(t, func() { cfg = loadConfig() })
		if cfg.Sort != nil || cfg.Views != nil {
			t.Fatalf("%s accepted: %+v", bad, cfg)
		}
	}
	data, _ := json.Marshal(map[string]any{"sort": good, "views": map[string]any{"plugin:x": map[string]any{"l": good}}})
	writeConfig(t, string(data))
	sameJSON(t, loadConfig().Sort, good, "good sort")
	// and our own pass our own check
	for source, k := range knownViews {
		for label, sort := range k.sorts {
			if _, ok := sortOK(norm(sort)); !ok {
				t.Fatal(source, label)
			}
		}
	}
	for m, pm := range panelModes {
		if _, ok := sortOK(norm(pm.sort)); !ok {
			t.Fatal(m)
		}
	}
}

func TestMalformedJSONSaysSo(t *testing.T) {
	writeConfig(t, "{nope")
	var cfg config
	out := captureStdout(t, func() { cfg = loadConfig() })
	if cfg.Badge != defaultBadge || out == "" {
		t.Fatalf("settings silently ignored: %+v %q", cfg, out)
	}
}

func TestValidSettingsAreKept(t *testing.T) {
	writeConfig(t, `{"badge": "zz", "durations": ["5m", 30], "views": {"src": {"label": [{"field": "status", "order": "asc"}]}}, "theme": "light"}`)
	cfg := loadConfig()
	if cfg.Badge != "zz" || !reflect.DeepEqual(cfg.Durations, []string{"5m", "30"}) || cfg.Views["src"] == nil || cfg.Theme != "light" {
		t.Fatalf("%+v", cfg)
	}
}

func TestNoConfigIsTheDefaults(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	if cfg := loadConfig(); !reflect.DeepEqual(cfg, defaultConfig()) {
		t.Fatalf("%+v", cfg)
	}
}
