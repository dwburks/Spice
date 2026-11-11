package ui

import (
	"bytes"
	"image/color"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"github.com/go-text/typesetting/font"

	"github.com/dixieflatline76/Spice/v2/asset"
	utilLog "github.com/dixieflatline76/Spice/v2/util/log"
)

// Spice theme: warm-tinted neutrals with a spice-orange accent, density 4/5, and the bundled
// Inter 4.1 (Regular + Bold) on every OS. Italic, bold-italic, monospace and symbol text keep
// the Fyne defaults.

// Custom color names used by the preferences window's own widgets (sidebar, cards, switches).
const (
	colorNameSidebar      fyne.ThemeColorName = "spiceSidebar"
	colorNameCard         fyne.ThemeColorName = "spiceCard"
	colorNameMuted        fyne.ThemeColorName = "spiceMuted"
	colorNameBorder       fyne.ThemeColorName = "spiceBorder"
	colorNameStrongBorder fyne.ThemeColorName = "spiceStrongBorder"
	colorNameSwitchTrack  fyne.ThemeColorName = "spiceSwitchTrack"
	colorNameIconTile     fyne.ThemeColorName = "spiceIconTile"
	colorNameWinSelection fyne.ThemeColorName = "spiceWinSelection"
	colorNameSwitchKnob   fyne.ThemeColorName = "spiceSwitchKnob"
)

// Custom size names (density 4/5).
const (
	sizeNameHelpText      fyne.ThemeSizeName = "spiceHelpText"
	sizeNameGroupLabel    fyne.ThemeSizeName = "spiceGroupLabel"
	sizeNameRowHeight     fyne.ThemeSizeName = "spiceRowHeight"
	sizeNameCardRadius    fyne.ThemeSizeName = "spiceCardRadius"
	sizeNameControlRadius fyne.ThemeSizeName = "spiceControlRadius"
	sizeNamePageTitle     fyne.ThemeSizeName = "spicePageTitle"
)

// spicePalette holds one variant's color tokens.
type spicePalette struct {
	bg, sidebar, card, text, muted, border, strong, track, accent, tile, winSel color.NRGBA
	button, disabledButton, disabled                                            color.NRGBA
}

func hexColor(v uint32) color.NRGBA {
	return color.NRGBA{R: uint8(v >> 16 & 0xFF), G: uint8(v >> 8 & 0xFF), B: uint8(v & 0xFF), A: 0xFF} //nolint:gosec // G115: each channel is masked to 8 bits
}

func withAlpha(c color.NRGBA, a uint8) color.NRGBA {
	c.A = a
	return c
}

var (
	lightPalette = spicePalette{
		bg:             hexColor(0xFAF8F5),
		sidebar:        hexColor(0xF3EFE9),
		card:           hexColor(0xFFFFFF),
		text:           hexColor(0x2C2A27),
		muted:          hexColor(0x6E6A63),
		border:         hexColor(0xE6E0D7),
		strong:         hexColor(0xD3CBBF),
		track:          hexColor(0xCDC5B8),
		accent:         hexColor(0xD85A30),
		tile:           hexColor(0xFBE7DD),
		winSel:         hexColor(0xE9E3DA),
		button:         hexColor(0xF3EFE9),
		disabledButton: hexColor(0xF0ECE6),
		disabled:       hexColor(0xB0A99F),
	}
	darkPalette = spicePalette{
		bg:             hexColor(0x1E1C1A),
		sidebar:        hexColor(0x262321),
		card:           hexColor(0x2D2A27),
		text:           hexColor(0xF2EEE8),
		muted:          hexColor(0xABA59C),
		border:         hexColor(0x3B3733),
		strong:         hexColor(0x4B4640),
		track:          hexColor(0x4B4640),
		accent:         hexColor(0xE66C3E),
		tile:           hexColor(0x3E2A20),
		winSel:         hexColor(0x35312C),
		button:         hexColor(0x3B3733),
		disabledButton: hexColor(0x2A2724),
		disabled:       hexColor(0x6E6A63),
	}
)

func paletteFor(v fyne.ThemeVariant) *spicePalette {
	if v == theme.VariantDark {
		return &darkPalette
	}
	return &lightPalette
}

// Bundled Inter fonts, validated once with go-text's ParseTTF. A font that fails to parse is left nil
// so the theme falls back to the Fyne default instead of panicking inside the renderer.
var (
	interOnce    sync.Once
	interRegular fyne.Resource
	interBold    fyne.Resource
)

// loadInterFont reads an embedded font and returns it only if go-text can parse it.
func loadInterFont(am *asset.Manager, name string) fyne.Resource {
	data, err := am.GetFont(name)
	if err != nil || len(data) == 0 {
		utilLog.Printf("theme: bundled font %s unavailable, using Fyne default: %v", name, err)
		return nil
	}
	if _, err := font.ParseTTF(bytes.NewReader(data)); err != nil {
		utilLog.Printf("theme: bundled font %s failed to parse, using Fyne default: %v", name, err)
		return nil
	}
	return fyne.NewStaticResource(name, data)
}

func interFonts() (regular, bold fyne.Resource) {
	interOnce.Do(func() {
		am := asset.NewManager()
		interRegular = loadInterFont(am, "Inter-Regular.ttf")
		interBold = loadInterFont(am, "Inter-Bold.ttf")
	})
	return interRegular, interBold
}

// spiceTheme applies the Spice palette, density and fonts on top of the Fyne default theme.
// When forced is true the variant is pinned (Theme setting Light/Dark); otherwise it follows the OS.
type spiceTheme struct {
	base    fyne.Theme
	variant fyne.ThemeVariant
	forced  bool
}

// newSpiceTheme returns the theme that follows the OS light/dark variant ("System").
func newSpiceTheme() *spiceTheme {
	return &spiceTheme{base: theme.DefaultTheme()}
}

// newForcedVariantTheme returns the theme pinned to the given variant ("Light" / "Dark").
func newForcedVariantTheme(v fyne.ThemeVariant) *spiceTheme {
	return &spiceTheme{base: theme.DefaultTheme(), variant: v, forced: true}
}

// themeForSetting maps the persisted Theme setting value to a theme. Unknown values follow the OS.
func themeForSetting(name string) fyne.Theme {
	switch name {
	case "Dark":
		return newForcedVariantTheme(theme.VariantDark)
	case "Light":
		return newForcedVariantTheme(theme.VariantLight)
	default:
		return newSpiceTheme()
	}
}

// Color implements fyne.Theme.
func (t *spiceTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	if t.forced {
		variant = t.variant
	}
	p := paletteFor(variant)
	switch name {
	case theme.ColorNameBackground:
		return p.bg
	case theme.ColorNameForeground:
		return p.text
	case theme.ColorNamePlaceHolder, colorNameMuted:
		return p.muted
	case theme.ColorNameSeparator, colorNameBorder:
		return p.border
	case theme.ColorNameInputBorder, colorNameStrongBorder:
		return p.strong
	case theme.ColorNameInputBackground:
		return p.card
	case theme.ColorNameButton:
		return p.button
	case theme.ColorNameDisabledButton:
		return p.disabledButton
	case theme.ColorNameDisabled:
		return p.disabled
	case theme.ColorNamePrimary, theme.ColorNameHyperlink:
		return p.accent
	case theme.ColorNameForegroundOnPrimary, colorNameSwitchKnob:
		return color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	case theme.ColorNameFocus:
		return withAlpha(p.accent, 0x66)
	case theme.ColorNameSelection:
		return withAlpha(p.accent, 0x40)
	case theme.ColorNameHover:
		return withAlpha(p.text, 0x0F)
	case theme.ColorNamePressed:
		return withAlpha(p.text, 0x1F)
	case theme.ColorNameHeaderBackground, colorNameSidebar:
		return p.sidebar
	case theme.ColorNameMenuBackground, theme.ColorNameOverlayBackground, colorNameCard:
		return p.card
	case theme.ColorNameScrollBar:
		return withAlpha(p.muted, 0x80)
	case colorNameSwitchTrack:
		return p.track
	case colorNameIconTile:
		return p.tile
	case colorNameWinSelection:
		return p.winSel
	}
	return t.base.Color(name, variant)
}

// Font implements fyne.Theme: Inter Regular / Bold, Fyne defaults for everything else.
func (t *spiceTheme) Font(style fyne.TextStyle) fyne.Resource {
	if style.Monospace || style.Symbol || style.Italic {
		return t.base.Font(style)
	}
	regular, bold := interFonts()
	if style.Bold {
		if bold != nil {
			return bold
		}
		return t.base.Font(style)
	}
	if regular != nil {
		return regular
	}
	return t.base.Font(style)
}

// Icon implements fyne.Theme.
func (t *spiceTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return t.base.Icon(name)
}

// Size implements fyne.Theme (density 4/5: 13pt body, ~11.5pt help, ~46px rows, roomy radii).
func (t *spiceTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameText:
		return 13
	case theme.SizeNameCaptionText, sizeNameHelpText:
		return 11.5
	case sizeNameGroupLabel:
		return 11.5
	case theme.SizeNameHeadingText, sizeNamePageTitle:
		return 20
	case theme.SizeNameSubHeadingText:
		return 15
	case theme.SizeNameInlineIcon:
		return 18
	case theme.SizeNameInputRadius, theme.SizeNameButtonRadius, sizeNameControlRadius:
		return 7
	case theme.SizeNameSelectionRadius:
		return 5
	case theme.SizeNameCardRadius, theme.SizeNameDialogRadius, sizeNameCardRadius:
		return 10
	case theme.SizeNamePopupRadius, theme.SizeNameMenuRadius:
		return 8
	case sizeNameRowHeight:
		return 46
	}
	return t.base.Size(name)
}

// isForcedDark reports whether th is a Spice theme pinned to the dark variant.
func isForcedDark(th fyne.Theme) (forced bool, dark bool) {
	if st, ok := th.(*spiceTheme); ok && st.forced {
		return true, st.variant == theme.VariantDark
	}
	return false, false
}

// themeColor resolves a (possibly custom) color name against the current app theme and variant.
// Custom Spice names fall back to the Spice palette when another theme is active (e.g. tests).
func themeColor(name fyne.ThemeColorName) color.Color {
	if _, ok := theme.Current().(*spiceTheme); !ok && strings.HasPrefix(string(name), "spice") {
		variant := theme.VariantLight
		if app := fyne.CurrentApp(); app != nil {
			variant = app.Settings().ThemeVariant()
		}
		return newSpiceTheme().Color(name, variant)
	}
	return theme.Color(name)
}

// themeSize resolves a (possibly custom) size name against the current app theme.
func themeSize(name fyne.ThemeSizeName) float32 {
	if _, ok := theme.Current().(*spiceTheme); !ok && strings.HasPrefix(string(name), "spice") {
		return newSpiceTheme().Size(name)
	}
	return theme.Size(name)
}
