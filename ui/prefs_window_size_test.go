package ui

import (
	"math"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"github.com/stretchr/testify/assert"

	"github.com/dixieflatline76/Spice/v2/config"
)

func newSizeTestConfig(t *testing.T) *config.AppConfig {
	t.Helper()
	app := test.NewApp()
	t.Cleanup(app.Quit)
	return config.NewAppConfig(app.Preferences())
}

var (
	bigMonitor = fyne.NewSize(2560, 1440)
	smallMin   = fyne.NewSize(400, 100)
)

func TestPrefsWindowSize_DefaultWhenNothingSaved(t *testing.T) {
	cfg := newSizeTestConfig(t)
	assert.Equal(t, fyne.NewSize(960, 680), initialPrefsWindowSize(cfg, bigMonitor, smallMin))
	assert.Equal(t, fyne.NewSize(960, 680), initialPrefsWindowSize(cfg, fyne.Size{}, smallMin), "unknown monitor: no clamp")
}

func TestPrefsWindowSize_SavedSizeRestored(t *testing.T) {
	cfg := newSizeTestConfig(t)
	cfg.SetPrefsWindowSize(1100, 760)
	assert.Equal(t, fyne.NewSize(1100, 760), initialPrefsWindowSize(cfg, bigMonitor, smallMin))
}

func TestPrefsWindowSize_ClampedToNinetyPercentOfMonitor(t *testing.T) {
	cfg := newSizeTestConfig(t)
	monitor := fyne.NewSize(1000, 600)
	assert.Equal(t, fyne.NewSize(900, 540), initialPrefsWindowSize(cfg, monitor, smallMin), "default clamped")

	cfg.SetPrefsWindowSize(3000, 2000) // saved on a larger display
	assert.Equal(t, fyne.NewSize(900, 540), initialPrefsWindowSize(cfg, monitor, smallMin), "saved size clamped")
}

func TestPrefsWindowSize_NeverBelowMinSize(t *testing.T) {
	cfg := newSizeTestConfig(t)
	min := fyne.NewSize(1000, 720)
	assert.Equal(t, min, initialPrefsWindowSize(cfg, fyne.NewSize(800, 600), min), "MinSize beats the monitor clamp")

	cfg.SetPrefsWindowSize(500, 300)
	assert.Equal(t, min, initialPrefsWindowSize(cfg, bigMonitor, min))
}

func TestPrefsWindowSize_InvalidSavedFallsBackToDefault(t *testing.T) {
	cfg := newSizeTestConfig(t)
	for _, bad := range [][2]float64{{0, 0}, {960, 0}, {0, 700}, {-5, 700}, {math.NaN(), 700}, {math.Inf(1), 700}} {
		cfg.SetPrefsWindowSize(bad[0], bad[1])
		assert.Equal(t, fyne.NewSize(960, 680), initialPrefsWindowSize(cfg, bigMonitor, smallMin), "saved %v", bad)
	}
}

func TestPrefsSizeSaver_DebouncesAndRespectsMinSize(t *testing.T) {
	cfg := newSizeTestConfig(t)
	s := newPrefsSizeSaver(cfg, func() fyne.Size { return fyne.NewSize(600, 400) })
	s.delay = 20 * time.Millisecond

	s.observe(fyne.NewSize(1200, 800))
	time.Sleep(60 * time.Millisecond)
	w, h := cfg.GetPrefsWindowSize()
	assert.Zero(t, w+h, "nothing is saved before the window is shown")

	s.start(fyne.NewSize(960, 680))
	s.observe(fyne.NewSize(1000, 700))
	s.observe(fyne.NewSize(1050, 720)) // supersedes the first within the debounce window
	w, h = cfg.GetPrefsWindowSize()
	assert.Zero(t, w+h, "debounced: not saved immediately")
	assert.Eventually(t, func() bool {
		w, h := cfg.GetPrefsWindowSize()
		return w == 1050 && h == 720
	}, time.Second, 10*time.Millisecond)

	s.observe(fyne.NewSize(500, 300)) // below MinSize: ignored
	time.Sleep(60 * time.Millisecond)
	w, h = cfg.GetPrefsWindowSize()
	assert.Equal(t, [2]float64{1050, 720}, [2]float64{w, h})

	s.flush(fyne.NewSize(1111, 777)) // close saves immediately
	w, h = cfg.GetPrefsWindowSize()
	assert.Equal(t, [2]float64{1111, 777}, [2]float64{w, h})

	s.observe(fyne.NewSize(1300, 900)) // after close nothing more is saved
	time.Sleep(60 * time.Millisecond)
	w, h = cfg.GetPrefsWindowSize()
	assert.Equal(t, [2]float64{1111, 777}, [2]float64{w, h})
}
