package ui

import (
	"image"
	"math"
	"sync"
	"time"

	"fyne.io/fyne/v2"

	"github.com/dixieflatline76/Spice/v2/config"
	"github.com/dixieflatline76/Spice/v2/pkg/sysinfo"
)

// Preferences window sizing. All sizes are Fyne logical units, so the window has the same
// apparent size on every OS and scale factor.

var prefsDefaultWindowSize = fyne.NewSize(960, 680)

const (
	prefsMaxMonitorFraction = 0.9                    // never larger than 90% of the active monitor
	prefsSizeSaveDelay      = 500 * time.Millisecond // debounce for persisting live resizes
)

// validWindowSize reports whether a saved size is usable (finite and positive in both axes).
func validWindowSize(s fyne.Size) bool {
	ok := func(v float32) bool {
		f := float64(v)
		return f > 0 && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return ok(s.Width) && ok(s.Height)
}

// clampPrefsWindowSize limits want to 90% of the monitor (when its size is known) and then raises
// it to at least the content's MinSize, which always wins.
func clampPrefsWindowSize(want, monitor, min fyne.Size) fyne.Size {
	if monitor.Width > 0 && want.Width > monitor.Width*prefsMaxMonitorFraction {
		want.Width = monitor.Width * prefsMaxMonitorFraction
	}
	if monitor.Height > 0 && want.Height > monitor.Height*prefsMaxMonitorFraction {
		want.Height = monitor.Height * prefsMaxMonitorFraction
	}
	if want.Width < min.Width {
		want.Width = min.Width
	}
	if want.Height < min.Height {
		want.Height = min.Height
	}
	return want
}

// initialPrefsWindowSize returns the saved size (or the default when nothing valid is saved),
// clamped to the monitor and the content's MinSize.
func initialPrefsWindowSize(cfg *config.AppConfig, monitor, min fyne.Size) fyne.Size {
	want := prefsDefaultWindowSize
	if cfg != nil {
		w, h := cfg.GetPrefsWindowSize()
		if saved := fyne.NewSize(float32(w), float32(h)); validWindowSize(saved) {
			want = saved
		}
	}
	return clampPrefsWindowSize(want, monitor, min)
}

// monitorLogicalSize converts the active monitor's bounds (native window units) to Fyne logical
// units. With no monitor known it falls back to the primary screen; zero means "unknown".
func monitorLogicalSize(bounds image.Rectangle) fyne.Size {
	scale := sysinfo.GetOSDisplayScale()
	if scale <= 0 {
		scale = 1
	}
	if app := fyne.CurrentApp(); app != nil {
		if s := app.Settings().Scale(); s > 0 {
			scale *= s
		}
	}
	if !bounds.Empty() {
		return fyne.NewSize(float32(bounds.Dx())/scale, float32(bounds.Dy())/scale)
	}
	if w, h, err := sysinfo.GetScreenDimensions(); err == nil && w > 0 && h > 0 {
		return fyne.NewSize(float32(w)/scale, float32(h)/scale)
	}
	return fyne.Size{}
}

// prefsSizeSaver persists the Preferences window size: debounced while the user resizes, and
// immediately when the window closes. Sizes below the content's MinSize are never saved.
type prefsSizeSaver struct {
	cfg     *config.AppConfig
	minSize func() fyne.Size
	delay   time.Duration

	mu     sync.Mutex
	active bool // only after the window is shown (never persist the computed initial size)
	timer  *time.Timer
	last   fyne.Size
}

func newPrefsSizeSaver(cfg *config.AppConfig, minSize func() fyne.Size) *prefsSizeSaver {
	return &prefsSizeSaver{cfg: cfg, minSize: minSize, delay: prefsSizeSaveDelay}
}

// start begins observing resizes; base is the size the window was opened at.
func (s *prefsSizeSaver) start(base fyne.Size) {
	s.mu.Lock()
	s.active = true
	s.last = base
	s.mu.Unlock()
}

// observe records a live resize, saving it after the debounce delay.
func (s *prefsSizeSaver) observe(size fyne.Size) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || size == s.last {
		return
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(s.delay, func() { s.save(size) })
}

// flush saves size now (window closing) and stops observing.
func (s *prefsSizeSaver) flush(size fyne.Size) {
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	wasActive := s.active
	s.active = false
	s.mu.Unlock()
	if wasActive {
		s.save(size)
	}
}

func (s *prefsSizeSaver) save(size fyne.Size) {
	if s.cfg == nil || !validWindowSize(size) {
		return
	}
	if s.minSize != nil {
		if min := s.minSize(); size.Width < min.Width || size.Height < min.Height {
			return
		}
	}
	s.mu.Lock()
	s.last = size
	s.mu.Unlock()
	s.cfg.SetPrefsWindowSize(float64(size.Width), float64(size.Height))
}
