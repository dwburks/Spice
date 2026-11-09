package ui

import (
	"testing"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
)

func renderFrequency(t *testing.T, initial string, applied *[]string) (*SettingsManager, *widget.Select, *widget.Entry) {
	t.Helper()
	app := test.NewApp()
	t.Cleanup(app.Quit)
	sm := NewSettingsManager(app.NewWindow("Presets")).(*SettingsManager)
	sm.RenderSchema(schema.PanelSchema{Sections: []schema.SectionSchema{{Items: []schema.ItemSchema{
		schema.TextItem{
			Name:         "changeFrequency",
			Label:        "Change frequency",
			InitialValue: initial,
			IsNumeric:    true,
			Presets: []schema.TextPreset{
				{Label: "Every hour", Value: "60"},
				{Label: "Every 3 hours", Value: "180"},
				{Label: "Never", Value: "0"},
			},
			CustomLabel: "Custom…",
			Suffix:      "minutes",
			ApplyFunc:   func(s string) { *applied = append(*applied, s) },
		},
	}}}})
	sel, ok := sm.allWidgets["changeFrequency__preset"].(*widget.Select)
	require.True(t, ok)
	entry := sm.allWidgets["changeFrequency"].(*widget.Entry)
	return sm, sel, entry
}

// customShown reports whether the minutes entry (and its suffix) is visible.
func customShown(sm *SettingsManager, name string) bool {
	return sm.allWidgets[name+"__custom"].Visible()
}

func TestPresetSelect_InitialValueMapsToPresetOrCustom(t *testing.T) {
	var applied []string
	sm, sel, entry := renderFrequency(t, "180", &applied)
	assert.Equal(t, "Every 3 hours", sel.Selected)
	assert.False(t, customShown(sm, "changeFrequency"), "a preset value hides the minutes entry")
	assert.Equal(t, "180", entry.Text)

	sm2, sel2, entry2 := renderFrequency(t, "45", &applied)
	assert.Equal(t, "Custom…", sel2.Selected, "an existing non-preset value shows as Custom")
	assert.True(t, customShown(sm2, "changeFrequency"), "Custom shows the minutes entry")
	assert.Equal(t, "45", entry2.Text, "with its number")
	assert.False(t, sm2.IsDirty(), "rendering never stages anything")
}

func TestPresetSelect_PickStagesSameMinutesPrefAndRevertRestores(t *testing.T) {
	var applied []string
	sm, sel, entry := renderFrequency(t, "60", &applied)

	sel.SetSelected("Never")
	assert.Equal(t, "0", entry.Text)
	assert.True(t, sm.HasPendingChange("changeFrequency"), "a preset stages the minutes setting")
	assert.Empty(t, applied, "staged, not saved")

	sm.RevertPending()
	assert.Equal(t, "60", entry.Text)
	assert.Equal(t, "Every hour", sel.Selected, "Revert puts the select back on the baseline preset")
	assert.False(t, sm.IsDirty())

	sel.SetSelected("Every 3 hours")
	test.Tap(sm.GetApplySettingsButton())
	assert.Equal(t, []string{"180"}, applied, "Save writes minutes through the existing ApplyFunc")
	assert.Equal(t, "180", sm.GetBaseline("changeFrequency"))
}

func TestPresetSelect_CustomRevealsEntryWithoutStaging(t *testing.T) {
	var applied []string
	sm, sel, entry := renderFrequency(t, "60", &applied)

	sel.SetSelected("Custom…")
	assert.True(t, customShown(sm, "changeFrequency"))
	assert.False(t, sm.IsDirty(), "choosing Custom alone changes nothing")

	entry.SetText("90")
	assert.True(t, sm.HasPendingChange("changeFrequency"))
	assert.Equal(t, "Custom…", sel.Selected, "typing keeps Custom selected")

	sm.SetValue("changeFrequency", "180") // programmatic sync keeps the select in step
	assert.Equal(t, "Every 3 hours", sel.Selected)
	assert.False(t, customShown(sm, "changeFrequency"))
}
