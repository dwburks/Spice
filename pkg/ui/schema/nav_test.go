package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeNav_MergesGroupsAndPagesByID(t *testing.T) {
	app := &NavSchema{Groups: []NavGroup{{
		ID: NavGroupGeneral, Title: "General", Order: 10,
		Items: []NavItem{
			{ID: NavPageApp, Title: "App", Order: 10, Panel: &PanelSchema{Sections: []SectionSchema{{Title: "Appearance"}}}},
			{ID: NavPageAdvanced, Title: "Advanced", Order: 80, Panel: &PanelSchema{Sections: []SectionSchema{{Title: "Diagnostics"}}}},
		},
	}}}
	plugin := &NavSchema{
		Selected: "Wallhaven",
		Groups: []NavGroup{
			{ID: NavGroupSources, Title: "Sources", Order: 20, Items: []NavItem{{ID: "Wallhaven", Title: "wallhaven"}}},
			{ID: NavGroupGeneral, Order: 10, Items: []NavItem{
				{ID: NavPageAdvanced, Description: "Diagnostics and updates", Order: 80, Panel: &PanelSchema{Sections: []SectionSchema{{Title: "Updates"}}}},
				{ID: NavPageWallpaper, Title: "Wallpaper", Order: 30},
			}},
		},
	}

	merged := MergeNav(app, nil, plugin)
	require.Len(t, merged.Groups, 2)
	assert.Equal(t, NavGroupGeneral, merged.Groups[0].ID)
	assert.Equal(t, NavGroupSources, merged.Groups[1].ID)
	assert.Equal(t, "Wallhaven", merged.Selected)

	general := merged.Groups[0].Items
	require.Len(t, general, 3)
	assert.Equal(t, []string{NavPageApp, NavPageWallpaper, NavPageAdvanced}, []string{general[0].ID, general[1].ID, general[2].ID},
		"pages sort by Order, not by contribution order")

	adv := general[2]
	assert.Equal(t, "Advanced", adv.Title, "first non-empty title wins")
	assert.Equal(t, "Diagnostics and updates", adv.Description, "missing description is filled from a later contribution")
	require.NotNil(t, adv.Panel)
	require.Len(t, adv.Panel.Sections, 2)
	assert.Equal(t, "Diagnostics", adv.Panel.Sections[0].Title)
	assert.Equal(t, "Updates", adv.Panel.Sections[1].Title)

	// Merging must not mutate the contributions.
	assert.Len(t, app.Groups[0].Items[1].Panel.Sections, 1)
}

func TestNavSchema_FindItemSearchesChildren(t *testing.T) {
	n := &NavSchema{Groups: []NavGroup{{ID: NavGroupSources, Items: []NavItem{
		{ID: NavPageMuseums, Title: "Museums", Children: []NavItem{{ID: "MetMuseum", Title: "The Met"}}},
	}}}}

	item, parent := n.FindItem("MetMuseum")
	require.NotNil(t, item)
	require.NotNil(t, parent)
	assert.Equal(t, "The Met", item.Title)
	assert.Equal(t, NavPageMuseums, parent.ID)

	item, parent = n.FindItem(NavPageMuseums)
	require.NotNil(t, item)
	assert.Nil(t, parent)

	item, _ = n.FindItem("missing")
	assert.Nil(t, item)
	assert.Equal(t, NavPageMuseums, n.FirstItemID())
}
