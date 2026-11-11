package wallpaper

import (
	"context"
	"errors"
	"image"
	"image/color"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/disintegration/imaging"
	"github.com/dixieflatline76/Spice/v2/pkg/provider"
	"github.com/dixieflatline76/Spice/v2/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// --- The size rule ------------------------------------------------------------------------

func TestMinImageSize_Rule(t *testing.T) {
	tests := []struct {
		name         string
		imgW, imgH   int
		dispW, dispH int
		mode         MinImageSize
		tooSmall     bool
	}{
		{"1920x1080 on 3440x1440 is too small", 1920, 1080, 3440, 1440, MinImageSizeDisplay, true},
		{"3840x2160 on 3440x1440 fits", 3840, 2160, 3440, 1440, MinImageSizeDisplay, false},
		{"exact display size fits", 3440, 1440, 3440, 1440, MinImageSizeDisplay, false},
		{"one pixel narrow is too small", 3439, 1440, 3440, 1440, MinImageSizeDisplay, true},
		{"one pixel short is too small", 3440, 1439, 3440, 1440, MinImageSizeDisplay, true},
		{"wide enough but short is too small", 5000, 1000, 3440, 1440, MinImageSizeDisplay, true},
		{"80% of the display is accepted", 2752, 1152, 3440, 1440, MinImageSizeSlight, false},
		{"just under 80% is too small", 2751, 1152, 3440, 1440, MinImageSizeSlight, true},
		{"80% in one dimension only is too small", 2752, 1151, 3440, 1440, MinImageSizeSlight, true},
		{"70% is too small at 80%", 2408, 1008, 3440, 1440, MinImageSizeSlight, true},
		{"any size accepts a thumbnail", 64, 48, 3440, 1440, MinImageSizeAny, false},
		{"unknown width is never too small", 0, 1080, 3440, 1440, MinImageSizeDisplay, false},
		{"unknown height is never too small", 1920, 0, 3440, 1440, MinImageSizeDisplay, false},
		{"unknown display is never too small", 1920, 1080, 0, 0, MinImageSizeDisplay, false},
		{"MacBook 3024x1964 needs at least that", 3024, 1964, 3024, 1964, MinImageSizeDisplay, false},
		{"4K on a MacBook fits", 3840, 2160, 3024, 1964, MinImageSizeDisplay, false},
		{"2560x1440 on a MacBook is too small", 2560, 1440, 3024, 1964, MinImageSizeDisplay, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.tooSmall, ImageTooSmall(tt.imgW, tt.imgH, tt.dispW, tt.dispH, tt.mode))
		})
	}
}

func TestCoverScale(t *testing.T) {
	assert.InDelta(t, 3440.0/1920.0, CoverScale(1920, 1080, 3440, 1440), 1e-9)
	assert.InDelta(t, 1440.0/1080.0, CoverScale(3440, 1080, 3440, 1440), 1e-9, "the taller side decides")
	assert.Equal(t, 0.0, CoverScale(0, 10, 10, 10))
}

func TestMinImageSize_ConfigDefaultAndRoundTrip(t *testing.T) {
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	assert.Equal(t, MinImageSizeDisplay, cfg.GetMinImageSize(), "default is at least display size")
	cfg.SetMinImageSize(MinImageSizeSlight)
	assert.Equal(t, MinImageSizeSlight, cfg.GetMinImageSize())
	cfg.SetMinImageSize(MinImageSizeAny)
	assert.Equal(t, MinImageSizeAny, cfg.GetMinImageSize())
	cfg.SetInt(MinImageSizePrefKey, 99)
	assert.Equal(t, MinImageSizeDisplay, cfg.GetMinImageSize(), "an out-of-range stored value falls back to the default")
	assert.Equal(t, "wallpaper_min_image_size_key", MinImageSizePrefKey)
	assert.Len(t, GetMinImageSizes(), 3)
}

func TestFitPolicy_ReportsShapeAndSizeSeparately(t *testing.T) {
	tuning := DefaultTuningConfig()
	quality := FitPolicy{Mode: SmartFitNormal, MinSize: MinImageSizeDisplay, Tuning: tuning}

	// Right shape, too small.
	err := quality.Check(1920, 1080, 3440, 1440)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrImageTooSmall))
	assert.False(t, errors.Is(err, ErrShapeIncompatible))

	// Big enough, wrong shape (4:3 on an ultrawide under Quality).
	err = quality.Check(4000, 3000, 3440, 1440)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrShapeIncompatible))
	assert.False(t, errors.Is(err, ErrImageTooSmall))

	// Both wrong: size is reported (framing cannot fix it).
	err = quality.Check(800, 1200, 3440, 1440)
	assert.True(t, errors.Is(err, ErrImageTooSmall))

	assert.NoError(t, quality.Check(3840, 2160, 3440, 1440))
}

func TestFitPolicy_SizeRuleAppliesWhenSmartFitIsOff(t *testing.T) {
	off := FitPolicy{Mode: SmartFitOff, MinSize: MinImageSizeDisplay, Tuning: DefaultTuningConfig()}
	assert.True(t, errors.Is(off.Check(1920, 1080, 3440, 1440), ErrImageTooSmall), "Off still skips small images")
	assert.NoError(t, off.Check(3840, 2160, 3440, 1440))
	assert.NoError(t, off.Check(4000, 6000, 3440, 1440), "Off never judges shape")

	anySize := FitPolicy{Mode: SmartFitOff, MinSize: MinImageSizeAny, Tuning: DefaultTuningConfig()}
	assert.NoError(t, anySize.Check(1920, 1080, 3440, 1440), "Any size with Off is the old behavior")
}

func TestFitPolicy_CopyClaimsOn3440x1440(t *testing.T) {
	// The numbers quoted on the Smart fit cards.
	tuning := DefaultTuningConfig()
	anySize := func(mode SmartFitMode) FitPolicy {
		return FitPolicy{Mode: mode, MinSize: MinImageSizeAny, Tuning: tuning}
	}
	quality, flex := anySize(SmartFitNormal), anySize(SmartFitAggressive)
	for _, c := range [][2]int{{1920, 1080}, {1920, 1200}} { // 16:9, 16:10
		assert.NoError(t, quality.Check(c[0], c[1], 3440, 1440), "Quality fits %dx%d", c[0], c[1])
	}
	assert.Error(t, quality.Check(1600, 1200, 3440, 1440), "Quality skips 4:3")
	assert.Error(t, quality.Check(1080, 1920, 3440, 1440), "Quality skips portrait")

	assert.NoError(t, flex.Check(2000, 1500, 3440, 1440), "Flexibility fits 4:3")
	assert.NoError(t, flex.Check(2125, 2125, 3440, 1440), "Flexibility fits a square from about 2125 px")
	assert.Error(t, flex.Check(2100, 2100, 3440, 1440), "and not much below it")
	assert.Error(t, flex.Check(1080, 1920, 3440, 1440), "Flexibility skips portrait")
}

func TestVirtualFramer_NeverRescuesATooSmallImage(t *testing.T) {
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	cfg.SetSmartFitMode(SmartFitNormal)
	cfg.VirtualFramingFallback = true
	vf := NewVirtualFramer(NewSmartImageProcessor(nil, cfg, nil), cfg)

	err := vf.CheckCompatibility(1920, 1080, 3440, 1440)
	assert.True(t, errors.Is(err, ErrImageTooSmall), "framing cannot fix low resolution")
	assert.False(t, errors.Is(err, ErrRequiresVirtualFraming))

	cfg.SetMinImageSize(MinImageSizeAny)
	assert.NoError(t, vf.CheckCompatibility(1920, 1080, 3440, 1440))
	assert.True(t, errors.Is(vf.CheckCompatibility(4000, 3000, 3440, 1440), ErrRequiresVirtualFraming), "a shape misfit is still rescued by framing")
}

// --- Flags: changing the setting re-evaluates images ---------------------------------------------

func TestProcessingFlags_IncludeMinImageSize(t *testing.T) {
	wp := flagFixturePlugin(true, SmartFitNormal, false, false)
	wp.cfg.SetMinImageSize(MinImageSizeDisplay)
	display := wp.processingFlags()
	assert.True(t, display["MinSizeDisplay"])
	assert.False(t, display["MinSizeSlight"])

	var img provider.Image
	wp.stampProcessingFlags(&img)
	store := &ImageStore{}
	assert.True(t, store.flagsMatch(img, wp.processingFlags()), "an image stamped now matches")

	wp.cfg.SetMinImageSize(MinImageSizeSlight)
	assert.False(t, store.flagsMatch(img, wp.processingFlags()), "changing the minimum size invalidates it")
	wp.cfg.SetMinImageSize(MinImageSizeAny)
	assert.False(t, store.flagsMatch(img, wp.processingFlags()))

	// Images processed before this setting existed carry neither key: they stay valid only
	// under "Any size", which is exactly the behavior they were processed with.
	legacy := provider.Image{ProcessingFlags: map[string]bool{}}
	legacyPlugin := flagFixturePlugin(false, SmartFitOff, false, false)
	for k, v := range legacyPlugin.processingFlags() {
		if k != "MinSizeDisplay" && k != "MinSizeSlight" {
			legacy.ProcessingFlags[k] = v
		}
	}
	legacyPlugin.cfg.SetMinImageSize(MinImageSizeAny)
	assert.True(t, store.flagsMatch(legacy, legacyPlugin.processingFlags()))
	legacyPlugin.cfg.SetMinImageSize(MinImageSizeDisplay)
	assert.False(t, store.flagsMatch(legacy, legacyPlugin.processingFlags()), "the new default re-evaluates old images")
}

func newTombstoneStore(t *testing.T) *ImageStore {
	t.Helper()
	isolateUserCache(t)
	dir := t.TempDir()
	fm := NewFileManager(dir)
	require.NoError(t, fm.EnsureDirs())
	store := NewImageStore()
	store.SetAsyncSave(false)
	store.SetFileManager(fm, filepath.Join(dir, "cache.json"))
	return store
}

// A rejected download leaves a masterless record carrying "incompatible:" tags. Its tags were
// decided under specific settings: they must outlive unrelated Syncs, but not a change of the
// setting that produced them, or lowering the requirement could never let the image back in.
func TestSync_SizeRejectionRecordIsDroppedWhenTheSettingChanges(t *testing.T) {
	store := newTombstoneStore(t)
	wp := flagFixturePlugin(true, SmartFitNormal, false, false)
	wp.cfg.SetMinImageSize(MinImageSizeDisplay)

	rejected := provider.Image{ID: "Wallhaven_small", Width: 1920, Height: 1080, ProcessingFlags: map[string]bool{"incompatible:3440x1440": true}}
	wp.stampProcessingFlags(&rejected)
	require.True(t, store.Add(rejected))

	store.Sync(100, wp.processingFlags(), nil)
	assert.True(t, store.Exists("Wallhaven_small"), "unchanged settings keep the rejection record")

	wp.cfg.SetMinImageSize(MinImageSizeAny)
	store.Sync(100, wp.processingFlags(), nil)
	assert.False(t, store.Exists("Wallhaven_small"), "lowering the requirement drops the stale record so the image is judged again")
}

// --- Pre-download skip (fetch) ----------------------------------------------------------

func newSizeFetchPlugin(t *testing.T, mode MinImageSize) (*Plugin, *MockPipeline, *countingProvider) {
	t.Helper()
	wp, _ := newCapPlugin(t, 50)
	wp.cfg.SetMinImageSize(mode)
	mos := new(MockOS)
	mos.On("GetMonitors").Return([]Monitor{{ID: 0, Rect: image.Rect(0, 0, 3440, 1440)}}, nil)
	mos.On("GetDesktopDimension").Return(3440, 1440, nil)
	wp.os = mos

	cp := &countingProvider{MockImageProvider: new(MockImageProvider)}
	cp.MockImageProvider.On("ID").Return("Wallhaven")
	cp.MockImageProvider.On("Type").Return(provider.TypeCommunity)
	cp.MockImageProvider.On("FetchImages", mock.Anything, "http://mock.url", 1).Return([]provider.Image{
		{ID: "small", Path: "http://example.invalid/small.jpg", Width: 1920, Height: 1080},
		{ID: "big", Path: "http://example.invalid/big.jpg", Width: 3840, Height: 2160},
		{ID: "unknown", Path: "http://example.invalid/unknown.jpg"},
	}, nil)
	sub := new(MockPipeline)
	sub.On("Submit", mock.Anything, mock.Anything).Return(true)
	wp.jobSubmitter = sub
	wp.queryPages = make(map[string]*util.SafeCounter)
	return wp, sub, cp
}

func submittedIDs(sub *MockPipeline) []string {
	var ids []string
	for _, c := range sub.Calls {
		ids = append(ids, c.Arguments.Get(1).(DownloadJob).Image.ID)
	}
	return ids
}

func TestFetch_SkipsImagesTooSmallForEveryDisplayBeforeSubmitting(t *testing.T) {
	wp, sub, cp := newSizeFetchPlugin(t, MinImageSizeDisplay)
	var mu sync.Mutex
	q := ImageQuery{ID: "q1", URL: "http://mock.url", Active: true, Provider: "Wallhaven"}
	wp.fetchFromProvider(context.Background(), q, cp, false, &mu, map[string]bool{}, util.NewSafeInt())

	assert.ElementsMatch(t, []string{"Wallhaven_big", "Wallhaven_unknown"}, submittedIDs(sub),
		"the too-small image is skipped; the one with unknown size waits for the post-download check")
	assert.Equal(t, 2, wp.queryPages["q1"].Value(), "a page whose images are partly skipped still counts: no page wrap")
}

func TestFetch_AnySizeSubmitsEverything(t *testing.T) {
	wp, sub, cp := newSizeFetchPlugin(t, MinImageSizeAny)
	var mu sync.Mutex
	q := ImageQuery{ID: "q1", URL: "http://mock.url", Active: true, Provider: "Wallhaven"}
	wp.fetchFromProvider(context.Background(), q, cp, false, &mu, map[string]bool{}, util.NewSafeInt())
	assert.ElementsMatch(t, []string{"Wallhaven_small", "Wallhaven_big", "Wallhaven_unknown"}, submittedIDs(sub))
}

func TestFetch_PageOfOnlySkippedImagesIsNotEndOfResults(t *testing.T) {
	wp, sub, cp := newSizeFetchPlugin(t, MinImageSizeDisplay)
	cp.MockImageProvider.ExpectedCalls = nil
	cp.MockImageProvider.On("ID").Return("Wallhaven")
	cp.MockImageProvider.On("Type").Return(provider.TypeCommunity)
	cp.MockImageProvider.On("FetchImages", mock.Anything, "http://mock.url", 3).Return([]provider.Image{
		{ID: "s1", Path: "http://example.invalid/s1.jpg", Width: 1280, Height: 720},
		{ID: "s2", Path: "http://example.invalid/s2.jpg", Width: 1920, Height: 1080},
	}, nil)
	wp.queryPages = map[string]*util.SafeCounter{"q1": util.NewSafeIntWithValue(3)}
	var mu sync.Mutex
	q := ImageQuery{ID: "q1", URL: "http://mock.url", Active: true, Provider: "Wallhaven"}
	wp.fetchFromProvider(context.Background(), q, cp, false, &mu, map[string]bool{}, util.NewSafeInt())
	assert.Empty(t, submittedIDs(sub))
	assert.Equal(t, 4, wp.queryPages["q1"].Value(), "pagination advances; it must not wrap back to page 1")
}

func TestMinResolutionFloor(t *testing.T) {
	wp, _ := newCapPlugin(t, 50)
	mos := new(MockOS)
	mos.On("GetMonitors").Return([]Monitor{
		{ID: 0, Rect: image.Rect(0, 0, 3440, 1440)},
		{ID: 1, Rect: image.Rect(0, 0, 1920, 1080)},
	}, nil)
	wp.os = mos

	wp.cfg.SetMinImageSize(MinImageSizeDisplay)
	w, h, ok := wp.minResolutionFloor()
	assert.True(t, ok)
	assert.Equal(t, [2]int{1920, 1080}, [2]int{w, h}, "the smaller display sets the floor so nothing it could use is excluded")

	wp.cfg.SetMinImageSize(MinImageSizeSlight)
	w, h, ok = wp.minResolutionFloor()
	assert.True(t, ok)
	assert.Equal(t, [2]int{1536, 864}, [2]int{w, h}, "80% of 1920x1080")

	wp.cfg.SetMinImageSize(MinImageSizeAny)
	_, _, ok = wp.minResolutionFloor()
	assert.False(t, ok, "Any size adds no floor")
}

// --- After download: the decoded size decides -------------------------------------------------

func newDecodePlugin(t *testing.T, size MinImageSize, imgW, imgH int) (*Plugin, *httptest.Server) {
	t.Helper()
	wp, _ := newCapPlugin(t, 50)
	wp.cfg.SetMinImageSize(size)
	wp.cfg.SetSmartFitMode(SmartFitOff)

	src := image.NewRGBA(image.Rect(0, 0, imgW, imgH))
	for y := 0; y < imgH; y++ {
		for x := 0; x < imgW; x++ {
			src.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	srcPath := filepath.Join(t.TempDir(), "src.jpg")
	require.NoError(t, imaging.Save(src, srcPath))
	ts := httptest.NewServer(http.FileServer(http.Dir(filepath.Dir(srcPath))))
	t.Cleanup(ts.Close)

	mos := new(MockOS)
	mos.On("GetMonitors").Return([]Monitor{{ID: 0, Rect: image.Rect(0, 0, 640, 360)}}, nil)
	mos.On("GetDesktopDimension").Return(640, 360, nil)
	wp.os = mos
	wp.httpClient = ts.Client()
	wp.imgProcessor = NewVirtualFramer(NewSmartImageProcessor(mos, wp.cfg, nil), wp.cfg)
	return wp, ts
}

func TestProcessImageJob_UnknownSizeIsRejectedAfterDecode(t *testing.T) {
	wp, ts := newDecodePlugin(t, MinImageSizeDisplay, 320, 180) // half the 640x360 display
	job := DownloadJob{Ctx: context.Background(), Image: provider.Image{ID: "Local_tiny", Path: ts.URL + "/src.jpg", Provider: "Local"}}

	out, err := wp.ProcessImageJob(context.Background(), job)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errIncompatibleAll), "no dimensions were known before the download, so the decoded size rejects it")
	assert.True(t, out.ProcessingFlags["incompatible:640x360"], "tagged for the display, like a shape misfit")
	assert.Equal(t, 320, out.Width, "the probed size is kept on the record (Golden Rule 2)")
	assert.Equal(t, 180, out.Height)
	assert.True(t, out.ProcessingFlags["MinSizeDisplay"], "the rejection is stamped with the settings it was decided under")
}

func TestProcessImageJob_SameImageIsAdmittedWhenSizeIsAny(t *testing.T) {
	wp, ts := newDecodePlugin(t, MinImageSizeAny, 320, 180)
	job := DownloadJob{Ctx: context.Background(), Image: provider.Image{ID: "Local_tiny", Path: ts.URL + "/src.jpg", Provider: "Local"}}

	out, err := wp.ProcessImageJob(context.Background(), job)
	require.NoError(t, err)
	assert.NotEmpty(t, out.DerivativePaths)
	assert.False(t, out.ProcessingFlags["incompatible:640x360"])
}

func TestProcessImageJob_DisplaySizedImageIsAdmitted(t *testing.T) {
	wp, ts := newDecodePlugin(t, MinImageSizeDisplay, 800, 450) // larger than the display
	job := DownloadJob{Ctx: context.Background(), Image: provider.Image{ID: "Local_big", Path: ts.URL + "/src.jpg", Provider: "Local"}}
	out, err := wp.ProcessImageJob(context.Background(), job)
	require.NoError(t, err)
	assert.NotEmpty(t, out.DerivativePaths)
}
