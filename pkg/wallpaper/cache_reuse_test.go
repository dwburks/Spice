package wallpaper

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dixieflatline76/Spice/v2/pkg/provider"
	"github.com/dixieflatline76/Spice/v2/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// --- helpers -----------------------------------------------------------------

// isolateUserCache points os.UserCacheDir at a temp dir on every OS so code that
// persists next to the real app cache (query_pages.json) stays inside the test.
func isolateUserCache(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)           // macOS: $HOME/Library/Caches
	t.Setenv("XDG_CACHE_HOME", dir) // Linux
	t.Setenv("LocalAppData", dir)   // Windows
}

// countingProvider counts EnrichImage calls (the per-image provider API call).
type countingProvider struct {
	*MockImageProvider
	enrichCalls atomic.Int32
}

func (c *countingProvider) EnrichImage(_ context.Context, img provider.Image) (provider.Image, error) {
	c.enrichCalls.Add(1)
	img.Attribution = "artist-" + img.ID
	return img, nil
}

// roundTripFunc is a hermetic http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// countingBody records how many bytes the client read and whether it was closed.
type countingBody struct {
	r      io.Reader
	read   atomic.Int64
	closed atomic.Bool
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.read.Add(int64(n))
	return n, err
}

func (b *countingBody) Close() error { b.closed.Store(true); return nil }

func tinyJPEG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte{'x'}, size), 0644))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func newRetentionStore(t *testing.T) (*ImageStore, *FileManager, string) {
	t.Helper()
	dir := t.TempDir()
	fm := NewFileManager(dir)
	require.NoError(t, fm.EnsureDirs())
	store := NewImageStore()
	store.SetAsyncSave(false)
	store.SetFileManager(fm, filepath.Join(dir, "cache.json"))
	// Drain async file work before the temp dir is removed (cleanups run LIFO).
	t.Cleanup(func() { fm.WaitTimeout(2 * time.Second) })
	return store, fm, dir
}

// addWithMaster adds an active image whose master file (size bytes) is on disk.
func addWithMaster(t *testing.T, store *ImageStore, fm *FileManager, id, queryID string, size int) string {
	t.Helper()
	master, err := fm.GetMasterPath(id, ".jpg")
	require.NoError(t, err)
	writeFile(t, master, size)
	require.True(t, store.Add(provider.Image{
		ID:            id,
		Path:          "http://example.invalid/" + id + ".jpg",
		SourceQueryID: queryID,
		Attribution:   "artist-" + id,
		Provider:      "Wallhaven",
		Width:         3840,
		Height:        2160,
	}))
	return master
}

func setLastUsed(store *ImageStore, id string, ts time.Time) {
	store.mu.Lock()
	defer store.mu.Unlock()
	e := store.retained[id]
	e.LastUsed = ts
	store.retained[id] = e
}

// --- 1. disable -> re-enable re-admits from disk with zero network ------------

func TestCacheReuse_DisableReenable_ZeroNetwork(t *testing.T) {
	isolateUserCache(t)
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	cfg.SetSmartFitMode(SmartFitNormal)
	cfg.Queries = []ImageQuery{}
	qid, err := cfg.AddImageQuery("Reuse", "http://mock.url", true)
	require.NoError(t, err)

	jpg := tinyJPEG(t)
	var imageHostRequests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imageHostRequests.Add(1)
		_, _ = w.Write(jpg)
	}))
	defer ts.Close()

	mp := new(MockImageProvider)
	mp.On("ID").Return("Wallhaven")
	mp.On("Name").Return("Wallhaven")
	var page1 []provider.Image
	for _, id := range []string{"a1", "a2", "a3"} {
		// No dimensions from the "API": Golden Rule 2 requires probing + persisting them.
		page1 = append(page1, provider.Image{ID: id, Path: ts.URL + "/" + id + ".jpg", Provider: "Wallhaven"})
	}
	mp.On("FetchImages", mock.Anything, "http://mock.url", 1).Return(page1, nil)
	mp.On("FetchImages", mock.Anything, "http://mock.url", mock.Anything).Return([]provider.Image{}, nil)
	cp := &countingProvider{MockImageProvider: mp}

	mockOS := new(MockOS)
	mockOS.On("GetDesktopDimension").Return(1920, 1080, nil)
	mockOS.On("GetMonitors").Return([]Monitor{{ID: 0, Name: "Primary", Rect: image.Rect(0, 0, 1920, 1080)}}, nil)
	mockIP := new(MockImageProcessor)
	mockIP.On("CheckCompatibility", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockIP.On("FitImage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(image.NewRGBA(image.Rect(0, 0, 4, 3)), nil)
	mockPM := new(MockPluginManager)
	mockPM.On("NotifyUser", mock.Anything, mock.Anything).Return()

	dir := t.TempDir()
	wp := &Plugin{
		os:                 mockOS,
		imgProcessor:       mockIP,
		cfg:                cfg,
		httpClient:         ts.Client(),
		manager:            mockPM,
		downloadedDir:      dir,
		interrupt:          util.NewSafeBool(),
		queryPages:         make(map[string]*util.SafeCounter),
		fitImageFlag:       util.NewSafeBool(),
		shuffleImageFlag:   util.NewSafeBool(),
		fetchingInProgress: util.NewSafeBool(),
		providers:          map[string]provider.ImageProvider{"Wallhaven": cp},
		store:              NewImageStore(),
	}
	store := wp.store.(*ImageStore)
	store.SetAsyncSave(false)
	wp.fm = NewFileManager(dir)
	require.NoError(t, wp.fm.EnsureDirs())
	store.SetFileManager(wp.fm, filepath.Join(dir, "cache.json"))
	wp.pipeline = NewPipeline(context.Background(), cfg, store, wp.ProcessImageJob, nil, nil)
	wp.jobSubmitter = wp.pipeline
	wp.pipeline.Start(2)
	defer wp.pipeline.Stop()

	// Initial fetch: 3 downloads, 3 enrichments.
	wp.FetchNewImages(false)
	require.Eventually(t, func() bool { return store.Count() == 3 }, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, int32(3), imageHostRequests.Load())
	require.Equal(t, int32(3), cp.enrichCalls.Load())

	// Disable: out of rotation, masters retained, derivatives cleaned by maintenance.
	require.NoError(t, cfg.DisableImageQuery(qid))
	wp.onQueryDisabled(qid)
	require.Eventually(t, func() bool { return store.Count() == 0 && store.RetainedCount() == 3 }, 5*time.Second, 20*time.Millisecond)
	require.True(t, wp.fm.WaitTimeout(5*time.Second))
	for _, raw := range []string{"a1", "a2", "a3"} {
		id := "Wallhaven_" + raw
		e, ok := store.GetRetained(id)
		require.True(t, ok, id)
		assert.True(t, fileExists(filepath.Join(dir, id+".jpg")), "master kept for %s", id)
		assert.True(t, e.DerivativesCleared, "derivatives of retained %s are deleted", id)
		assert.Positive(t, e.Bytes)
		assert.Equal(t, "artist-"+raw, e.Image.Attribution, "enriched metadata retained")
	}

	// Re-enable: re-admitted straight from disk.
	require.NoError(t, cfg.EnableImageQuery(qid))
	wp.onQueryEnabled(qid)
	require.Eventually(t, func() bool { return store.Count() == 3 }, 5*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool { return !wp.fetchingInProgress.Value() }, 5*time.Second, 20*time.Millisecond)

	assert.Equal(t, int32(3), imageHostRequests.Load(), "re-admission must not touch the image host")
	assert.Equal(t, int32(3), cp.enrichCalls.Load(), "re-admission must not call EnrichImage")
	assert.Equal(t, 0, store.RetainedCount(), "re-admitted images move back to active")
	for _, img := range store.List() {
		assert.Equal(t, "artist-"+strings.TrimPrefix(img.ID, "Wallhaven_"), img.Attribution)
		assert.Equal(t, qid, img.SourceQueryID)
		assert.Positive(t, img.Width, "Golden Rule 2: dimensions persisted")
		assert.Positive(t, img.Height, "Golden Rule 2: dimensions persisted")
		require.NotEmpty(t, img.DerivativePaths, "Golden Rule 3: derivatives regenerated locally")
		for res, p := range img.DerivativePaths {
			assert.True(t, fileExists(p), "derivative %s for %s exists", res, img.ID)
		}
	}
}

// Backlog healing for an active image: master on disk + metadata in the store means the
// derivative is regenerated locally without enrichment or download (Golden Rule 3).
func TestCacheReuse_BacklogHealingSkipsEnrichAndDownload(t *testing.T) {
	isolateUserCache(t)
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	cfg.SetSmartFitMode(SmartFitNormal)

	var requests atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("network disabled in test")
	})}
	mockOS := new(MockOS)
	mockOS.On("GetDesktopDimension").Return(1920, 1080, nil)
	mockOS.On("GetMonitors").Return([]Monitor{{ID: 0, Name: "Primary", Rect: image.Rect(0, 0, 1920, 1080)}}, nil)
	mockIP := new(MockImageProcessor)
	mockIP.On("CheckCompatibility", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockIP.On("FitImage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(image.NewRGBA(image.Rect(0, 0, 4, 3)), nil)

	store, fm, dir := newRetentionStore(t)
	wp := &Plugin{os: mockOS, imgProcessor: mockIP, cfg: cfg, httpClient: client, store: store, fm: fm}

	id := "Wallhaven_heal1"
	master := filepath.Join(dir, id+".jpg")
	require.NoError(t, os.WriteFile(master, tinyJPEG(t), 0644))
	require.True(t, store.Add(provider.Image{ID: id, Path: "http://example.invalid/heal1.jpg", Attribution: "known", Provider: "Wallhaven", Enriched: true}))

	cp := &countingProvider{MockImageProvider: new(MockImageProvider)}
	cp.MockImageProvider.On("ID").Return("Wallhaven")
	out, err := wp.ProcessImageJob(context.Background(), DownloadJob{
		Ctx:      context.Background(),
		Image:    provider.Image{ID: id, Path: "http://example.invalid/heal1.jpg", Provider: "Wallhaven", SourceQueryID: "q"},
		Provider: cp,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(0), requests.Load())
	assert.Equal(t, int32(0), cp.enrichCalls.Load())
	assert.Equal(t, "known", out.Attribution)
	assert.Equal(t, 4, out.Width)
	assert.Equal(t, 3, out.Height)
	assert.NotEmpty(t, out.DerivativePaths)
}

// --- 2. query delete / URL change keep masters as (detached) retained entries ---

func TestCacheReuse_QueryDeleteAndURLChangeKeepMasters(t *testing.T) {
	isolateUserCache(t)
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	store, fm, _ := newRetentionStore(t)
	wp := &Plugin{cfg: cfg, store: store, fm: fm, queryPages: make(map[string]*util.SafeCounter)}

	delMaster := addWithMaster(t, store, fm, "Wallhaven_del1", "qDel", 10)
	urlMaster := addWithMaster(t, store, fm, "Wallhaven_url1", "qURL", 10)
	keepMaster := addWithMaster(t, store, fm, "Wallhaven_keep1", "qKeep", 10)

	wp.onQueryRemoved("qDel")
	wp.onQueryURLChanged("qURL", "qURL2") // new ID unknown to cfg -> treated as inactive, no fetch
	require.True(t, fm.WaitTimeout(5*time.Second))

	assert.Equal(t, 1, store.Count())
	assert.True(t, store.Exists("Wallhaven_keep1"))
	for _, tc := range []struct{ id, master string }{{"Wallhaven_del1", delMaster}, {"Wallhaven_url1", urlMaster}} {
		assert.True(t, fileExists(tc.master), "%s master kept on disk", tc.id)
		e, ok := store.GetRetained(tc.id)
		require.True(t, ok, "%s retained", tc.id)
		assert.Empty(t, e.Image.SourceQueryID, "%s detached from its query", tc.id)
		assert.Equal(t, "artist-"+tc.id, e.Image.Attribution)
	}
	assert.Empty(t, store.RetainedForQuery("qDel", 0), "deleted query must not resurrect images")
	assert.Empty(t, store.RetainedForQuery("qURL", 0), "old URL's images must not come back on re-enable")
	assert.True(t, fileExists(keepMaster))
}

// --- 3. eviction to the disk budget --------------------------------------------

func TestCacheReuse_EvictionLRUNeverActive(t *testing.T) {
	store, fm, dir := newRetentionStore(t)

	activeMaster := addWithMaster(t, store, fm, "act1", "qA", 100)
	r1 := addWithMaster(t, store, fm, "old1", "qR", 100)
	r2 := addWithMaster(t, store, fm, "mid1", "qR", 100)
	r3 := addWithMaster(t, store, fm, "new1", "qR", 100)
	// Derivative of a retained image (deleted on eviction with its master).
	r1Deriv := filepath.Join(dir, FittedRootDir, QualityDir, StandardDir, "1920x1080", "old1.jpg")
	writeFile(t, r1Deriv, 5)
	// Unknown file: neither active nor retained.
	unknown := filepath.Join(dir, "stranger.jpg")
	writeFile(t, unknown, 50)

	store.RemoveByQueryID("qR")
	require.Equal(t, 3, store.RetainedCount())
	base := time.Now().Add(-time.Hour)
	setLastUsed(store, "old1", base)
	setLastUsed(store, "mid1", base.Add(time.Minute))
	setLastUsed(store, "new1", base.Add(2*time.Minute))

	// Orphan cleanup keeps active + retained, removes the unknown file.
	fm.CleanupOrphans(store.KnownFileIDs())
	assert.False(t, fileExists(unknown), "unknown file removed")
	assert.True(t, fileExists(r1) && fileExists(r2) && fileExists(r3) && fileExists(activeMaster))

	// 400 bytes on disk, budget 250: evict old1 then mid1 (LRU), keep new1 and active.
	res := store.EnforceDiskBudget(250, nil)
	assert.ElementsMatch(t, []string{"old1", "mid1"}, res.EvictedIDs)
	assert.Equal(t, int64(200), res.TotalBytes)
	assert.False(t, fileExists(r1))
	assert.False(t, fileExists(r1Deriv), "derivatives go with an evicted master")
	assert.False(t, fileExists(r2))
	assert.True(t, fileExists(r3))
	assert.True(t, fileExists(activeMaster))
	_, ok := store.GetRetained("old1")
	assert.False(t, ok)

	// Budget 0: every retained entry goes, active never does.
	res = store.EnforceDiskBudget(0, nil)
	assert.Equal(t, []string{"new1"}, res.EvictedIDs)
	assert.True(t, fileExists(activeMaster), "active images are never evicted")
	assert.True(t, store.Exists("act1"))
	assert.Equal(t, 0, store.RetainedCount())
}

func TestCacheReuse_EvictionSkipsDisplayedAndRetriesFailures(t *testing.T) {
	store, fm, _ := newRetentionStore(t)
	shown := addWithMaster(t, store, fm, "shown1", "qR", 100)
	locked := addWithMaster(t, store, fm, "locked1", "qR", 100)
	store.RemoveByQueryID("qR")
	setLastUsed(store, "shown1", time.Now().Add(-2*time.Hour))
	setLastUsed(store, "locked1", time.Now().Add(-time.Hour))

	// Simulate a file held open (Windows) without relying on OS delete semantics.
	fm.removeFn = func(path string) error {
		if strings.HasPrefix(filepath.Base(path), "locked1") {
			return errors.New("simulated: file in use")
		}
		return os.Remove(path)
	}

	res := store.EnforceDiskBudget(0, map[string]bool{"shown1": true})
	assert.Empty(t, res.EvictedIDs)
	assert.Equal(t, []string{"locked1"}, res.FailedIDs)
	assert.True(t, fileExists(shown), "displayed image is never evicted")
	assert.True(t, fileExists(locked))
	_, ok := store.GetRetained("locked1")
	assert.True(t, ok, "failed delete keeps the index entry (index matches disk)")

	// Next pass, file released: eviction completes.
	fm.removeFn = nil
	res = store.EnforceDiskBudget(0, map[string]bool{"shown1": true})
	assert.Equal(t, []string{"locked1"}, res.EvictedIDs)
	assert.False(t, fileExists(locked))
	_, ok = store.GetRetained("locked1")
	assert.False(t, ok)
	_, ok = store.GetRetained("shown1")
	assert.True(t, ok)
}

// Case handling is decided on strings, so it behaves the same on case-sensitive (Linux)
// and case-insensitive (Windows/macOS) filesystems.
func TestCacheReuse_CaseVariantsNeverDeleteActiveFile(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	activeMaster := addWithMaster(t, store, fm, "Wallhaven_AbC", "qA", 100)

	// A retained entry whose ID differs only by case (no file of its own is created:
	// on a case-insensitive filesystem it would be the active image's file).
	store.mu.Lock()
	store.retained["Wallhaven_abc"] = RetainedEntry{Image: provider.Image{ID: "Wallhaven_abc"}, Ext: ".jpg", Bytes: 100, LastUsed: time.Now()}
	store.mu.Unlock()

	fm.CleanupOrphans(store.KnownFileIDs())
	store.EnforceDiskBudget(0, nil)
	assert.True(t, fileExists(activeMaster), "active master must survive on every OS")

	// Orphan cleanup treats a case variant of a known ID as known.
	variant := filepath.Join(dir, "stray_case.jpg")
	writeFile(t, variant, 1)
	fm.CleanupOrphans(map[string]bool{"STRAY_CASE": true})
	assert.True(t, fileExists(variant))
}

// --- 4. retained index persists across reload ----------------------------------

func TestCacheReuse_RetainedIndexPersists(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	addWithMaster(t, store, fm, "Wallhaven_keep1", "qA", 10)
	addWithMaster(t, store, fm, "Wallhaven_gone1", "qR", 42)
	store.SetTuningOptions("Wallhaven_gone1", "1920x1080", provider.TuningOptions{Anchor: provider.AnchorTopLeft})
	store.RemoveByQueryID("qR")
	want, ok := store.GetRetained("Wallhaven_gone1")
	require.True(t, ok)
	store.SaveCache()
	assert.True(t, fileExists(filepath.Join(dir, cacheIndexFileName)))

	reloaded := NewImageStore()
	reloaded.SetAsyncSave(false)
	reloaded.SetFileManager(fm, filepath.Join(dir, "cache.json"))
	require.NoError(t, reloaded.LoadCache())

	assert.Equal(t, 1, reloaded.Count())
	got, ok := reloaded.GetRetained("Wallhaven_gone1")
	require.True(t, ok)
	assert.Equal(t, want.Ext, got.Ext)
	assert.Equal(t, int64(42), got.Bytes)
	assert.True(t, want.LastUsed.Equal(got.LastUsed))
	assert.Equal(t, "Wallhaven", got.Provider)
	assert.Equal(t, "qR", got.Image.SourceQueryID)
	assert.Equal(t, "artist-Wallhaven_gone1", got.Image.Attribution)
	assert.Equal(t, 3840, got.Image.Width)
	assert.Equal(t, provider.AnchorTopLeft, got.Image.GetTuning("1920x1080").Anchor, "user tuning survives retention")
	assert.Equal(t, 1, len(reloaded.RetainedForQuery("qR", 0)))
}

// --- 5. download size cap ------------------------------------------------------

func newCapPlugin(t *testing.T, capMB int) (*Plugin, string) {
	t.Helper()
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	cfg.SetMaxDownloadSizeMB(capMB)
	dir := t.TempDir()
	fm := NewFileManager(dir)
	require.NoError(t, fm.EnsureDirs())
	store := NewImageStore()
	store.SetAsyncSave(false)
	store.SetFileManager(fm, filepath.Join(dir, "cache.json"))
	return &Plugin{cfg: cfg, fm: fm, store: store}, dir
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestDownloadCap_ContentLengthOverCapAbortsWithoutReading(t *testing.T) {
	wp, dir := newCapPlugin(t, 1)
	capBytes := wp.maxDownloadBytes()
	body := &countingBody{r: bytes.NewReader(make([]byte, 1024))}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: capBytes + 1, Body: body, Header: http.Header{}, Request: r}, nil
	})}
	master := filepath.Join(dir, "Wallhaven_big1.jpg")
	_, err := wp.downloadMasterFile(context.Background(), client, "http://example.invalid/big1.jpg", master, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDownloadTooLarge))
	assert.Equal(t, int64(0), body.read.Load(), "body must not be read")
	assert.True(t, body.closed.Load())
	assert.False(t, fileExists(master))
	assert.Empty(t, dirEntries(t, dir), "no partial or temp file left")
}

func TestDownloadCap_StreamOverCapAbortsAndRemovesPartial(t *testing.T) {
	wp, dir := newCapPlugin(t, 1)
	capBytes := wp.maxDownloadBytes()
	body := &countingBody{r: bytes.NewReader(make([]byte, capBytes+4096))}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Body: body, Header: http.Header{}, Request: r}, nil
	})}
	master := filepath.Join(dir, "Wallhaven_big2.jpg")
	_, err := wp.downloadMasterFile(context.Background(), client, "http://example.invalid/big2.jpg", master, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDownloadTooLarge))
	assert.LessOrEqual(t, body.read.Load(), capBytes+1, "reads stop at cap+1")
	assert.False(t, fileExists(master))
	assert.Empty(t, dirEntries(t, dir), "partial temp file removed")

	// Under the cap: written via temp file and renamed into place.
	small := &countingBody{r: bytes.NewReader([]byte("ok"))}
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Body: small, Header: http.Header{}, Request: r}, nil
	})
	got, err := wp.downloadMasterFile(context.Background(), client, "http://example.invalid/ok.jpg", master, nil)
	require.NoError(t, err)
	assert.Equal(t, master, got)
	assert.Equal(t, []string{"Wallhaven_big2.jpg"}, dirEntries(t, dir))
}

func TestDownloadCap_ReportedFileSizeSkippedBeforeAnyRequest(t *testing.T) {
	isolateUserCache(t)
	wp, _ := newCapPlugin(t, 1)
	var requests atomic.Int32
	wp.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("network disabled in test")
	})}
	cp := &countingProvider{MockImageProvider: new(MockImageProvider)}
	cp.MockImageProvider.On("ID").Return("Wallhaven")

	// Pipeline level: oversized job fails fast, no enrich, no download.
	big := provider.Image{ID: "Wallhaven_huge", Path: "http://example.invalid/huge.jpg", Provider: "Wallhaven", FileSize: wp.maxDownloadBytes() + 1}
	out, err := wp.ProcessImageJob(context.Background(), DownloadJob{Ctx: context.Background(), Image: big, Provider: cp})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDownloadTooLarge))
	assert.Empty(t, out.ID, "no zombie store entry for a skipped image")
	assert.Equal(t, int32(0), requests.Load())
	assert.Equal(t, int32(0), cp.enrichCalls.Load())

	// Fetch level: skipped before submission; the page still counts (no page wrap).
	mp := cp.MockImageProvider
	mp.On("FetchImages", mock.Anything, "http://mock.url", 1).Return([]provider.Image{
		{ID: "huge", Path: "http://example.invalid/huge.jpg", FileSize: wp.maxDownloadBytes() + 1},
		{ID: "fine", Path: "http://example.invalid/fine.jpg", FileSize: 1024},
	}, nil)
	sub := new(MockPipeline)
	sub.On("Submit", mock.Anything, mock.MatchedBy(func(j DownloadJob) bool { return j.Image.ID == "Wallhaven_fine" })).Return(true)
	wp.jobSubmitter = sub
	wp.queryPages = make(map[string]*util.SafeCounter)

	var mu sync.Mutex
	q := ImageQuery{ID: "q1", URL: "http://mock.url", Active: true, Provider: "Wallhaven"}
	wp.fetchFromProvider(context.Background(), q, cp, false, &mu, map[string]bool{}, util.NewSafeInt())

	sub.AssertNumberOfCalls(t, "Submit", 1)
	assert.Equal(t, 2, wp.queryPages["q1"].Value(), "oversized skip must not trigger page wrap")
	assert.Equal(t, int32(0), requests.Load())
}
