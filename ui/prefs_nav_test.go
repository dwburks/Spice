package ui

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
)

// --- Switch widget -----------------------------------------------------------------------------

func TestSwitch_TapTogglesAndSetCheckedIsSilent(t *testing.T) {
	test.NewApp()
	var got []bool
	sw := NewSwitch(false, func(b bool) { got = append(got, b) })

	test.Tap(sw)
	assert.True(t, sw.Checked)
	test.Tap(sw)
	assert.False(t, sw.Checked)
	assert.Equal(t, []bool{true, false}, got, "taps fire OnChanged")

	sw.SetChecked(true)
	assert.True(t, sw.Checked)
	assert.Len(t, got, 2, "SetChecked is programmatic and never fires OnChanged")

	sw.Disable()
	test.Tap(sw)
	assert.True(t, sw.Checked, "a disabled switch ignores taps")

	sw.Enable()
	sw.TypedRune(' ')
	assert.False(t, sw.Checked, "space toggles a focused switch")
}

func TestSwitchRow_TapStagesAndSetValueDoesNotMarkDirty(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	sm := NewSettingsManager(app.NewWindow("Switch")).(*SettingsManager)

	applied := 0
	hookCalls := 0
	sm.RenderSchema(schema.PanelSchema{Sections: []schema.SectionSchema{{Items: []schema.ItemSchema{
		schema.BoolItem{
			Name:         "flag",
			Label:        "Flag:",
			InitialValue: false,
			OnChanged:    func(bool) { hookCalls++ },
			ApplyFunc:    func(bool) { applied++ },
		},
	}}}})
	sw := sm.allWidgets["flag"].(*Switch)

	// Programmatic sync to the baseline value never marks the setting dirty or calls the hook.
	sm.SetValue("flag", false)
	assert.False(t, sm.HasPendingChange("flag"))
	assert.False(t, sm.IsDirty())
	assert.Equal(t, 0, hookCalls)

	// A user tap stages the change (no instant save).
	test.Tap(sw)
	assert.True(t, sm.HasPendingChange("flag"))
	assert.Equal(t, 1, hookCalls)
	assert.Equal(t, 0, applied, "staged, not saved")

	// SetValue back to the baseline (what Revert does) clears it again, still without the hook.
	sm.SetValue("flag", false)
	assert.False(t, sw.Checked)
	assert.False(t, sm.HasPendingChange("flag"))
	assert.Equal(t, 1, hookCalls)
	assert.Equal(t, 0, applied)
}

// --- Navigation --------------------------------------------------------------------------------

type navFixture struct {
	app     fyne.App
	win     fyne.Window
	sm      *SettingsManager
	view    *prefsNavView
	applied map[string]interface{}
}

func newNavFixture(t *testing.T, initial string) *navFixture {
	t.Helper()
	app := test.NewApp()
	t.Cleanup(app.Quit)
	w := app.NewWindow("Nav")
	f := &navFixture{app: app, win: w, applied: map[string]interface{}{}}
	f.sm = NewSettingsManager(w).(*SettingsManager)
	apply := func(name string) func(bool) {
		return func(b bool) { f.applied[name] = b }
	}

	museum := func(id, title string) schema.NavItem {
		return schema.NavItem{
			ID: id, Title: title, Description: title + " collections",
			Badge: func() string { return "1 of 3 on" },
			Panel: &schema.PanelSchema{Sections: []schema.SectionSchema{{Items: []schema.ItemSchema{
				schema.BoolItem{Name: id + "_framed", Label: "Display as framed gallery", ApplyFunc: apply(id + "_framed")},
			}}}},
		}
	}
	nav := &schema.NavSchema{Groups: []schema.NavGroup{
		{ID: schema.NavGroupGeneral, Title: "General", Items: []schema.NavItem{
			{ID: "alpha", Title: "Alpha", IconName: "settings", Panel: &schema.PanelSchema{Sections: []schema.SectionSchema{{
				Title: "Group A",
				Items: []schema.ItemSchema{
					schema.BoolItem{Name: "a1", Label: "A one", ApplyFunc: apply("a1")},
					schema.TextItem{Name: "a2", Label: "A two", InitialValue: "base", ApplyFunc: func(s string) { f.applied["a2"] = s }},
				},
			}}}},
			{ID: "beta", Title: "Beta", Panel: &schema.PanelSchema{Sections: []schema.SectionSchema{{Items: []schema.ItemSchema{
				schema.BoolItem{Name: "b1", Label: "B one", InitialValue: true, ApplyFunc: apply("b1")},
			}}}}},
		}},
		{ID: schema.NavGroupSources, Title: "Sources", Items: []schema.NavItem{
			{ID: schema.NavPageMuseums, Title: "Museums", Children: []schema.NavItem{museum("met", "The Met"), museum("aic", "AIC")}},
		}},
	}}
	f.view = newNavFixtureView(f.sm, nav, initial)
	w.SetContent(f.view.Root())
	w.Resize(fyne.NewSize(1000, 700))
	return f
}

func newNavFixtureView(sm *SettingsManager, nav *schema.NavSchema, initial string) *prefsNavView {
	v := newPrefsNavView(sm, nav, initial)
	sm.checkAndEnableApply()
	return v
}

func (f *navFixture) toggle(name string) {
	test.Tap(f.sm.allWidgets[name].(*Switch))
}

func TestPrefsNav_SelectsInitialPageAndFallsBack(t *testing.T) {
	f := newNavFixture(t, "beta")
	assert.Equal(t, "beta", f.view.CurrentPage())
	assert.True(t, f.view.sidebar["beta"].selected)
	assert.False(t, f.view.sidebar["alpha"].selected)

	g := newNavFixture(t, "does-not-exist")
	assert.Equal(t, "alpha", g.view.CurrentPage(), "unknown pages fall back to the first page")
}

func TestPrefsNav_MuseumDrillIn(t *testing.T) {
	f := newNavFixture(t, schema.NavPageMuseums)

	// The Museums page is a list of chevron rows, one per museum.
	var rows []*tapRow
	walkTree(f.view.pages[schema.NavPageMuseums], func(o fyne.CanvasObject) {
		if r, ok := o.(*tapRow); ok {
			rows = append(rows, r)
		}
	})
	require.Len(t, rows, 2)
	var labels []string
	walkTree(rows[0], func(o fyne.CanvasObject) {
		if l, ok := o.(*widget.Label); ok {
			labels = append(labels, l.Text)
		}
	})
	assert.Contains(t, labels, "The Met")
	assert.Contains(t, labels, "1 of 3 on")

	// Tapping a museum drills into its page; the sidebar keeps Museums selected.
	test.Tap(rows[0])
	assert.Equal(t, "met", f.view.CurrentPage())
	assert.True(t, f.view.sidebar[schema.NavPageMuseums].selected)

	// The museum page has a back link to Museums.
	back := findButtonByText(f.view.pages["met"], "Museums")
	require.NotNil(t, back)
	test.Tap(back)
	assert.Equal(t, schema.NavPageMuseums, f.view.CurrentPage())

	// A change on a museum page marks the Museums sidebar item and names Museums in the pill.
	f.toggle("aic_framed")
	assert.True(t, f.view.sidebar[schema.NavPageMuseums].dirty)
	assert.Contains(t, f.view.pill.label.Text, "Museums")
}

func TestSavePill_AppearsWhenDirtyAndRevertRestoresBaseline(t *testing.T) {
	f := newNavFixture(t, "alpha")
	assert.False(t, f.view.pill.Visible(), "hidden while nothing is staged")

	f.toggle("a1")
	f.toggle("b1")
	f.sm.allWidgets["a2"].(*widget.Entry).SetText("edited")

	assert.True(t, f.view.pill.Visible())
	assert.Equal(t, "3 unsaved changes on Alpha and Beta", f.view.pill.label.Text)
	assert.True(t, f.view.sidebar["alpha"].dirty)
	assert.True(t, f.view.sidebar["beta"].dirty)
	for _, d := range f.sm.rowDots["a1"] {
		assert.True(t, d.Visible(), "changed rows show their dot")
	}

	test.Tap(f.view.pill.revert)

	assert.False(t, f.view.pill.Visible())
	assert.False(t, f.sm.IsDirty())
	assert.False(t, f.sm.allWidgets["a1"].(*Switch).Checked)
	assert.True(t, f.sm.allWidgets["b1"].(*Switch).Checked)
	assert.Equal(t, "base", f.sm.allWidgets["a2"].(*widget.Entry).Text)
	assert.False(t, f.view.sidebar["alpha"].dirty)
	for _, d := range f.sm.rowDots["a1"] {
		assert.False(t, d.Visible())
	}
	assert.Empty(t, f.applied, "Revert never writes to Config")
}

func TestSavePill_SaveRunsApplyPath(t *testing.T) {
	f := newNavFixture(t, "alpha")
	f.toggle("a1")
	assert.Equal(t, "1 unsaved change on Alpha", f.view.pill.label.Text)
	assert.Same(t, f.sm.GetApplySettingsButton(), f.view.pill.save, "Save changes is the existing apply button")

	test.Tap(f.view.pill.save)

	assert.Equal(t, map[string]interface{}{"a1": true}, f.applied)
	assert.False(t, f.sm.IsDirty())
	assert.False(t, f.view.pill.Visible())
	assert.Equal(t, true, f.sm.GetBaseline("a1"), "baseline moves to the saved value")
}

func TestPendingSummary(t *testing.T) {
	assert.Equal(t, "", pendingSummary(0, nil))
	assert.Equal(t, "1 unsaved change", pendingSummary(1, nil))
	assert.Equal(t, "2 unsaved changes", pendingSummary(2, nil))
	assert.Equal(t, "2 unsaved changes on Alpha", pendingSummary(2, []string{"Alpha"}))
	assert.Equal(t, "4 unsaved changes on A and B +2", pendingSummary(4, []string{"A", "B", "C", "D"}))
}

func TestConfirmDiscardOnClose(t *testing.T) {
	f := newNavFixture(t, "alpha")
	closed := false
	f.win.SetOnClosed(func() { closed = true })

	// Dirty: closing asks first and keeps the window open.
	f.toggle("a1")
	confirmDiscardOnClose(f.win, f.sm)
	require.NotNil(t, f.win.Canvas().Overlays().Top(), "discard prompt shown")
	assert.False(t, closed)

	discard := findButtonByText(f.win.Canvas().Overlays().Top(), "Discard")
	require.NotNil(t, discard)
	test.Tap(discard)
	assert.True(t, closed, "Discard closes the window")
	assert.Empty(t, f.applied, "discarding never saves")
}

func TestConfirmDiscardOnClose_CleanClosesImmediately(t *testing.T) {
	f := newNavFixture(t, "alpha")
	closed := false
	f.win.SetOnClosed(func() { closed = true })
	confirmDiscardOnClose(f.win, f.sm)
	assert.True(t, closed)
}

// --- Headers -----------------------------------------------------------------------------------

func TestPageHeader_BannerUsesPortDataAndFallsBack(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	sm := NewSettingsManager(app.NewWindow("Banner")).(*SettingsManager)

	path := filepath.Join(t.TempDir(), "now.png")
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	img.SetNRGBA(1, 1, color.NRGBA{R: 200, A: 255})
	fh, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, png.Encode(fh, img))
	require.NoError(t, fh.Close())

	nav := &schema.NavSchema{Groups: []schema.NavGroup{{ID: schema.NavGroupGeneral, Items: []schema.NavItem{
		{ID: "wp", Title: "Wallpaper", Header: schema.HeaderBanner, Banner: &schema.BannerData{ImagePath: path, ProviderName: "wallhaven"}},
		{ID: "plain", Title: "Plain", Header: schema.HeaderBanner}, // no snapshot -> icon header
	}}}}
	v := newPrefsNavView(sm, nav, "wp")

	var files []string
	var texts []string
	walkTree(v.pages["wp"], func(o fyne.CanvasObject) {
		if ci, ok := o.(*canvas.Image); ok {
			files = append(files, ci.File)
		}
		if ct, ok := o.(*canvas.Text); ok {
			texts = append(texts, ct.Text)
		}
	})
	assert.Equal(t, []string{path}, files, "banner shows the snapshot's image")
	assert.Contains(t, texts, "Now showing · wallhaven")

	files = nil
	var titles []string
	walkTree(v.pages["plain"], func(o fyne.CanvasObject) {
		if ci, ok := o.(*canvas.Image); ok && ci.File != "" {
			files = append(files, ci.File)
		}
		if l, ok := o.(*widget.Label); ok {
			titles = append(titles, l.Text)
		}
	})
	assert.Empty(t, files, "no banner without port data")
	assert.Contains(t, titles, "Plain", "falls back to the icon header")
}

func TestNavItem_SelectionStylePerOS(t *testing.T) {
	test.NewApp()
	defer func(old bool) { navStyleWindows = old }(navStyleWindows)

	for _, windows := range []bool{false, true} {
		navStyleWindows = windows
		item := newNavItemWidget("Wallpaper", nil, nil, nil)
		item.setSelected(true)
		r := test.WidgetRenderer(item).(*navItemRenderer)
		if windows {
			assert.NotEqual(t, color.Transparent, r.bar.FillColor, "Windows: 3px accent bar on the left edge")
			assert.Equal(t, themeColor(colorNameWinSelection), r.bg.FillColor, "Windows: neutral selection fill")
		} else {
			assert.Equal(t, color.Transparent, r.bar.FillColor, "macOS/Linux: no bar")
			assert.Equal(t, themeColor(theme.ColorNamePrimary), r.bg.FillColor, "macOS/Linux: filled accent")
		}

		item.setDirty(true)
		assert.True(t, r.dot.Visible(), "pages with unsaved changes show a dot")
	}
}
