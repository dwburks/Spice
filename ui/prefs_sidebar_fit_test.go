package ui

import (
	"testing"

	"fyne.io/fyne/v2/test"
	"github.com/dixieflatline76/Spice/v2/asset"
	"github.com/dixieflatline76/Spice/v2/config"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
	"github.com/dixieflatline76/Spice/v2/pkg/wallpaper"
	"github.com/zalando/go-keyring"
)

// TestPrefsSidebarFitsDefaultWindow keeps every sidebar group visible without scrolling at the
// default 960x680 Preferences size (Windows reported Library cut off at the bottom).
func TestPrefsSidebarFitsDefaultWindow(t *testing.T) {
	keyring.MockInit()
	a := test.NewApp()
	defer a.Quit()

	sa := &SpiceApp{App: a, assetMgr: asset.NewManager(), appConfig: config.NewAppConfig(a.Preferences()), os: &noopOS{}}
	sa.Register(wallpaper.GetInstance())

	w := a.NewWindow("Spice Preferences")
	defer w.Close()
	sa.prefsWindow = w
	sm := NewSettingsManager(w).(*SettingsManager)
	nav, _ := sa.buildPreferencesNav(sm, schema.NavPageWallpaper)
	v := newPrefsNavView(sm, nav, schema.NavPageWallpaper)

	got := v.sidebarBox.MinSize().Height + 2*12 // sidebar outer padding
	t.Logf("sidebar content height %.0f", got)
	if got > prefsDefaultWindowSize.Height {
		t.Fatalf("sidebar needs %.0f px, more than the %.0f px default window height", got, prefsDefaultWindowSize.Height)
	}
}
