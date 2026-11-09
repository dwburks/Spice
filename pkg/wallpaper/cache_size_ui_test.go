package wallpaper

import (
	"testing"

	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Cache Size dropdown index is not the CacheSize enum value (0 and 2 are retired),
// so the UI must translate both ways through the domain helpers.

func cacheSizeSelect(t *testing.T, wp *Plugin) schema.SelectItem {
	t.Helper()
	for _, sec := range NewPrefsPanelBuilder(wp, nil).BuildGeneralTabSchema().Sections {
		for _, it := range sec.Items {
			if si, ok := it.(schema.SelectItem); ok && si.Name == "cacheSize" {
				return si
			}
		}
	}
	t.Fatal("cacheSize select not found")
	return schema.SelectItem{}
}

func TestCacheSize_RoundTripEveryOption(t *testing.T) {
	labels := []string{"100", "300", "500", "1000", "5000", "10000"}
	opts := GetCacheSizes()
	require.Len(t, opts, len(labels))
	for i, label := range labels {
		cs := IndexToCacheSize(i)
		assert.Equal(t, label, opts[i].String())
		assert.Equal(t, label, cs.String(), "index %d", i)
		assert.Equal(t, i, CacheSizeToIndex(cs), "index %d round trip", i)

		ResetConfig()
		cfg := GetConfig(NewMockPreferences())
		cfg.SetCacheSizeIndex(i)
		assert.Equal(t, cs, cfg.GetCacheSize())
		assert.Equal(t, i, cfg.GetCacheSizeIndex())
	}
	assert.Equal(t, Cache300Images, IndexToCacheSize(-1))
	assert.Equal(t, Cache300Images, IndexToCacheSize(len(labels)))
}

func TestCacheSize_DefaultDisplaysAs300(t *testing.T) {
	ResetConfig()
	wp := &Plugin{cfg: GetConfig(NewMockPreferences())}
	si := cacheSizeSelect(t, wp)
	idx, ok := si.InitialValue.(int)
	require.True(t, ok)
	assert.Equal(t, "300", si.Options[idx], "the default must show as 300 (was shown as 1000)")
}

func TestCacheSize_ApplyStoresTheChosenEnum(t *testing.T) {
	for i, want := range []CacheSize{Cache100Images, Cache300Images, Cache500Images, Cache1000Images, Cache5000Images, Cache10000Images} {
		ResetConfig()
		cfg := GetConfig(NewMockPreferences())
		wp := &Plugin{cfg: cfg}
		si := cacheSizeSelect(t, wp)
		si.ApplyFunc(i) // the settings manager passes the selected index
		assert.Equal(t, want, cfg.GetCacheSize(), "option %q", si.Options[i])
		assert.Equal(t, si.Options[i], cfg.GetCacheSize().String())
		assert.Equal(t, i, cacheSizeSelect(t, wp).InitialValue, "re-opening shows the same choice")
	}
}

// Stored prefs keep the meaning they have today: the value is read as a CacheSize enum.
// 0 (former "None") and 2 (former "200") already read as 300.
func TestCacheSize_LegacyPrefValuesLoadUnchanged(t *testing.T) {
	cases := []struct {
		stored  int
		size    int
		display string
	}{
		{0, 300, "300"}, {1, 100, "100"}, {2, 300, "300"}, {3, 300, "300"},
		{4, 500, "500"}, {5, 1000, "1000"}, {6, 5000, "5000"}, {7, 10000, "10000"},
	}
	for _, c := range cases {
		ResetConfig()
		prefs := NewMockPreferences()
		prefs.SetInt(CacheSizePrefKey, c.stored)
		cfg := GetConfig(prefs)
		assert.Equal(t, c.size, cfg.GetCacheSize().Size(), "stored %d", c.stored)
		si := cacheSizeSelect(t, &Plugin{cfg: cfg})
		assert.Equal(t, c.display, si.Options[si.InitialValue.(int)], "stored %d", c.stored)
		assert.Equal(t, c.stored, prefs.Int(CacheSizePrefKey), "loading never rewrites the pref")
	}
}
