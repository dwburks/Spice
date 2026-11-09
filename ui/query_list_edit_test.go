package ui

import (
	"net/url"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func renderEditableList(t *testing.T, editCfg *schema.AddQueryConfig, queries []schema.Query) (*SettingsManager, *queryListWidget) {
	t.Helper()
	app := test.NewApp()
	t.Cleanup(app.Quit)
	sm := NewSettingsManager(app.NewWindow("QueryEdit")).(*SettingsManager)

	obj := sm.renderQueryList(schema.QueryListItem{
		GetQueries:   func() []schema.Query { return queries },
		EnableQuery:  func(string) error { return nil },
		DisableQuery: func(string) error { return nil },
		RemoveQuery:  func(string) error { return nil },
		EditConfig:   editCfg,
	})
	ql, ok := obj.(*queryListWidget)
	require.True(t, ok)
	return sm, ql
}

func TestRenderQueryList_EditButtonVisibility(t *testing.T) {
	queries := []schema.Query{
		{ID: "user", URL: "https://example.com/a", Description: "User", Active: true},
		{ID: "synced", URL: "https://example.com/b", Description: "Synced", Managed: true},
	}

	t.Run("EditConfig set: only non-managed rows", func(t *testing.T) {
		_, ql := renderEditableList(t, &schema.AddQueryConfig{Title: "Edit"}, queries)
		require.Len(t, ql.rows, 2)
		assert.True(t, ql.rows[0].editButton.Visible(), "non-managed row shows edit button")
		assert.False(t, ql.rows[1].editButton.Visible(), "managed row hides edit button")
	})

	t.Run("EditConfig nil: no edit buttons", func(t *testing.T) {
		_, ql := renderEditableList(t, nil, queries)
		require.Len(t, ql.rows, 2)
		assert.False(t, ql.rows[0].editButton.Visible())
		assert.False(t, ql.rows[1].editButton.Visible())
	})
}

func TestRenderQueryList_EditCommitsImmediatelyWithoutPendingChange(t *testing.T) {
	var gotID, gotDesc, gotURL string
	cfg := &schema.AddQueryConfig{
		Title: "Edit",
		UpdateHandler: func(id, desc, u string) error {
			gotID, gotDesc, gotURL = id, desc, u
			return nil
		},
	}
	queries := []schema.Query{{ID: "user", URL: "https://api.example.com/a", Description: "User", Active: true}}

	app := test.NewApp()
	defer app.Quit()
	w := app.NewWindow("QueryEdit")
	sm := NewSettingsManager(w).(*SettingsManager)
	ql := sm.renderQueryList(schema.QueryListItem{
		GetQueries:    func() []schema.Query { return queries },
		EnableQuery:   func(string) error { return nil },
		DisableQuery:  func(string) error { return nil },
		RemoveQuery:   func(string) error { return nil },
		GetDisplayURL: func(q schema.Query) *url.URL { u, _ := url.Parse("https://www.example.com/a"); return u },
		EditConfig:    cfg,
	}).(*queryListWidget)
	w.SetContent(ql)
	w.Resize(fyneSize(900, 700))
	w.Show()

	test.Tap(ql.rows[0].editButton)
	top := w.Canvas().Overlays().Top()
	require.NotNil(t, top, "edit dialog must open")

	// Dialog is prefilled with the display URL and current description.
	var entries []string
	collectEntries(top, &entries)
	assert.Contains(t, entries, "https://www.example.com/a")
	assert.Contains(t, entries, "User")

	// Drive the dialog: tap the Save button.
	save := findButtonByText(top, "Save")
	require.NotNil(t, save)
	require.False(t, save.Disabled(), "prefilled valid form must enable Save")
	test.Tap(save)

	assert.Equal(t, "user", gotID)
	assert.Equal(t, "User", gotDesc)
	assert.Equal(t, "https://www.example.com/a", gotURL)
	assert.False(t, sm.HasPendingChange("user"), "edit must not stage a pending change")
	assert.Nil(t, w.Canvas().Overlays().Top(), "dialog closes after a successful save")
}

func fyneSize(w, h float32) fyne.Size { return fyne.NewSize(w, h) }

func walkTree(obj fyne.CanvasObject, visit func(fyne.CanvasObject)) {
	visit(obj)
	var children []fyne.CanvasObject
	switch o := obj.(type) {
	case *fyne.Container:
		children = o.Objects
	case fyne.Widget:
		children = test.WidgetRenderer(o).Objects()
	}
	for _, c := range children {
		walkTree(c, visit)
	}
}

func collectEntries(root fyne.CanvasObject, out *[]string) {
	walkTree(root, func(o fyne.CanvasObject) {
		if e, ok := o.(*widget.Entry); ok {
			*out = append(*out, e.Text)
		}
	})
}
