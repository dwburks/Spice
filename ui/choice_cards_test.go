package ui

import (
	"context"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"github.com/dixieflatline76/Spice/v2/asset"
	"github.com/dixieflatline76/Spice/v2/config"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
	"github.com/dixieflatline76/Spice/v2/pkg/wallpaper"
)

func renderCards(t *testing.T, initial int, applied *[]int) (*SettingsManager, []*choiceCard) {
	t.Helper()
	app := test.NewApp()
	t.Cleanup(app.Quit)
	sm := NewSettingsManager(app.NewWindow("Cards")).(*SettingsManager)
	sm.RenderSchema(schema.PanelSchema{Sections: []schema.SectionSchema{{Items: []schema.ItemSchema{
		schema.ChoiceCardsItem{
			Name: "mode",
			Cards: []schema.ChoiceCard{
				{Title: "Off", Description: "off", Illustration: schema.IllustrationNone},
				{Title: "Quality", Description: "quality", Illustration: schema.IllustrationQuality},
				{Title: "Flexibility", Description: "flex", Illustration: schema.IllustrationFlexibility},
			},
			InitialValue: initial,
			ApplyFunc:    func(i int) { *applied = append(*applied, i) },
			NeedsRefresh: true,
		},
	}}}})
	strip, ok := sm.allWidgets["mode"].(*fyne.Container)
	require.True(t, ok, "the card strip is registered under the item's name")
	var cards []*choiceCard
	for _, o := range strip.Objects {
		cards = append(cards, o.(*choiceCard))
	}
	require.Len(t, cards, 3)
	return sm, cards
}

func selectedIndex(cards []*choiceCard) int {
	for i, c := range cards {
		if c.selected {
			return i
		}
	}
	return -1
}

func TestChoiceCards_RenderSeedsBaselineWithoutStaging(t *testing.T) {
	var applied []int
	sm, cards := renderCards(t, 2, &applied)
	assert.Equal(t, 2, selectedIndex(cards))
	assert.Equal(t, 2, sm.GetBaseline("mode"))
	assert.Equal(t, 2, sm.GetValue("mode"))
	assert.False(t, sm.IsDirty())
	assert.Empty(t, applied)
}

func TestChoiceCards_PickStagesUntilSaveAndRevertRestores(t *testing.T) {
	var applied []int
	sm, cards := renderCards(t, 2, &applied)

	test.Tap(cards[1])
	assert.Equal(t, 1, selectedIndex(cards), "the tapped card is selected")
	assert.Equal(t, 1, sm.GetValue("mode"))
	assert.True(t, sm.HasPendingChange("mode"), "a pick stages the setting")
	assert.True(t, sm.IsDirty())
	assert.True(t, sm.GetApplySettingsButton().Disabled() == false, "Save is enabled")
	assert.Empty(t, applied, "nothing is applied before Save")
	assert.Equal(t, 2, sm.GetBaseline("mode"), "the baseline is the saved value")

	// Picking the saved card again cancels the pending change.
	test.Tap(cards[2])
	assert.False(t, sm.HasPendingChange("mode"))
	assert.False(t, sm.IsDirty())

	test.Tap(cards[0])
	require.True(t, sm.HasPendingChange("mode"))
	sm.RevertPending()
	assert.Equal(t, 2, selectedIndex(cards), "Revert restores the saved selection")
	assert.Equal(t, 2, sm.GetValue("mode"))
	assert.False(t, sm.IsDirty())
	assert.Empty(t, applied, "Revert writes nothing")

	test.Tap(cards[0])
	test.Tap(sm.GetApplySettingsButton())
	assert.Equal(t, []int{0}, applied, "Save applies the staged index through ApplyFunc, once")
	assert.Equal(t, 0, sm.GetBaseline("mode"), "the new baseline is the applied value")
	assert.False(t, sm.IsDirty())
}

func TestChoiceCards_KeyboardArrowsMoveSelection(t *testing.T) {
	var applied []int
	sm, cards := renderCards(t, 1, &applied)
	cards[1].TypedKey(&fyne.KeyEvent{Name: fyne.KeyRight})
	assert.Equal(t, 2, selectedIndex(cards))
	assert.True(t, sm.HasPendingChange("mode"))
	cards[2].TypedKey(&fyne.KeyEvent{Name: fyne.KeyRight})
	assert.Equal(t, 2, selectedIndex(cards), "no wrap past the last card")
	assert.Empty(t, applied)
}

func TestChoiceCards_SetValueStagesWithoutOnChanged(t *testing.T) {
	app := test.NewApp()
	t.Cleanup(app.Quit)
	sm := NewSettingsManager(app.NewWindow("Cards")).(*SettingsManager)
	called := false
	sm.RenderSchema(schema.PanelSchema{Sections: []schema.SectionSchema{{Items: []schema.ItemSchema{
		schema.ChoiceCardsItem{
			Name:         "mode",
			Cards:        []schema.ChoiceCard{{Title: "A"}, {Title: "B"}},
			InitialValue: 0,
			OnChanged:    func(int) { called = true },
		},
	}}}})
	sm.SetValue("mode", 1)
	assert.Equal(t, 1, sm.GetValue("mode"))
	assert.True(t, sm.HasPendingChange("mode"))
	assert.False(t, called, "SetValue never fires the item's OnChanged")
}

// The Windows tester saw "wallpaper_smart_fit_mode_key=2" written the moment Flexibility was
// selected. On the real Smart fit page the stored mode must stay as it was until Save.
func TestSmartFitPage_PickingAModeDoesNotWritePreferenceBeforeSave(t *testing.T) {
	keyring.MockInit()
	a := test.NewApp()
	defer a.Quit()
	sa := &SpiceApp{App: a, assetMgr: asset.NewManager(), appConfig: config.NewAppConfig(a.Preferences()), os: &noopOS{}}
	sa.Register(wallpaper.GetInstance())
	cfg := wallpaper.GetConfig(a.Preferences())
	cfg.SetSmartFitMode(wallpaper.SmartFitNormal)
	cfg.SetMinImageSize(wallpaper.MinImageSizeDisplay)

	w := a.NewWindow("Spice Preferences")
	defer w.Close()
	sa.prefsWindow = w
	sm := NewSettingsManager(w).(*SettingsManager)
	sm.runAsync = func(f func()) { f() }
	nav, _ := sa.buildPreferencesNav(sm, schema.NavPageSmartFit)
	v := newPrefsNavView(sm, nav, schema.NavPageSmartFit)
	w.SetContent(v.Root())
	w.Resize(fyne.NewSize(960, 680))

	prefs := a.Preferences()
	strip := sm.allWidgets["smartFitMode"].(*fyne.Container)
	flexibility := strip.Objects[2].(*choiceCard)
	test.Tap(flexibility)
	require.Equal(t, 2, sm.GetValue("smartFitMode"))

	assert.Equal(t, int(wallpaper.SmartFitNormal), prefs.IntWithFallback(wallpaper.SmartFitModePrefKey, -1), "stored preference unchanged while only staged")
	assert.Equal(t, wallpaper.SmartFitNormal, cfg.GetSmartFitMode(), "Config unchanged while only staged")
	assert.True(t, sm.HasPendingChange("smartFitMode"))

	// The minimum-size select stages the same way.
	require.Contains(t, sm.allWidgets, "minImageSize")
	sm.SetValue("minImageSize", 2)
	assert.Equal(t, int(wallpaper.MinImageSizeDisplay), prefs.IntWithFallback(wallpaper.MinImageSizePrefKey, -1)+0, "min size stored value unchanged while staged")
	assert.Equal(t, wallpaper.MinImageSizeDisplay, cfg.GetMinImageSize())

	// Save runs the staged ApplyFunc (called directly: the full Save also kicks off a wallpaper
	// refresh, which is not what is under test).
	sm.chgPrefsCallbacks["smartFitMode"]()
	sm.chgPrefsCallbacks["minImageSize"]()
	assert.Equal(t, wallpaper.SmartFitAggressive, cfg.GetSmartFitMode())
	assert.Equal(t, 2, prefs.IntWithFallback(wallpaper.SmartFitModePrefKey, -1))
	assert.Equal(t, wallpaper.MinImageSizeAny, cfg.GetMinImageSize())
}

// Live items must not decode or compute anything until their page is first shown.
func TestLiveItems_ComputeOnFirstShowThenOnDependencyChange(t *testing.T) {
	app := test.NewApp()
	t.Cleanup(app.Quit)
	sm := NewSettingsManager(app.NewWindow("Live")).(*SettingsManager)
	sm.runAsync = func(f func()) { f() }

	var seen []int
	nav := &schema.NavSchema{Groups: []schema.NavGroup{{ID: "g", Items: []schema.NavItem{
		{ID: "one", Title: "One", Panel: &schema.PanelSchema{Sections: []schema.SectionSchema{{Items: []schema.ItemSchema{
			schema.ChoiceCardsItem{Name: "mode", Cards: []schema.ChoiceCard{{Title: "A"}, {Title: "B"}}, InitialValue: 0},
			schema.StatsItem{
				Name:      "stats",
				DependsOn: []string{"mode"},
				Compute: func(_ context.Context, get func(string) interface{}) []schema.StatRow {
					m, _ := get("mode").(int)
					seen = append(seen, m)
					return []schema.StatRow{{Label: "row", Value: "v"}}
				},
			},
		}}}}},
		{ID: "two", Title: "Two", Panel: &schema.PanelSchema{}},
	}}}}
	v := newPrefsNavView(sm, nav, "two")
	assert.Empty(t, seen, "an unvisited page computes nothing")

	v.Select("one")
	assert.Equal(t, []int{0}, seen, "first view computes once")

	sm.SetValue("mode", 1)
	assert.Equal(t, []int{0, 1}, seen, "a dependency change recomputes with the staged value")
	sm.SetValue("mode", 1)
	assert.Equal(t, []int{0, 1}, seen, "no change, no recompute")
	sm.RevertPending()
	assert.Equal(t, []int{0, 1, 0}, seen, "Revert recomputes for the baseline")
}
