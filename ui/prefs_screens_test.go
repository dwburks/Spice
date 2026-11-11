package ui

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"github.com/dixieflatline76/Spice/v2/asset"
	"github.com/dixieflatline76/Spice/v2/config"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
	"github.com/dixieflatline76/Spice/v2/pkg/wallpaper"

	_ "github.com/dixieflatline76/Spice/v2/pkg/wallpaper/providers/artic"
	_ "github.com/dixieflatline76/Spice/v2/pkg/wallpaper/providers/cleveland"
	_ "github.com/dixieflatline76/Spice/v2/pkg/wallpaper/providers/favorites"
	_ "github.com/dixieflatline76/Spice/v2/pkg/wallpaper/providers/getty"
	_ "github.com/dixieflatline76/Spice/v2/pkg/wallpaper/providers/googlephotos"
	_ "github.com/dixieflatline76/Spice/v2/pkg/wallpaper/providers/localfolder"
	_ "github.com/dixieflatline76/Spice/v2/pkg/wallpaper/providers/metmuseum"
	_ "github.com/dixieflatline76/Spice/v2/pkg/wallpaper/providers/npm"
	_ "github.com/dixieflatline76/Spice/v2/pkg/wallpaper/providers/pexels"
	_ "github.com/dixieflatline76/Spice/v2/pkg/wallpaper/providers/rijksmuseum"
	_ "github.com/dixieflatline76/Spice/v2/pkg/wallpaper/providers/smk"
	_ "github.com/dixieflatline76/Spice/v2/pkg/wallpaper/providers/wallhaven"
	_ "github.com/dixieflatline76/Spice/v2/pkg/wallpaper/providers/wikimedia"
)

// TestRenderPrefsScreenshots renders the real preferences window headlessly (Fyne test driver)
// and writes PNGs for design review. It is opt-in: set SPICE_SCREENSHOTS to an output directory.
func TestRenderPrefsScreenshots(t *testing.T) {
	outDir := os.Getenv("SPICE_SCREENSHOTS")
	if outDir == "" {
		t.Skip("set SPICE_SCREENSHOTS=<dir> to render preferences screenshots")
	}
	require.NoError(t, os.MkdirAll(outDir, 0o755))
	keyring.MockInit()

	a := test.NewApp()
	defer a.Quit()

	sa := &SpiceApp{App: a, assetMgr: asset.NewManager(), appConfig: config.NewAppConfig(a.Preferences()), os: &noopOS{}}
	sa.Register(wallpaper.GetInstance())

	cfg := wallpaper.GetConfig(a.Preferences())
	_, _ = cfg.AddImageQuery("Mountain landscapes", "https://wallhaven.cc/api/v1/search?q=mountains&categories=100", true)
	_, _ = cfg.AddImageQuery("Neon city at night", "https://wallhaven.cc/api/v1/search?q=neon+city", true)
	_, _ = cfg.AddImageQuery("Minimal abstract", "https://wallhaven.cc/api/v1/search?q=minimal", false)

	placeholder := filepath.Join(t.TempDir(), "banner.png")
	writePlaceholderWallpaper(t, placeholder)

	render := func(name string, variant fyne.ThemeVariant, page string, prep func(v *prefsNavView, sm *SettingsManager)) {
		a.Settings().SetTheme(newForcedVariantTheme(variant))
		w := a.NewWindow("Spice Preferences")
		defer w.Close()
		sa.prefsWindow = w
		sm := NewSettingsManager(w).(*SettingsManager)
		sm.runAsync = func(f func()) { f() } // live items compute synchronously in the headless render
		nav, _ := sa.buildPreferencesNav(sm, page)
		stubSmartFitLiveItems(nav)
		if item, _ := nav.FindItem(schema.NavPageWallpaper); item != nil {
			item.Banner = &schema.BannerData{ImagePath: placeholder, ProviderName: "wallhaven"}
		}
		v := newPrefsNavView(sm, nav, page)
		w.SetContent(v.Root())
		w.Resize(fyne.NewSize(1000, 700))
		if prep != nil {
			prep(v, sm)
		}
		sm.checkAndEnableApply()
		img := w.Canvas().Capture()
		f, err := os.Create(filepath.Join(outDir, name+".png"))
		require.NoError(t, err)
		defer f.Close()
		require.NoError(t, png.Encode(f, img))
	}

	render("wallpaper_light", theme.VariantLight, schema.NavPageWallpaper, nil)
	render("wallpaper_dark", theme.VariantDark, schema.NavPageWallpaper, nil)
	render("wallhaven_light", theme.VariantLight, "Wallhaven", nil)
	render("museums_list_light", theme.VariantLight, schema.NavPageMuseums, nil)
	render("museum_met_light", theme.VariantLight, "MetMuseum", nil)
	render("save_pill_light", theme.VariantLight, schema.NavPageWallpaper, func(v *prefsNavView, sm *SettingsManager) {
		sm.allWidgets["staggerChanges"].(*Switch).Toggle()
		sm.allWidgets["nightlyRefresh"].(*Switch).Toggle()
		sm.allWidgets["faceCrop"].(*Switch).Toggle()
	})
	render("frequency_custom_light", theme.VariantLight, schema.NavPageWallpaper, func(v *prefsNavView, sm *SettingsManager) {
		sm.allWidgets["changeFrequency__preset"].(*widget.Select).SetSelected("Custom…")
	})
	render("smartfit_light", theme.VariantLight, schema.NavPageSmartFit, nil)
	render("smartfit_dark", theme.VariantDark, schema.NavPageSmartFit, nil)
	render("smartfit_staged_light", theme.VariantLight, schema.NavPageSmartFit, func(v *prefsNavView, sm *SettingsManager) {
		sm.valueSetters["smartFitMode"](1) // as if the Quality card was clicked (the default is Flexibility)
	})
	render("smartfit_full_light", theme.VariantLight, schema.NavPageSmartFit, func(v *prefsNavView, sm *SettingsManager) {
		sm.prefsWindow.Resize(fyne.NewSize(1000, 1500)) // the whole page, no scrolling
	})
	render("smartfit_full_dark", theme.VariantDark, schema.NavPageSmartFit, func(v *prefsNavView, sm *SettingsManager) {
		sm.prefsWindow.Resize(fyne.NewSize(1000, 1500))
	})
	render("smartfit_960x680_light", theme.VariantLight, schema.NavPageSmartFit, func(v *prefsNavView, sm *SettingsManager) {
		sm.prefsWindow.Resize(fyne.NewSize(960, 680)) // the default window size
	})
	render("app_light", theme.VariantLight, schema.NavPageApp, nil)

	oldDebounce := searchDebounce
	searchDebounce = 0 // results appear as soon as the query is typed
	defer func() { searchDebounce = oldDebounce }()
	typeSearch := func(q string) func(v *prefsNavView, sm *SettingsManager) {
		return func(v *prefsNavView, sm *SettingsManager) {
			sm.prefsWindow.Resize(fyne.NewSize(960, 680)) // the default window size
			v.search.entry.SetText(q)
		}
	}
	render("search_light", theme.VariantLight, schema.NavPageWallpaper, typeSearch("cache"))
	render("search_dark", theme.VariantDark, schema.NavPageWallpaper, typeSearch("cache"))
	render("search_empty_light", theme.VariantLight, schema.NavPageWallpaper, typeSearch("zzzz"))
	render("pause_dark", theme.VariantDark, schema.NavPagePause, nil)
}

type noopOS struct{}

func (noopOS) TransformToForeground()             {}
func (noopOS) TransformToBackground()             {}
func (noopOS) SetupLifecycle(fyne.App, *SpiceApp) {}

// writePlaceholderWallpaper draws a warm dusk landscape gradient as a stand-in wallpaper.
func writePlaceholderWallpaper(t *testing.T, path string) {
	t.Helper()
	const w, h = 1600, 600
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		fy := float64(y) / h
		for x := 0; x < w; x++ {
			fx := float64(x) / w
			r := 40 + 190*fy + 20*math.Sin(fx*6)
			g := 50 + 90*fy*fy
			b := 110 - 60*fy
			ridge := 0.62 + 0.08*math.Sin(fx*9+1) + 0.05*math.Sin(fx*23)
			if fy > ridge {
				r, g, b = r*0.35, g*0.35, b*0.45
			}
			img.SetNRGBA(x, y, color.NRGBA{R: clamp8(r), G: clamp8(g), B: clamp8(b), A: 0xFF})
		}
	}
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, png.Encode(f, img))
}

func clamp8(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// stubSmartFitLiveItems swaps the Smart fit page's computed items for fixtures so the screenshot
// shows realistic content (the headless app has no displays or library). The real computations
// are covered by tests in pkg/wallpaper.
func stubSmartFitLiveItems(nav *schema.NavSchema) {
	item, _ := nav.FindItem(schema.NavPageSmartFit)
	if item == nil || item.Panel == nil {
		return
	}
	for si := range item.Panel.Sections {
		items := item.Panel.Sections[si].Items
		for ii, it := range items {
			switch v := it.(type) {
			case schema.StatsItem:
				v.Compute = func(_ context.Context, get func(string) interface{}) []schema.StatRow {
					mode, _ := get("smartFitMode").(int)
					shape, small := 40, 18
					if mode == 0 {
						shape = 0
					}
					return []schema.StatRow{
						{Label: "Display 1 · 3440×1440", Value: fmt.Sprintf("%d fit · %d wrong shape · %d too small", 142+40-shape, shape, small)},
						{Label: "Display 2 · 3440×1440", Value: fmt.Sprintf("%d fit · %d wrong shape · %d too small", 142+40-shape, shape, small)},
						{Label: "12 images not measured yet", Muted: true},
					}
				}
				items[ii] = v
			case schema.PreviewStripItem:
				v.Compute = func(_ context.Context, get func(string) interface{}) []schema.PreviewThumb {
					orig := fixturePhoto(440, 293)
					cur := fixturePhoto(440, 184)
					return []schema.PreviewThumb{
						{Caption: "Original", Sub: "Display 1 · 3440×1440", Image: orig},
						{Caption: "Current", Sub: "Quality", Image: cur},
						{Caption: "With your changes", Sub: "Flexibility", Image: fixturePhoto(440, 184)},
					}
				}
				items[ii] = v
			}
		}
	}
}

func fixturePhoto(w, h int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		fy := float64(y) / float64(h)
		for x := 0; x < w; x++ {
			fx := float64(x) / float64(w)
			r := 40 + 190*fy + 20*math.Sin(fx*6)
			g := 50 + 90*fy*fy
			b := 110 - 60*fy
			img.SetNRGBA(x, y, color.NRGBA{R: clamp8(r), G: clamp8(g), B: clamp8(b), A: 0xFF})
		}
	}
	return img
}
