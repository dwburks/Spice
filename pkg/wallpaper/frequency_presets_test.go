package wallpaper

import (
	"strconv"
	"testing"

	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChangeFrequency_PresetSelect checks the frequency row folds the old preset buttons into
// one select whose values are the same minutes pref (and keeps the minutes entry for Custom).
func TestChangeFrequency_PresetSelect(t *testing.T) {
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	wp := &Plugin{cfg: cfg}

	var freq *schema.TextItem
	for _, sec := range NewPrefsPanelBuilder(wp, nil).BuildGeneralTabSchema().Sections {
		for _, it := range sec.Items {
			switch v := it.(type) {
			case schema.TextItem:
				if v.Name == "changeFrequency" {
					item := v
					freq = &item
				}
			case schema.HorizontalRowItem:
				assert.NotEqual(t, "freq_shortcuts", v.ID, "the preset buttons row is gone")
			}
		}
	}
	require.NotNil(t, freq)
	assert.Equal(t, "Change frequency", freq.Label)
	assert.Equal(t, "Custom…", freq.CustomLabel)
	assert.True(t, freq.IsNumeric)

	var values []string
	for _, p := range freq.Presets {
		_, err := strconv.Atoi(p.Value)
		require.NoError(t, err, "preset %q must be a minutes value", p.Label)
		require.NoError(t, freq.Validator(p.Value))
		values = append(values, p.Value)
	}
	assert.Equal(t, []string{"15", "30", "60", "180", "360", "720", "1440", "0"}, values)
	assert.Equal(t, strconv.Itoa(int(cfg.GetWallpaperChangeFrequency())), freq.InitialValue)
	assert.Error(t, freq.Validator("-5"))
}
