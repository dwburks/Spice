package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func searchFixtureNav() *NavSchema {
	return &NavSchema{Groups: []NavGroup{
		{ID: NavGroupGeneral, Title: "General", Items: []NavItem{
			{ID: "wallpaper", Title: "Wallpaper", Description: "How wallpapers change", Panel: &PanelSchema{Sections: []SectionSchema{
				{Title: "Rotation", Items: []ItemSchema{
					TextItem{Name: "freq", Label: "Change frequency", Help: "How often the wallpaper changes.", Presets: []TextPreset{{Label: "Every 15 minutes", Value: "15"}}},
					BoolItem{Name: "nightly", Label: "Nightly refresh", Help: "Fetch new images overnight so the cache stays full."},
				}},
				{Title: "Storage", Items: []ItemSchema{
					SliderItem{Name: "cacheSize", Label: "Cache size", Help: "Disk space for downloads."},
					ConfirmButtonItem{Name: "clearCache", Label: "Clear wallpaper cache", ButtonText: "Clear cache", ConfirmMessage: "All downloaded images will be deleted."},
					BoolItem{Name: "dupA", Label: "Duplicate"},
					HorizontalRowItem{ID: "row", Items: []ItemSchema{
						BoolItem{Name: "dupA", Label: "Duplicate"},
						ButtonItem{Name: "preview", ButtonText: "Preview"},
					}},
				}},
			}}},
			{ID: "smartfit", Title: "Smart fit", Description: "Crop images to fit your display", Panel: &PanelSchema{Sections: []SectionSchema{
				{Title: "Fit mode", Items: []ItemSchema{
					ChoiceCardsItem{Name: "fitMode", Cards: []ChoiceCard{
						{Title: "Quality", Description: "Crops only a little."},
						{Title: "Flexibility", Description: "Larger images may crop more."},
					}},
				}},
				{Title: "Faces", Items: []ItemSchema{
					LabelItem{Text: "Framing", IsTitle: true},
					BoolItem{Name: "faceCrop", Label: "Face crop", Help: "Frames tightly around the largest face."},
					StatsItem{Name: "stats"},
				}},
			}}},
		}},
		{ID: NavGroupSources, Title: "Sources", Items: []NavItem{
			{ID: NavPageMuseums, Title: "Museums", Description: "Art collections", Children: []NavItem{
				{ID: "met", Title: "The Met", Panel: &PanelSchema{Sections: []SectionSchema{{Title: "Display", Items: []ItemSchema{
					BoolItem{Name: "met_framed", Label: "Fréquence des cadres", Help: "Show art in a gallery frame."},
				}}}}},
			}},
		}},
	}}
}

func labels(res []SearchResult) []string {
	var out []string
	for _, r := range res {
		out = append(out, r.Label)
	}
	return out
}

func TestSearch_EmptyQueryReturnsNil(t *testing.T) {
	idx := BuildSearchIndex(searchFixtureNav())
	assert.Nil(t, Search(idx, ""))
	assert.Nil(t, Search(idx, "   "))
	assert.Nil(t, Search(nil, "cache"))
}

func TestBuildSearchIndex_PagesSettingsAndChildren(t *testing.T) {
	idx := BuildSearchIndex(searchFixtureNav())

	byLabel := map[string]SearchEntry{}
	for _, e := range idx {
		if _, dup := byLabel[e.Label]; !dup {
			byLabel[e.Label] = e
		}
	}
	page := byLabel["Wallpaper"]
	assert.True(t, page.IsPage)
	assert.Equal(t, "wallpaper", page.PageID)
	assert.Equal(t, "How wallpapers change", page.Help)

	freq := byLabel["Change frequency"]
	assert.Equal(t, []string{"Wallpaper"}, freq.PageTrail)
	assert.Equal(t, "Rotation", freq.SectionTitle)
	assert.Equal(t, "freq", freq.SettingName)
	assert.Equal(t, []string{"Every 15 minutes"}, freq.Options)

	child := byLabel["Fréquence des cadres"]
	assert.Equal(t, "met", child.PageID)
	assert.Equal(t, []string{"Museums", "The Met"}, child.PageTrail)
	assert.True(t, byLabel["The Met"].IsPage)

	// A choice-cards row has no label of its own: it is shown under its section's name and
	// carries the card text as options.
	fit := byLabel["Fit mode"]
	assert.Equal(t, "fitMode", fit.SettingName)
	assert.Empty(t, fit.SectionTitle)
	assert.Contains(t, fit.Options, "Flexibility")

	// A sub-heading becomes the section title of the rows that follow it.
	assert.Equal(t, "Framing", byLabel["Face crop"].SectionTitle)

	// Rows with nothing to read (stats) are skipped.
	for _, e := range idx {
		assert.NotEqual(t, "stats", e.SettingName)
	}
}

func TestSearch_RanksLabelBeforePageSectionHelpAndOptions(t *testing.T) {
	idx := BuildSearchIndex(searchFixtureNav())

	got := labels(Search(idx, "cache"))
	require.Len(t, got, 3)
	assert.Equal(t, "Cache size", got[0], "label starts with the word")
	assert.Equal(t, "Clear wallpaper cache", got[1], "a later label word starts with it")
	assert.Equal(t, "Nightly refresh", got[2], "help text match ranks after the labels")

	// "wallpaper": the page itself, then rows whose label contains it.
	got = labels(Search(idx, "wallpaper"))
	require.GreaterOrEqual(t, len(got), 3)
	assert.Equal(t, "Wallpaper", got[0])
	assert.Equal(t, "Clear wallpaper cache", got[1])

	// Page title > section title > help text > option text.
	ranked := Search([]SearchEntry{
		{PageID: "p", PageTrail: []string{"Other"}, SectionTitle: "Other", Label: "Zeta", Help: "mentions wallpaper"},
		{PageID: "p", PageTrail: []string{"Other"}, SectionTitle: "Wallpaper things", Label: "Alpha"},
		{PageID: "p", PageTrail: []string{"Wallpaper"}, Label: "Beta"},
		{PageID: "p", PageTrail: []string{"Other"}, Label: "Gamma", Options: []string{"wallpaper mode"}},
	}, "wallpaper")
	assert.Equal(t, []string{"Beta", "Alpha", "Zeta", "Gamma"}, labels(ranked))
}

func TestSearch_FindsCardTextAndPresetOptions(t *testing.T) {
	idx := BuildSearchIndex(searchFixtureNav())

	res := Search(idx, "flexib")
	require.Len(t, res, 1)
	assert.Equal(t, "Fit mode", res[0].Label)
	assert.Equal(t, "fitMode", res[0].SettingName)

	res = Search(idx, "15 minutes")
	require.NotEmpty(t, res)
	assert.Equal(t, "Change frequency", res[0].Label)
}

func TestSearch_AllWordsMustMatch(t *testing.T) {
	idx := BuildSearchIndex(searchFixtureNav())

	assert.Equal(t, []string{"Clear wallpaper cache"}, labels(Search(idx, "clear cache")))
	assert.Empty(t, Search(idx, "cache banana"))
	// Words may be spread over different fields of one entry.
	res := Search(idx, "nightly overnight")
	require.Len(t, res, 1)
	assert.Equal(t, "Nightly refresh", res[0].Label)
	// Word order does not matter.
	assert.Equal(t, labels(Search(idx, "cache clear")), labels(Search(idx, "clear cache")))
}

func TestSearch_CaseAndAccentInsensitive(t *testing.T) {
	idx := BuildSearchIndex(searchFixtureNav())

	for _, q := range []string{"frequence des", "FRÉQUENCE", "fréquence", "Frequence"} {
		res := Search(idx, q)
		require.NotEmpty(t, res, q)
		assert.Equal(t, "Fréquence des cadres", res[0].Label, q)
	}
	// An accent-free label matches an accented query too.
	res := Search(idx, "cáche")
	require.NotEmpty(t, res)
	assert.Equal(t, "Cache size", res[0].Label)
	// Decomposed text (e + combining acute) folds the same way.
	assert.NotEmpty(t, Search(idx, "fréquence"))

	assert.Equal(t, "strasse", foldSearchText("Straße"))
	assert.Equal(t, "olcn", foldSearchText("ØLÇÑ"))
}

func TestSearch_DedupesAndKeepsNavigationOrder(t *testing.T) {
	idx := BuildSearchIndex(searchFixtureNav())

	var dup int
	for _, r := range Search(idx, "duplicate") {
		if r.SettingName == "dupA" {
			dup++
		}
	}
	assert.Equal(t, 1, dup, "the same setting listed twice appears once")

	// Equal matches keep navigation order.
	res := Search([]SearchEntry{
		{PageID: "a", PageTrail: []string{"A"}, SettingName: "z", Label: "Image one"},
		{PageID: "a", PageTrail: []string{"A"}, SettingName: "y", Label: "Image two"},
		{PageID: "b", PageTrail: []string{"B"}, SettingName: "x", Label: "Image three"},
	}, "image")
	assert.Equal(t, []string{"Image one", "Image two", "Image three"}, labels(res))
}

func TestSearch_CapsResults(t *testing.T) {
	var entries []SearchEntry
	for i := 0; i < 80; i++ {
		entries = append(entries, SearchEntry{PageID: "p", SettingName: string(rune('a'+i%26)) + string(rune('A'+i/26)), Label: "Setting"})
	}
	assert.Len(t, Search(entries, "setting"), 50)
}
