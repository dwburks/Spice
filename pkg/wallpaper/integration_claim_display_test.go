package wallpaper

import (
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/dixieflatline76/Spice/v2/pkg/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Integration (fork local/combined + cache reuse): the cross-monitor claim and the store's
// display references must describe the same on-screen image per monitor.
func TestClaimAndDisplayRefAgree(t *testing.T) {
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	store, fm, dir := newRetentionStore(t)
	for _, id := range []string{"a", "b"} {
		p := filepath.Join(dir, id+".jpg")
		require.NoError(t, os.WriteFile(p, []byte("x"), 0644))
		require.True(t, store.Add(imageWithPath(id, p)))
	}
	wp := &Plugin{}
	newMC := func(id int) *MonitorController {
		mos := new(MockOS)
		mos.On("Stat", mock.Anything).Return(nil, nil).Maybe()
		mos.On("SetWallpaper", mock.Anything, id).Return(nil).Maybe()
		mc := NewMonitorController(id, Monitor{ID: id, Rect: image.Rect(0, 0, 0, 0)}, store, fm, mos, cfg, nil)
		mc.ClaimImage = wp.claimImage
		return mc
	}
	mc0, mc1 := newMC(0), newMC(1)
	mc0.State.ShuffleIDs = []string{"a", "b"}
	mc1.State.ShuffleIDs = []string{"a", "b"}
	mc0.next(true)
	mc1.next(true)
	assert.Equal(t, "a", mc0.State.CurrentID)
	assert.Equal(t, "b", mc1.State.CurrentID, "fork de-dup still applies")
	assert.True(t, store.IsDisplayed("a"))
	assert.True(t, store.IsDisplayed("b"))

	// Monitor 0 tries to switch to an image whose file is missing: neither record moves.
	missing := imageWithPath("gone", filepath.Join(dir, "gone.jpg"))
	mos := new(MockOS)
	mos.On("Stat", missing.FilePath).Return(nil, os.ErrNotExist)
	mc0.os = mos
	mc0.applyImage(missing)
	assert.False(t, store.IsDisplayed("gone"), "reservation cancelled")
	assert.True(t, store.IsDisplayed("a"))
	assert.False(t, wp.claimImage(1, "a", false), "claim still points at the image monitor 0 shows")
	assert.True(t, wp.claimImage(1, "gone", false), "the unpublished image is not claimed by monitor 0")
}

func imageWithPath(id, path string) provider.Image {
	return provider.Image{ID: id, FilePath: path, DerivativePaths: map[string]string{"0x0": path}}
}
