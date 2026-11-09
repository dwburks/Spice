package wallpaper

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dixieflatline76/Spice/v2/pkg/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// --- Processing flags: one source of truth ------------------------------------------

// A freshly downloaded image must not be invalidated by the next Sync when no setting
// changed (it was, on every startup / Apply / nightly run, since v2.6.0).
func TestRepro_NewDownloadSurvivesNextSync(t *testing.T) {
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	cfg.SetSmartFitMode(SmartFitNormal)
	cfg.Queries = []ImageQuery{}
	qid, err := cfg.AddImageQuery("Repro", "http://mock.url", true)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 3)), nil))
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(buf.Bytes()) }))
	defer ts.Close()

	dir := t.TempDir()
	fm := NewFileManager(dir)
	require.NoError(t, fm.EnsureDirs())
	store := NewImageStore()
	store.SetAsyncSave(false)
	store.SetFileManager(fm, filepath.Join(dir, "cache.json"))

	mockOS := new(MockOS)
	mockOS.On("GetMonitors").Return([]Monitor{{ID: 0, Rect: image.Rect(0, 0, 1920, 1080)}}, nil)
	mockOS.On("GetDesktopDimension").Return(1920, 1080, nil)
	mockIP := new(MockImageProcessor)
	mockIP.On("CheckCompatibility", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockIP.On("FitImage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(image.NewRGBA(image.Rect(0, 0, 4, 3)), nil)
	wp := &Plugin{cfg: cfg, store: store, fm: fm, os: mockOS, imgProcessor: mockIP, httpClient: ts.Client()}

	img, err2 := wp.ProcessImageJob(context.Background(), DownloadJob{Ctx: context.Background(),
		Image: provider.Image{ID: "Wallhaven_new", Path: ts.URL + "/new.jpg", SourceQueryID: qid}})
	require.NoError(t, err2)
	require.NotEmpty(t, img.DerivativePaths)
	require.True(t, store.Add(img))

	wp.syncStoreWithConfig() // e.g. the next startup, Apply Changes or nightly run; settings unchanged
	got, ok := store.GetByID("Wallhaven_new")
	require.True(t, ok, "image left the store")
	require.NotEmpty(t, got.DerivativePaths, "unchanged settings must not invalidate a fresh download (flags %v)", got.ProcessingFlags)
}

func TestStampProcessingFlags_FullSetAndStaleFrameSizeDropped(t *testing.T) {
	wp := flagFixturePlugin(true, SmartFitAggressive, true, false)
	img := provider.Image{ProcessingFlags: map[string]bool{"FrameSize_0.80": true, "incompatible:800x600": true, "VirtualFramed:1920x1080": true}}
	wp.stampProcessingFlags(&img)
	for k, v := range wp.processingFlags() {
		assert.Equal(t, v, img.ProcessingFlags[k], k)
	}
	assert.NotContains(t, img.ProcessingFlags, "FrameSize_0.80", "stale frame-size key removed")
	assert.True(t, img.ProcessingFlags["incompatible:800x600"], "per-image tags kept")
	assert.True(t, img.ProcessingFlags["VirtualFramed:1920x1080"])
	assert.True(t, (&ImageStore{}).flagsMatch(img, wp.processingFlags()))
}

// A virtually framed image carries a "VirtualFramed:<res>" tag; it is per-image metadata and
// must not make every Sync invalidate the image (which repeated forever once re-fit).
func TestSync_VirtualFramedImageNotInvalidated(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	addWithMaster(t, store, fm, "framed", "", 10)
	deriv := filepath.Join(dir, FittedRootDir, QualityDir, StandardDir, "1920x1080", "framed.jpg")
	writeFile(t, deriv, 10)
	wp := flagFixturePlugin(true, SmartFitNormal, false, false)
	img, _ := store.GetByID("framed")
	wp.stampProcessingFlags(&img)
	img.ProcessingFlags["VirtualFramed:1920x1080"] = true
	img.DerivativePaths = map[string]string{"1920x1080": deriv}
	require.True(t, store.replace(img))

	store.Sync(100, wp.processingFlags(), nil)
	require.True(t, fm.WaitTimeout(2*time.Second))
	got, _ := store.GetByID("framed")
	assert.Equal(t, deriv, got.DerivativePaths["1920x1080"])
	assert.FileExists(t, deriv)
}

// ClearDerivatives (a derivative file went missing) used to wipe ProcessingFlags, so the
// next Sync "invalidated" the image and dropped its incompatibility tags.
func TestClearDerivatives_KeepsSettingsFlagsForNextSync(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	addWithMaster(t, store, fm, "lost", "", 10)
	wp := flagFixturePlugin(true, SmartFitNormal, false, false)
	img, _ := store.GetByID("lost")
	wp.stampProcessingFlags(&img)
	img.ProcessingFlags["incompatible:2560x1440"] = true
	img.ProcessingFlags["VirtualFramed:1920x1080"] = true
	img.DerivativePaths = map[string]string{"1920x1080": filepath.Join(dir, "gone.jpg")}
	require.True(t, store.replace(img))

	require.True(t, store.ClearDerivatives("lost"))
	got, _ := store.GetByID("lost")
	assert.NotContains(t, got.ProcessingFlags, "VirtualFramed:1920x1080", "per-derivative tag dropped with the derivative")
	assert.True(t, store.flagsMatch(got, wp.processingFlags()), "still matches the current settings")

	store.Sync(100, wp.processingFlags(), nil)
	got, _ = store.GetByID("lost")
	assert.True(t, got.ProcessingFlags["incompatible:2560x1440"], "not invalidated: rejection tag survives Sync")
}

func TestReuseOnly_NeverEnriches(t *testing.T) {
	wp, store, _, dir := reuseTestPlugin(t, nil)
	id := "Wallhaven_unenriched"
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".jpg"), tinyJPEG(t), 0644))
	require.True(t, store.Add(provider.Image{ID: id, Path: "http://example.invalid/u.jpg", Provider: "Wallhaven"})) // Enriched=false
	cp := &countingProvider{MockImageProvider: new(MockImageProvider)}
	cp.MockImageProvider.On("ID").Return("Wallhaven")
	img, _ := store.GetByID(id)
	_, err := wp.ProcessImageJob(context.Background(), DownloadJob{Ctx: context.Background(), Image: img, Provider: cp, ReuseOnly: true})
	require.NoError(t, err)
	assert.Equal(t, int32(0), cp.enrichCalls.Load(), "ReuseOnly jobs are network-free even if a provider is attached")
}

// --- Startup restore of dormant images -------------------------------------------

type pacedCountingProvider struct {
	*countingProvider
	pace time.Duration
}

func (p *pacedCountingProvider) GetAPIPacing() time.Duration     { return 0 }
func (p *pacedCountingProvider) GetProcessPacing() time.Duration { return p.pace }

type restoreFixture struct {
	wp       *Plugin
	store    *ImageStore
	dir      string
	requests *atomic.Int32
	enrich   *countingProvider
	jobs     *atomic.Int32
}

func newRestoreFixture(t *testing.T, prov provider.ImageProvider, enrich *countingProvider) *restoreFixture {
	t.Helper()
	wp, store, _, dir := reuseTestPlugin(t, nil)
	var requests atomic.Int32
	wp.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("network disabled in test")
	})}
	wp.providers = map[string]provider.ImageProvider{"Wallhaven": prov}
	wp.restorePace = time.Millisecond
	var jobs atomic.Int32
	p := NewPipeline(context.Background(), wp.cfg, store, func(ctx context.Context, job DownloadJob) (provider.Image, error) {
		jobs.Add(1)
		return wp.ProcessImageJob(ctx, job)
	}, nil, nil)
	p.Start(2)
	t.Cleanup(p.Stop)
	wp.pipeline, wp.jobSubmitter = p, p
	return &restoreFixture{wp: wp, store: store, dir: dir, requests: &requests, enrich: enrich, jobs: &jobs}
}

// addDormant adds an image in rotation without derivatives; withMaster controls whether
// its master is cached.
func (f *restoreFixture) addDormant(t *testing.T, id string, withMaster bool, flags map[string]bool) {
	t.Helper()
	if withMaster {
		require.NoError(t, os.WriteFile(filepath.Join(f.dir, id+".jpg"), tinyJPEG(t), 0644))
	}
	require.True(t, f.store.Add(provider.Image{ID: id, Path: "http://example.invalid/" + id + ".jpg", Provider: "Wallhaven", ProcessingFlags: flags}))
}

func TestRestore_DormantImagesRefitLocally(t *testing.T) {
	cp := &countingProvider{MockImageProvider: new(MockImageProvider)}
	cp.MockImageProvider.On("ID").Return("Wallhaven")
	f := newRestoreFixture(t, cp, cp)
	for _, id := range []string{"Wallhaven_d1", "Wallhaven_d2", "Wallhaven_d3"} {
		f.addDormant(t, id, true, nil) // never enriched either: still no enrich call
	}
	f.addDormant(t, "Wallhaven_nomaster", false, nil)
	nofitFlags := f.wp.processingFlags()        // processed under the current settings...
	nofitFlags["incompatible:1920x1080"] = true // ...and found to fit no connected monitor
	f.addDormant(t, "Wallhaven_nofit", true, nofitFlags)

	restored := f.wp.restoreDormantImages(context.Background())
	assert.Equal(t, 3, restored)
	for _, id := range []string{"Wallhaven_d1", "Wallhaven_d2", "Wallhaven_d3"} {
		img, ok := f.store.GetByID(id)
		require.True(t, ok)
		assert.NotEmpty(t, img.DerivativePaths["1920x1080"], "%s back in rotation", id)
		assert.True(t, f.store.flagsMatch(img, f.wp.processingFlags()), "%s carries the full flag set", id)
	}
	assert.Equal(t, int32(3), f.jobs.Load(), "masterless and fits-no-monitor images are not submitted")
	nm, _ := f.store.GetByID("Wallhaven_nomaster")
	assert.Empty(t, nm.DerivativePaths)
	nf, _ := f.store.GetByID("Wallhaven_nofit")
	assert.True(t, nf.ProcessingFlags["incompatible:1920x1080"], "existing incompatible handling untouched")
	assert.Equal(t, int32(0), f.requests.Load(), "no HTTP requests")
	assert.Equal(t, int32(0), cp.enrichCalls.Load(), "no enrich calls")

	// The next Sync keeps them (flags match), and a second startup has nothing to do.
	f.store.Sync(100, f.wp.processingFlags(), nil) // nil: these fixtures belong to no query
	img, _ := f.store.GetByID("Wallhaven_d1")
	assert.NotEmpty(t, img.DerivativePaths)
	assert.Equal(t, 0, f.wp.restoreDormantImages(context.Background()), "second startup does nothing")
	assert.Equal(t, int32(3), f.jobs.Load())
}

func TestRestore_PacedByProviderProcessLimiter(t *testing.T) {
	cp := &countingProvider{MockImageProvider: new(MockImageProvider)}
	cp.MockImageProvider.On("ID").Return("Wallhaven")
	paced := &pacedCountingProvider{countingProvider: cp, pace: 80 * time.Millisecond}
	f := newRestoreFixture(t, paced, cp)
	for _, id := range []string{"Wallhaven_p1", "Wallhaven_p2", "Wallhaven_p3", "Wallhaven_p4"} {
		f.addDormant(t, id, true, nil)
	}
	start := time.Now()
	assert.Equal(t, 4, f.wp.restoreDormantImages(context.Background()))
	assert.GreaterOrEqual(t, time.Since(start), 3*80*time.Millisecond, "jobs spaced by the provider's process pacing")
	assert.Same(t, f.wp.getProcessLimiter(paced), f.wp.restoreLimiter("Wallhaven"), "shares the normal process limiter")
	assert.Equal(t, int32(0), f.requests.Load())
}
