package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestWriteDemoScreens saves each README screen, as the popup draws it, from
// the demo's agents. It only runs for scripts/screenshots.sh, which sets
// SCREENS_DIR and turns the files into PNGs with freeze.
func TestWriteDemoScreens(t *testing.T) {
	dir := os.Getenv("SCREENS_DIR")
	if dir == "" {
		t.Skip("set SCREENS_DIR to write the README screenshots")
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.ANSI256) })
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	save := func(name string, m model) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name+".ansi"), []byte(m.View()+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A Tuesday afternoon, so the times read the same every run.
	stopped := time.Date(2026, 10, 13, 14, 5, 0, 0, time.Local)
	old := timeNow
	timeNow = func() time.Time { return stopped }
	t.Cleanup(func() { timeNow = old })

	// herdr's default theme, which freeze's background matches.
	catppuccin := herdrPalettes["catppuccin"]
	useTheme(&catppuccin)
	t.Cleanup(func() { useTheme(nil) })

	// The list: what's snoozed and until when, then everything awake.
	list := demoScreen(newModel(defaultConfig(), newDemoWorld(), target{}, true, "", nil), 96, 19)
	save("list", press(list, "down"))

	// The durations, on an agent that's in the way.
	// Working, with a pull request open: it can also wait till it's done, or
	// till that's merged.
	pick := demoScreen(newModel(defaultConfig(), newDemoWorld(), target{}, false, "w1:p2", nil), pickerWidth, pickerHeight(defaultConfig()))
	save("picker", press(press(pick, "up"), "up"))

	// Your own: a time or a duration, read as you type.
	custom := press(pick, "c")
	for _, r := range "tomorrow 2pm" {
		custom = press(custom, string(r))
	}
	save("custom", custom)
}

// demoScreen is the popup at a size, its agents loaded.
func demoScreen(m model, w, h int) model {
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = next.(model)
	next, _ = m.Update(m.world.load())
	m = next.(model)
	next, _ = m.Update(linksMsg(m.world.siblings()))
	return next.(model)
}

func press(m model, k string) model {
	next, _ := m.Update(keyMsg(k))
	return next.(model)
}
