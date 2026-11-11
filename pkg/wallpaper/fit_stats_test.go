package wallpaper

import (
	"context"
	"image"
	"image/color"
	"path/filepath"
	"testing"

	"github.com/disintegration/imaging"
	"github.com/dixieflatline76/Spice/v2/pkg/provider"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingPrefs fails the test on any write: the preview and the stats are read-only, so a
// Config on these preferences proves they never write.
type recordingPrefs struct {
	Preferences
	t *testing.T
}

func (r recordingPrefs) fail(key string)                    { r.t.Errorf("unexpected preference write: %s", key) }
func (r recordingPrefs) SetBool(k string, _ bool)           { r.fail(k) }
func (r recordingPrefs) SetInt(k string, _ int)             { r.fail(k) }
func (r recordingPrefs) SetFloat(k string, _ float64)       { r.fail(k) }
func (r recordingPrefs) SetString(k string, _ string)       { r.fail(k) }
func (r recordingPrefs) SetStringList(k string, _ []string) { r.fail(k) }
func (r recordingPrefs) RemoveValue(k string)               { r.fail(k) }

// newStatsPlugin returns a plugin with one primary display of the given size showing img, on a
// Config whose preferences fail the test if written to.
func newStatsPlugin(t *testing.T, dispW, dispH int, img provider.Image, saved func(*Config)) *Plugin {
	t.Helper()
	isolateUserCache(t)
	ResetConfig()
	base := GetConfig(NewMockPreferences())
	if saved != nil {
		saved(base)
	}
	cfg := &Config{Preferences: recordingPrefs{Preferences: base.Preferences, t: t}, Tuning: DefaultTuningConfig()}

	dir := t.TempDir()
	fm := NewFileManager(dir)
	require.NoError(t, fm.EnsureDirs())
	store := NewImageStore()
	store.SetAsyncSave(false)
	store.SetFileManager(fm, filepath.Join(dir, "cache.json"))

	wp := &Plugin{cfg: cfg, fm: fm, store: store, Monitors: map[int]*MonitorController{}}
	wp.imgProcessor = NewVirtualFramer(NewSmartImageProcessor(nil, cfg, nil), cfg)
	mc := &MonitorController{ID: 0, Monitor: Monitor{ID: 0, Name: "Primary", Rect: image.Rect(0, 0, dispW, dispH)}, State: &MonitorState{CurrentImage: img}}
	wp.Monitors[0] = mc
	return wp
}

func TestComputeLibraryFit_CountsShapeAndSizeSeparately(t *testing.T) {
	displays := []Monitor{{ID: 0, Rect: image.Rect(0, 0, 3440, 1440)}, {ID: 1, Rect: image.Rect(0, 0, 1920, 1080)}}
	images := []provider.Image{
		{ID: "a", Width: 3840, Height: 2160}, // fits both
		{ID: "b", Width: 4000, Height: 3000}, // 4:3: wrong shape on the ultrawide under Quality; fits the 16:9
		{ID: "c", Width: 1920, Height: 1080}, // too small for the ultrawide; fits the 1080p
		{ID: "d", Width: 800, Height: 600},   // too small everywhere
		{ID: "e"},                            // not measured
		{ID: "f", Width: 3000},               // not measured (no height)
	}
	policy := FitPolicy{Mode: SmartFitNormal, MinSize: MinImageSizeDisplay, Tuning: DefaultTuningConfig()}
	fit := computeLibraryFit(displays, images, policy, false)

	require.Len(t, fit.Displays, 2)
	assert.Equal(t, 2, fit.Unmeasured)
	ultra, hd := fit.Displays[0], fit.Displays[1]
	assert.Equal(t, [3]int{1, 1, 2}, [3]int{ultra.Fit, ultra.WrongShape, ultra.TooSmall}, "3440x1440: fit a / wrong shape b / too small c,d")
	assert.Equal(t, [3]int{3, 0, 1}, [3]int{hd.Fit, hd.WrongShape, hd.TooSmall}, "1920x1080: a, b (4:3 is close enough to 16:9) and c fit / d too small")
	assert.Contains(t, ultra.Label, "3440×1440")
	assert.Equal(t, len(images)-fit.Unmeasured, ultra.Fit+ultra.WrongShape+ultra.TooSmall, "every measured image lands in exactly one bucket")

	// Loosening the policy moves images between buckets.
	loose := computeLibraryFit(displays, images, FitPolicy{Mode: SmartFitNormal, MinSize: MinImageSizeAny, Tuning: DefaultTuningConfig()}, false)
	assert.Equal(t, [3]int{2, 2, 0}, [3]int{loose.Displays[0].Fit, loose.Displays[0].WrongShape, loose.Displays[0].TooSmall}, "Any size: a and c fit; the 4:3 images b and d are shape misfits")
	off := computeLibraryFit(displays, images, FitPolicy{Mode: SmartFitOff, MinSize: MinImageSizeAny, Tuning: DefaultTuningConfig()}, false)
	assert.Equal(t, [3]int{4, 0, 0}, [3]int{off.Displays[0].Fit, off.Displays[0].WrongShape, off.Displays[0].TooSmall})

	// Shape misfits that virtual framing rescues count as fit; size misfits never do.
	rescued := computeLibraryFit(displays, images, policy, true)
	assert.Equal(t, [3]int{2, 0, 2}, [3]int{rescued.Displays[0].Fit, rescued.Displays[0].WrongShape, rescued.Displays[0].TooSmall})
}

func TestLibraryFitStats_UsesStagedPolicyAndWritesNothing(t *testing.T) {
	wp := newStatsPlugin(t, 3440, 1440, provider.Image{}, func(c *Config) {
		c.SetSmartFitMode(SmartFitNormal)
		c.SetMinImageSize(MinImageSizeDisplay)
	})
	for _, im := range []provider.Image{
		{ID: "Wallhaven_a", Width: 3840, Height: 2160},
		{ID: "Wallhaven_b", Width: 1920, Height: 1080},
		{ID: "Wallhaven_c"},
	} {
		require.True(t, wp.store.Add(im))
	}

	saved := wp.cfg.Policy()
	assert.Equal(t, SmartFitNormal, saved.Mode)
	assert.Equal(t, MinImageSizeDisplay, saved.MinSize)

	// Staged values come from the page's getter; the saved Config is untouched (recordingPrefs
	// fails the test on any write).
	staged := map[string]interface{}{settingSmartFitMode: int(SmartFitOff), settingMinImageSize: int(MinImageSizeAny)}
	rows := wp.LibraryFitRows(context.Background(), func(name string) interface{} { return staged[name] })
	require.Len(t, rows, 2)
	assert.Contains(t, rows[0].Label, "3440×1440")
	assert.Equal(t, "2 fit · 0 wrong shape · 0 too small", rows[0].Value, "Off with Any size: both measured images fit")
	assert.True(t, rows[1].Muted, "unmeasured images get a muted note, not a guess")
	assert.Contains(t, rows[1].Label, "1 images not measured yet")

	staged[settingSmartFitMode] = int(SmartFitNormal)
	staged[settingMinImageSize] = int(MinImageSizeDisplay)
	rows = wp.LibraryFitRows(context.Background(), func(name string) interface{} { return staged[name] })
	assert.Equal(t, "1 fit · 0 wrong shape · 1 too small", rows[0].Value)

	assert.Equal(t, SmartFitNormal, wp.cfg.GetSmartFitMode(), "reading stats never changes the saved mode")
}

func TestStagedFitSettings_FallBackToSavedValues(t *testing.T) {
	wp := newStatsPlugin(t, 1920, 1080, provider.Image{}, func(c *Config) {
		c.SetSmartFitMode(SmartFitNormal)
		c.SetMinImageSize(MinImageSizeSlight)
		c.SetFaceBoostEnabled(true)
		c.SetFaceCropEnabled(false)
	})
	got := wp.stagedFitSettings(func(string) interface{} { return nil })
	assert.Equal(t, FitSettings{Mode: SmartFitNormal, MinSize: MinImageSizeSlight, FaceBoost: true}, got)
	got = wp.stagedFitSettings(func(n string) interface{} {
		return map[string]interface{}{settingSmartFitMode: 2, settingFaceCrop: true}[n]
	})
	assert.Equal(t, FitSettings{Mode: SmartFitAggressive, MinSize: MinImageSizeSlight, FaceCrop: true, FaceBoost: true}, got)
}

// --- Preview: the real processor, never the saved Config ------------------------------------

func writeJPEG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: 120, A: 255})
		}
	}
	require.NoError(t, imaging.Save(img, path))
}

func previewFixture(t *testing.T, imgW, imgH int, saved func(*Config)) (*Plugin, provider.Image) {
	t.Helper()
	img := provider.Image{ID: "Wallhaven_pv", Path: "http://example.invalid/pv.jpg", Width: imgW, Height: imgH, Provider: "Wallhaven"}
	wp := newStatsPlugin(t, 3440, 1440, img, saved)
	master, err := wp.fm.GetMasterPath(img.ID, ".jpg")
	require.NoError(t, err)
	writeJPEG(t, master, imgW, imgH)
	deriv := filepath.Join(t.TempDir(), "deriv.jpg")
	writeJPEG(t, deriv, 344, 144)
	img.DerivativePaths = map[string]string{"3440x1440": deriv}
	img.ProcessingFlags = map[string]bool{"SmartFit": true, "FitQuality": true}
	wp.Monitors[0].State.CurrentImage = img
	return wp, img
}

func TestFitPreview_RunsTheRealProcessorOnStagedSettingsWithoutTouchingConfig(t *testing.T) {
	wp, _ := previewFixture(t, 1600, 1000, func(c *Config) { // 16:10, smaller than the display
		c.SetSmartFitMode(SmartFitNormal)
		c.SetMinImageSize(MinImageSizeAny)
	})
	before := wp.cfg.Policy()

	p := wp.FitPreviewFor(context.Background(), FitSettings{Mode: SmartFitAggressive, MinSize: MinImageSizeAny})
	require.True(t, p.HasWallpaper)
	require.NotNil(t, p.Original)
	require.NotNil(t, p.Current, "the file already on disk is used as is")
	require.NotNil(t, p.Staged, "an honest variant is produced for the staged mode")
	assert.Equal(t, "Quality", p.CurrentLabel, "current is described by the flags it was made with")
	assert.Contains(t, p.Display, "3440×1440")

	// The crop is to the display's shape: 3440:1440 at thumbnail size.
	b := p.Staged.Bounds()
	assert.InDelta(t, 3440.0/1440.0, float64(b.Dx())/float64(b.Dy()), 0.05)
	assert.LessOrEqual(t, b.Dx(), previewThumbMaxW, "thumbnail size is capped")
	assert.LessOrEqual(t, p.Original.Bounds().Dx(), previewThumbMaxW)

	assert.Equal(t, before, wp.cfg.Policy(), "the preview did not change the saved settings")
	// (recordingPrefs also fails the test on any preference write.)
}

func TestFitPreview_StagedOffKeepsTheOriginalShape(t *testing.T) {
	wp, _ := previewFixture(t, 1600, 1000, func(c *Config) { c.SetSmartFitMode(SmartFitNormal) })
	p := wp.FitPreviewFor(context.Background(), FitSettings{Mode: SmartFitOff, MinSize: MinImageSizeAny})
	require.NotNil(t, p.Staged)
	b := p.Staged.Bounds()
	assert.InDelta(t, 1.6, float64(b.Dx())/float64(b.Dy()), 0.05, "Off shows the original image, uncropped")
}

func TestFitPreview_SaysWhyAVariantIsMissing(t *testing.T) {
	wp, _ := previewFixture(t, 1600, 1000, func(c *Config) { c.SetMinImageSize(MinImageSizeAny) })

	p := wp.FitPreviewFor(context.Background(), FitSettings{Mode: SmartFitNormal, MinSize: MinImageSizeDisplay})
	assert.Nil(t, p.Staged)
	assert.Equal(t, "Too small, skipped", p.StagedNote, "1600x1000 is under the 3440x1440 display")
	assert.NotNil(t, p.Original)

	p = wp.FitPreviewFor(context.Background(), FitSettings{Mode: SmartFitNormal, MinSize: MinImageSizeAny})
	assert.NotNil(t, p.Staged, "16:10 fits Quality when size is not checked")

	// A portrait image under Quality is a shape misfit.
	wp2, _ := previewFixture(t, 1000, 1600, func(c *Config) { c.SetMinImageSize(MinImageSizeAny) })
	p = wp2.FitPreviewFor(context.Background(), FitSettings{Mode: SmartFitNormal, MinSize: MinImageSizeAny})
	assert.Nil(t, p.Staged)
	assert.Equal(t, "Wrong shape, skipped", p.StagedNote)

	// ...unless virtual framing would rescue it.
	wp2.cfg.VirtualFramingFallback = true
	p = wp2.FitPreviewFor(context.Background(), FitSettings{Mode: SmartFitNormal, MinSize: MinImageSizeAny})
	assert.Equal(t, "Shown in a frame", p.StagedNote)
}

func TestFitPreview_NoWallpaperYet(t *testing.T) {
	wp := newStatsPlugin(t, 3440, 1440, provider.Image{}, nil)
	p := wp.FitPreviewFor(context.Background(), FitSettings{})
	assert.False(t, p.HasWallpaper)
	assert.Empty(t, wp.FitPreviewThumbs(context.Background(), func(string) interface{} { return nil }))
}

func TestFitPreviewThumbs_ThirdPictureOnlyWhenSettingsDiffer(t *testing.T) {
	wp, _ := previewFixture(t, 1600, 1000, func(c *Config) {
		c.SetSmartFitMode(SmartFitNormal)
		c.SetMinImageSize(MinImageSizeAny)
	})
	saved := map[string]interface{}{settingSmartFitMode: 1, settingMinImageSize: 2}
	thumbs := wp.FitPreviewThumbs(context.Background(), func(n string) interface{} { return saved[n] })
	require.Len(t, thumbs, 3)
	assert.Equal(t, "Original", thumbs[0].Caption)
	assert.Equal(t, "Current", thumbs[1].Caption)
	assert.Nil(t, thumbs[2].Image, "nothing staged: nothing to show")
	assert.NotEmpty(t, thumbs[2].Note)

	staged := map[string]interface{}{settingSmartFitMode: 2, settingMinImageSize: 2}
	thumbs = wp.FitPreviewThumbs(context.Background(), func(n string) interface{} { return staged[n] })
	require.Len(t, thumbs, 3)
	assert.NotNil(t, thumbs[2].Image)
	assert.Contains(t, thumbs[2].Sub, "Flexibility")
	var _ schema.PreviewThumb = thumbs[0]
}

func TestFitPreview_ContextCancelledStopsEarly(t *testing.T) {
	wp, _ := previewFixture(t, 1600, 1000, func(c *Config) { c.SetMinImageSize(MinImageSizeAny) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := wp.FitPreviewFor(ctx, FitSettings{Mode: SmartFitAggressive, MinSize: MinImageSizeAny})
	assert.Nil(t, p.Staged)
	assert.Nil(t, p.Original)
}
