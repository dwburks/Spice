package ui

import (
	"bytes"
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"github.com/go-text/typesetting/font"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dixieflatline76/Spice/v2/asset"
)

func TestTheme_BundledInterFontsParse(t *testing.T) {
	am := asset.NewManager()
	for _, name := range []string{"Inter-Regular.ttf", "Inter-Bold.ttf"} {
		data, err := am.GetFont(name)
		require.NoError(t, err, name)
		_, err = font.ParseTTF(bytes.NewReader(data))
		require.NoError(t, err, "%s must parse with go-text", name)
	}
	_, err := am.GetFont("Inter-LICENSE.txt")
	require.NoError(t, err, "the OFL license must ship next to the fonts")

	th := newSpiceTheme()
	assert.Equal(t, "Inter-Regular.ttf", th.Font(fyne.TextStyle{}).Name())
	assert.Equal(t, "Inter-Bold.ttf", th.Font(fyne.TextStyle{Bold: true}).Name())
	// Italic and monospace stay on the Fyne defaults.
	assert.Equal(t, theme.DefaultTheme().Font(fyne.TextStyle{Italic: true}).Name(), th.Font(fyne.TextStyle{Italic: true}).Name())
	assert.Equal(t, theme.DefaultTheme().Font(fyne.TextStyle{Monospace: true}).Name(), th.Font(fyne.TextStyle{Monospace: true}).Name())
}

func TestTheme_UnparseableFontFallsBackToNil(t *testing.T) {
	assert.Nil(t, loadInterFont(asset.NewManager(), "does-not-exist.ttf"))
}

func TestTheme_PaletteAndForcedVariant(t *testing.T) {
	light := themeForSetting("Light")
	dark := themeForSetting("Dark")
	system := themeForSetting("System")

	// Forced variants ignore the variant argument.
	assert.Equal(t, color.Color(hexColor(0xFAF8F5)), light.Color(theme.ColorNameBackground, theme.VariantDark))
	assert.Equal(t, color.Color(hexColor(0x1E1C1A)), dark.Color(theme.ColorNameBackground, theme.VariantLight))
	assert.Equal(t, color.Color(hexColor(0xD85A30)), light.Color(theme.ColorNamePrimary, theme.VariantLight))
	assert.Equal(t, color.Color(hexColor(0xE66C3E)), dark.Color(theme.ColorNamePrimary, theme.VariantLight))

	// System follows the variant it is asked for.
	assert.Equal(t, color.Color(hexColor(0x2D2A27)), system.Color(colorNameCard, theme.VariantDark))
	assert.Equal(t, color.Color(hexColor(0xFFFFFF)), system.Color(colorNameCard, theme.VariantLight))

	forced, isDark := isForcedDark(dark)
	assert.True(t, forced)
	assert.True(t, isDark)
	forced, _ = isForcedDark(system)
	assert.False(t, forced)

	assert.Equal(t, float32(13), system.Size(theme.SizeNameText))
	assert.Equal(t, float32(46), system.Size(sizeNameRowHeight))
}
