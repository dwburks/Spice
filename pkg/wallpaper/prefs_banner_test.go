package wallpaper

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dixieflatline76/Spice/v2/pkg/provider"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func bannerTestPlugin() *Plugin {
	return &Plugin{
		cfg:       GetConfig(NewMockPreferences()),
		providers: make(map[string]provider.ImageProvider),
	}
}

func bannerTestMonitor(id int, path, providerID string) *MonitorController {
	return &MonitorController{
		ID:      id,
		Monitor: Monitor{ID: id},
		State:   &MonitorState{CurrentImage: provider.Image{ID: "img", FilePath: path, Provider: providerID}},
	}
}

// TestPrimaryWallpaperBanner_SnapshotsPrimaryMonitor covers the banner port data: the inner ring
// snapshots the primary monitor's current image path + provider title as plain values.
func TestPrimaryWallpaperBanner_SnapshotsPrimaryMonitor(t *testing.T) {
	dir := t.TempDir()
	primary := filepath.Join(dir, "primary.jpg")
	secondary := filepath.Join(dir, "secondary.jpg")
	require.NoError(t, os.WriteFile(primary, []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(secondary, []byte("x"), 0o600))

	wp := bannerTestPlugin()
	mp := &MockImageProvider{}
	mp.On("Title").Return("wallhaven")
	wp.providers["Wallhaven"] = mp
	wp.Monitors = map[int]*MonitorController{
		1: bannerTestMonitor(1, secondary, "Pexels"),
		0: bannerTestMonitor(0, primary, "Wallhaven"),
	}

	got := wp.PrimaryWallpaperBanner()
	require.NotNil(t, got)
	assert.Equal(t, schema.BannerData{ImagePath: primary, ProviderName: "wallhaven"}, *got)

	// Without monitor 0, the lowest-numbered monitor is used; unknown providers keep their ID.
	delete(wp.Monitors, 0)
	got = wp.PrimaryWallpaperBanner()
	require.NotNil(t, got)
	assert.Equal(t, secondary, got.ImagePath)
	assert.Equal(t, "Pexels", got.ProviderName)
}

func TestPrimaryWallpaperBanner_NilWhenNothingToShow(t *testing.T) {
	wp := bannerTestPlugin()
	assert.Nil(t, wp.PrimaryWallpaperBanner(), "no monitors")

	wp.Monitors = map[int]*MonitorController{0: bannerTestMonitor(0, "", "")}
	assert.Nil(t, wp.PrimaryWallpaperBanner(), "nothing shown yet")

	wp.Monitors[0] = bannerTestMonitor(0, filepath.Join(t.TempDir(), "gone.jpg"), "Wallhaven")
	assert.Nil(t, wp.PrimaryWallpaperBanner(), "file deleted from disk")
}

// TestBuildNavSchema_WallpaperPageCarriesBanner checks the builder maps the snapshot onto the
// Wallpaper page (banner header) and lays out the General pages in sidebar order.
func TestBuildNavSchema_WallpaperPageCarriesBanner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "now.jpg")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	wp := bannerTestPlugin()
	wp.Monitors = map[int]*MonitorController{0: bannerTestMonitor(0, path, "Wallhaven")}

	nav := NewPrefsPanelBuilder(wp, nil).BuildNavSchema()
	page, _ := nav.FindItem(schema.NavPageWallpaper)
	require.NotNil(t, page)
	assert.Equal(t, schema.HeaderBanner, page.Header)
	require.NotNil(t, page.Banner)
	assert.Equal(t, path, page.Banner.ImagePath)

	var ids []string
	for _, it := range nav.Groups[0].Items {
		ids = append(ids, it.ID)
	}
	assert.Equal(t, []string{schema.NavPageWallpaper, schema.NavPageSmartFit, schema.NavPageFraming,
		schema.NavPagePause, schema.NavPageDisplays, schema.NavPageAdvanced}, ids)

	// Every General setting is on exactly one page; the storage group holds the new storage
	// settings and Blocked images.
	storage, _ := nav.FindItem(schema.NavPageWallpaper)
	var names []string
	for _, sec := range storage.Panel.Sections {
		if sec.ID != "storage" {
			continue
		}
		for _, it := range sec.Items {
			switch v := it.(type) {
			case schema.SelectItem:
				names = append(names, v.Name)
			case schema.ConfirmButtonItem:
				names = append(names, v.Name)
			}
		}
	}
	assert.Equal(t, []string{"cacheSize", "maxDownloadSize", "cacheDiskSpace", "clearCache", "resetAvoidSet"}, names)
}
