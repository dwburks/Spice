package ui

import (
	"fmt"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"github.com/dixieflatline76/Spice/v2/asset"
	"github.com/dixieflatline76/Spice/v2/config"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
	"github.com/dixieflatline76/Spice/v2/pkg/wallpaper"
)

type searchFixture struct {
	app  fyne.App
	win  fyne.Window
	sm   *SettingsManager
	view *prefsNavView
}

// newSearchFixture builds a small preferences window with a tall page (to test scrolling to a
// result), a choice-cards page and a drill-in museum page. Search runs synchronously.
func newSearchFixture(t *testing.T, initial string) *searchFixture {
	t.Helper()
	oldDebounce, oldReveal := searchDebounce, searchRevealDelay
	searchDebounce, searchRevealDelay = 0, 0
	t.Cleanup(func() { searchDebounce, searchRevealDelay = oldDebounce, oldReveal })

	app := test.NewApp()
	t.Cleanup(app.Quit)
	w := app.NewWindow("Search")
	f := &searchFixture{app: app, win: w}
	f.sm = NewSettingsManager(w).(*SettingsManager)

	long := make([]schema.ItemSchema, 0, 40)
	for i := 0; i < 40; i++ {
		long = append(long, schema.BoolItem{Name: fmt.Sprintf("row%d", i), Label: fmt.Sprintf("Option number %d", i), Help: "Filler."})
	}
	nav := &schema.NavSchema{Groups: []schema.NavGroup{
		{ID: schema.NavGroupGeneral, Title: "General", Items: []schema.NavItem{
			{ID: "alpha", Title: "Alpha", Description: "The first page", IconName: "settings", Panel: &schema.PanelSchema{Sections: []schema.SectionSchema{
				{Title: "Storage", Items: []schema.ItemSchema{
					schema.SliderItem{Name: "cacheSize", Label: "Cache size", Min: 0, Max: 10, Step: 1},
					schema.BoolItem{Name: "prefetch", Label: "Prefetch", Help: "Keeps the cache warm."},
					schema.BoolItem{Name: "hiddenOpt", Label: "Hidden option", VisibleIf: func() bool { return false }},
				}},
				{Title: "Long list", Items: long},
			}}},
			{ID: "smartfit", Title: "Smart fit", Panel: &schema.PanelSchema{Sections: []schema.SectionSchema{{Title: "Fit mode", Items: []schema.ItemSchema{
				schema.ChoiceCardsItem{Name: "fitMode", Cards: []schema.ChoiceCard{
					{Title: "Quality", Description: "Crops a little."},
					{Title: "Flexibility", Description: "Crops more."},
				}},
			}}}}},
			{ID: "beta", Title: "Beta", Panel: &schema.PanelSchema{Sections: []schema.SectionSchema{{Items: []schema.ItemSchema{
				schema.BoolItem{Name: "b1", Label: "B one"},
			}}}}},
		}},
		{ID: schema.NavGroupSources, Title: "Sources", Items: []schema.NavItem{
			{ID: schema.NavPageMuseums, Title: "Museums", Children: []schema.NavItem{
				{ID: "met", Title: "The Met", Panel: &schema.PanelSchema{Sections: []schema.SectionSchema{{Title: "Gallery", Items: []schema.ItemSchema{
					schema.BoolItem{Name: "met_framed", Label: "Display as framed gallery"},
				}}}}},
			}},
		}},
	}}
	f.view = newNavFixtureView(f.sm, nav, initial)
	w.SetContent(f.view.Root())
	w.Resize(fyne.NewSize(1000, 700))
	return f
}

func (f *searchFixture) typeQuery(q string) { f.view.search.entry.SetText(q) }

// resultRows returns the tappable result rows of the page now in the content area.
func (f *searchFixture) resultRows() []*tapRow {
	var rows []*tapRow
	walkTree(f.view.content, func(o fyne.CanvasObject) {
		if r, ok := o.(*tapRow); ok {
			rows = append(rows, r)
		}
	})
	return rows
}

func rowTexts(root fyne.CanvasObject) []string {
	var out []string
	walkTree(root, func(o fyne.CanvasObject) {
		switch w := o.(type) {
		case *widget.Label:
			out = append(out, w.Text)
		case *widget.RichText:
			out = append(out, w.String())
		}
	})
	return out
}

func (f *searchFixture) contentTexts() []string { return rowTexts(f.view.content) }

func TestSearch_TypingShowsRankedResultsPage(t *testing.T) {
	f := newSearchFixture(t, "beta")
	f.typeQuery("cache")

	require.True(t, f.view.search.showing)
	rows := f.resultRows()
	require.Len(t, rows, 2)
	first := rowTexts(rows[0])
	assert.Equal(t, "Cache size", first[0], "the label match ranks first")
	assert.Contains(t, first, "Alpha › Storage", "muted trail: page › section")
	assert.Equal(t, "Prefetch", rowTexts(rows[1])[0], "a help-text match ranks after it")

	texts := f.contentTexts()
	assert.Contains(t, texts, "Search")
	assert.Contains(t, texts, "2 results for “cache”")

	// The sidebar keeps showing the page that was selected before searching.
	assert.Equal(t, "beta", f.view.CurrentPage())
	assert.True(t, f.view.sidebar["beta"].selected)
	assert.False(t, f.view.sidebar["alpha"].selected)
}

func TestSearch_SkipsRowsThatAreHidden(t *testing.T) {
	f := newSearchFixture(t, "beta")
	require.False(t, f.sm.rowOf["hiddenOpt"].Visible(), "the fixture row is hidden by VisibleIf")

	f.typeQuery("hidden")

	assert.Empty(t, f.resultRows())
	assert.Contains(t, f.contentTexts(), "No settings match “hidden”")
}

func TestSearch_SingularResultCountAndNestedTrail(t *testing.T) {
	f := newSearchFixture(t, "alpha")
	f.typeQuery("framed")

	assert.Contains(t, f.contentTexts(), "1 result for “framed”")
	rows := f.resultRows()
	require.Len(t, rows, 1)
	assert.Contains(t, rowTexts(rows[0]), "Museums › The Met › Gallery")
}

func TestSearch_FindsChoiceCardsRowByCardText(t *testing.T) {
	f := newSearchFixture(t, "alpha")
	f.typeQuery("flexib")

	rows := f.resultRows()
	require.Len(t, rows, 1)
	texts := rowTexts(rows[0])
	assert.Equal(t, "Fit mode", texts[0])
	assert.Contains(t, texts, "Smart fit")

	test.Tap(rows[0])
	assert.Equal(t, "smartfit", f.view.CurrentPage())
	assert.True(t, f.view.sidebar["smartfit"].selected)
}

func TestSearch_ClickingAResultOpensItsPageAndClearsTheField(t *testing.T) {
	f := newSearchFixture(t, "beta")
	f.typeQuery("framed")

	test.Tap(f.resultRows()[0])

	assert.Equal(t, "met", f.view.CurrentPage())
	assert.Empty(t, f.view.search.entry.Text, "the field is cleared")
	assert.False(t, f.view.search.showing)
	assert.Same(t, f.view.pages["met"], f.view.content.Objects[0])
	assert.True(t, f.view.sidebar[schema.NavPageMuseums].selected, "the museum's parent stays selected")
}

func TestSearch_ClearingTheFieldRestoresThePreviousPage(t *testing.T) {
	f := newSearchFixture(t, "beta")
	f.typeQuery("cache")
	require.True(t, f.view.search.showing)

	f.view.search.entry.SetText("")

	assert.False(t, f.view.search.showing)
	assert.Equal(t, "beta", f.view.CurrentPage())
	assert.Same(t, f.view.pages["beta"], f.view.content.Objects[0])
	assert.True(t, f.view.sidebar["beta"].selected)

	// Whitespace alone also counts as empty.
	f.typeQuery("cache")
	f.typeQuery("   ")
	assert.Same(t, f.view.pages["beta"], f.view.content.Objects[0])
}

func TestSearch_EscapeClearsTheField(t *testing.T) {
	f := newSearchFixture(t, "beta")
	f.typeQuery("cache")

	f.view.search.entry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEscape})

	assert.Empty(t, f.view.search.entry.Text)
	assert.Same(t, f.view.pages["beta"], f.view.content.Objects[0])
}

func TestSearch_EnterOpensTheFirstResult(t *testing.T) {
	f := newSearchFixture(t, "beta")
	f.typeQuery("cache")

	f.view.search.entry.OnSubmitted(f.view.search.entry.Text)

	assert.Equal(t, "alpha", f.view.CurrentPage())
	assert.Empty(t, f.view.search.entry.Text)
	assert.False(t, f.view.search.showing)

	// Enter with nothing typed, or nothing found, does nothing.
	f.view.search.entry.OnSubmitted("")
	f.typeQuery("zzzz")
	f.view.search.entry.OnSubmitted("zzzz")
	assert.Equal(t, "alpha", f.view.CurrentPage())
	assert.True(t, f.view.search.showing)
}

func TestSearch_NoResultsShowsEmptyState(t *testing.T) {
	f := newSearchFixture(t, "beta")
	f.typeQuery("zzzz")

	assert.Empty(t, f.resultRows())
	texts := f.contentTexts()
	assert.Contains(t, texts, "No settings match “zzzz”")
	assert.Contains(t, texts, "Try a different word, like “cache” or “faces”.")
	assert.Equal(t, "beta", f.view.CurrentPage())
}

func TestSearch_ChoosingASidebarPageEndsTheSearch(t *testing.T) {
	f := newSearchFixture(t, "beta")
	f.typeQuery("cache")

	f.view.Select("alpha")

	assert.Empty(t, f.view.search.entry.Text)
	assert.False(t, f.view.search.showing)
	assert.Same(t, f.view.pages["alpha"], f.view.content.Objects[0])
}

func TestSearch_ActionButtonClearsWhileThereIsText(t *testing.T) {
	f := newSearchFixture(t, "beta")
	f.typeQuery("cache")
	test.Tap(f.view.search.action)
	assert.Empty(t, f.view.search.entry.Text)
	assert.Same(t, f.view.pages["beta"], f.view.content.Objects[0])
}

func TestSearch_ResultScrollsToTheRowAndFlashesIt(t *testing.T) {
	f := newSearchFixture(t, "beta")
	f.typeQuery("number 35")
	rows := f.resultRows()
	require.NotEmpty(t, rows)
	assert.Equal(t, "Option number 35", rowTexts(rows[0])[0])

	test.Tap(rows[0])

	require.Equal(t, "alpha", f.view.CurrentPage())
	row := f.sm.rowOf["row35"]
	require.NotNil(t, row)
	sc, pos, ok := locateInScroll(f.view.pages["alpha"], row)
	require.True(t, ok)
	require.NotNil(t, sc)
	assert.Greater(t, sc.Offset.Y, float32(0), "the page scrolled down to the row")
	assert.GreaterOrEqual(t, pos.Y, sc.Offset.Y, "the row is not above the viewport")
	assert.Less(t, pos.Y+row.Size().Height, sc.Offset.Y+sc.Size().Height+1, "the row is not below the viewport")

	// The row has a flash rectangle; the test driver runs an animation straight to its end, so
	// the tint is already faded out here.
	rect, ok := row.(*fyne.Container).Objects[rowObjFlash].(*canvas.Rectangle)
	require.True(t, ok)
	assert.Zero(t, alphaOf(rect))
}

func TestFlashAlpha_HoldsThenFadesToNothing(t *testing.T) {
	assert.Equal(t, uint8(searchFlashPeak), flashAlpha(0))
	assert.Equal(t, uint8(searchFlashPeak), flashAlpha(searchFlashHoldUntil))
	mid := flashAlpha((1 + searchFlashHoldUntil) / 2)
	assert.Greater(t, mid, uint8(0))
	assert.Less(t, mid, uint8(searchFlashPeak))
	assert.Equal(t, uint8(0), flashAlpha(1))
}

func alphaOf(r *canvas.Rectangle) uint32 {
	_, _, _, a := r.FillColor.RGBA()
	return a
}

func TestSearch_ResultsPageKeepsRoomForTheSavePill(t *testing.T) {
	f := newSearchFixture(t, "beta")
	f.typeQuery("option")

	var column *fyne.Container
	walkTree(f.view.content, func(o fyne.CanvasObject) {
		if c, ok := o.(*fyne.Container); ok && column == nil {
			if l, isCol := c.Layout.(*pageColumnLayout); isCol && l.bottom == pageBottomForPill {
				column = c
			}
		}
	})
	require.NotNil(t, column, "the results column reserves the pill's space like other pages")
}

func TestLocateInScroll_ReportsPositionWithinContent(t *testing.T) {
	test.NewApp()
	row := widget.NewLabel("row")
	inner := container.NewVBox(widget.NewLabel("a"), row)
	scroll := container.NewVScroll(container.NewPadded(inner))
	scroll.Resize(fyne.NewSize(200, 40))
	page := container.NewBorder(widget.NewLabel("header"), nil, nil, nil, scroll)
	page.Resize(fyne.NewSize(200, 120))

	sc, pos, ok := locateInScroll(page, row)
	require.True(t, ok)
	assert.Same(t, scroll, sc)
	assert.Greater(t, pos.Y, float32(0))

	_, _, ok = locateInScroll(page, widget.NewLabel("elsewhere"))
	assert.False(t, ok)
}

func TestSearchTrail(t *testing.T) {
	setting := schema.SearchResult{SearchEntry: schema.SearchEntry{PageTrail: []string{"Museums", "The Met"}, SectionTitle: "Gallery:"}}
	assert.Equal(t, "Museums › The Met › Gallery", searchTrail(setting))
	top := schema.SearchResult{SearchEntry: schema.SearchEntry{PageTrail: []string{"Wallpaper"}, IsPage: true, Help: "How wallpapers change"}}
	assert.Equal(t, "How wallpapers change", searchTrail(top))
	nested := schema.SearchResult{SearchEntry: schema.SearchEntry{PageTrail: []string{"Museums", "The Met"}, IsPage: true}}
	assert.Equal(t, "Museums", searchTrail(nested))
}

// The real preferences schema: the examples from the design brief find the right rows.
func TestSearch_RealPreferencesSchema(t *testing.T) {
	keyring.MockInit()
	a := test.NewApp()
	defer a.Quit()
	sa := &SpiceApp{App: a, assetMgr: asset.NewManager(), appConfig: config.NewAppConfig(a.Preferences()), os: &noopOS{}}
	sa.Register(wallpaper.GetInstance())
	w := a.NewWindow("Real search")
	defer w.Close()
	sa.prefsWindow = w
	sm := NewSettingsManager(w).(*SettingsManager)
	sm.runAsync = func(f func()) { f() }
	nav, _ := sa.buildPreferencesNav(sm, schema.NavPageWallpaper)
	idx := schema.BuildSearchIndex(nav)

	top := func(q string) schema.SearchResult {
		res := schema.Search(idx, q)
		require.NotEmpty(t, res, q)
		return res[0]
	}
	assert.Equal(t, "Cache size", top("cache").Label)
	assert.Equal(t, schema.NavPageSmartFit, top("flexib").PageID, "card text finds the Smart fit mode row")
	assert.Equal(t, "Face crop", top("face crop").Label)
	assert.Empty(t, schema.Search(idx, "qqqzzzxxx"))

	// Every named result can be revealed: its row was rendered and remembered.
	newPrefsNavView(sm, nav, schema.NavPageWallpaper)
	for _, e := range idx {
		if e.SettingName == "" {
			continue
		}
		assert.Contains(t, sm.rowOf, e.SettingName, "no revealable row for %q (%s on %s)", e.SettingName, e.Label, e.PageID)
	}
}
