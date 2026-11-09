package wallpaper

// Regression tests for the two independent reviews of feat/cache-reuse.
// TestReview_* are ported from the Codex probe tests; the rest cover the Antigravity findings.

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"net/http"
	"os"
	"path/filepath"
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

// --- Codex #1: eviction / derivative cleanup vs re-admission ----------------------

// Ported from TestReview_EvictionDeletesReadmittedMaster. A re-admission worker owns the ID
// (job lease) from processing through admission, so eviction cannot select it at all; its
// result is then admitted and the master is intact.
func TestReview_EvictionSkipsReadmissionInFlight(t *testing.T) {
	store, fm, _ := newRetentionStore(t)
	master := addWithMaster(t, store, fm, "victim", "q", 100)
	store.RetainByQueryID("q", false)
	entry, ok := store.GetRetained("victim")
	require.True(t, ok)

	require.NoError(t, store.AcquireJobLease(context.Background(), "victim"))
	var removeAttempted atomic.Bool
	fm.removeFn = func(path string) error {
		if path == master {
			removeAttempted.Store(true)
		}
		return os.Remove(path)
	}
	store.EnforceDiskBudget(0, nil)
	// The worker's result reaches stateManagerLoop after the eviction pass.
	require.True(t, store.Add(entry.Image))
	store.ReleaseJobLease("victim")

	_, active := store.GetByID("victim")
	require.True(t, active)
	require.FileExists(t, master, "eviction must not delete a newly active master")
	assert.False(t, removeAttempted.Load(), "eviction must not even attempt to delete a leased ID")

	store.EnforceDiskBudget(0, nil)
	require.FileExists(t, master, "active images are never evicted")
}

// The probe's other interleaving (admission arriving while the deleter already owns the
// files, with no lease) must not produce an active record pointing at a deleted file:
// admission is refused for the duration of the delete claim.
func TestReview_AdmissionRefusedWhileEvictionOwnsFiles(t *testing.T) {
	store, fm, _ := newRetentionStore(t)
	master := addWithMaster(t, store, fm, "victim", "q", 100)
	store.RetainByQueryID("q", false)
	entry, ok := store.GetRetained("victim")
	require.True(t, ok)

	var admitted atomic.Bool
	fm.removeFn = func(path string) error {
		if path == master {
			admitted.Store(store.Add(entry.Image))
		}
		return os.Remove(path)
	}
	store.EnforceDiskBudget(0, nil)
	assert.False(t, admitted.Load(), "Add must be refused while eviction owns the ID")
	_, active := store.GetByID("victim")
	assert.False(t, active)
	assert.NoFileExists(t, master)
	_, retained := store.GetRetained("victim")
	assert.False(t, retained, "index and disk agree: entry and file are both gone")
}

// Ported from TestReview_DerivativeCleanupDeletesReadmittedDerivative.
func TestReview_DerivativeCleanupSkipsReadmissionInFlight(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	addWithMaster(t, store, fm, "victim", "q", 100)
	derivative := filepath.Join(dir, FittedRootDir, "quality", "standard", "1920x1080", "victim.jpg")
	writeFile(t, derivative, 100)
	store.RetainByQueryID("q", false)
	entry, ok := store.GetRetained("victim")
	require.True(t, ok)

	require.NoError(t, store.AcquireJobLease(context.Background(), "victim"))
	store.EnforceDiskBudget(1000, nil)
	// The re-admission job reused the still-existing derivative in ensureDerivative.
	entry.Image.DerivativePaths = map[string]string{"1920x1080": derivative}
	entry.Image.FilePath = derivative
	require.True(t, store.Add(entry.Image))
	store.ReleaseJobLease("victim")

	img, active := store.GetByID("victim")
	require.True(t, active)
	require.Equal(t, derivative, img.DerivativePaths["1920x1080"])
	require.FileExists(t, derivative, "cleanup must not unlink a derivative already back in rotation")
}

func TestReview_DerivativeCleanupRefusesAdmissionMidDelete(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	addWithMaster(t, store, fm, "victim", "q", 100)
	derivative := filepath.Join(dir, FittedRootDir, "quality", "standard", "1920x1080", "victim.jpg")
	writeFile(t, derivative, 100)
	store.RetainByQueryID("q", false)
	entry, _ := store.GetRetained("victim")

	var admitted atomic.Bool
	fm.removeFn = func(path string) error {
		if path == derivative {
			entry.Image.DerivativePaths = map[string]string{"1920x1080": derivative}
			admitted.Store(store.Add(entry.Image))
		}
		return os.Remove(path)
	}
	store.EnforceDiskBudget(1000, nil)
	assert.False(t, admitted.Load(), "no active record may point at a derivative being deleted")
	e, ok := store.GetRetained("victim")
	require.True(t, ok)
	assert.True(t, e.DerivativesCleared)
}

// End to end: the pipeline holds the lease while the job runs and until its result is
// admitted, so a maintenance pass in that window leaves the files alone.
func TestLease_PipelineHoldsLeaseThroughAdmission(t *testing.T) {
	store, fm, _ := newRetentionStore(t)
	master := addWithMaster(t, store, fm, "victim", "q", 100)
	store.RetainByQueryID("q", false)
	entry, _ := store.GetRetained("victim")

	started := make(chan struct{})
	release := make(chan struct{})
	proc := func(ctx context.Context, job DownloadJob) (provider.Image, error) {
		close(started)
		<-release
		img := job.Image
		img.DerivativePaths = map[string]string{"1920x1080": master}
		return img, nil
	}
	ResetConfig()
	p := NewPipeline(context.Background(), GetConfig(NewMockPreferences()), store, proc, nil, nil)
	p.Start(1)
	defer p.Stop()

	require.True(t, p.Submit(context.Background(), DownloadJob{Ctx: context.Background(), Image: entry.Image, ReuseOnly: true}))
	<-started
	assert.True(t, store.IsLeased("victim"))
	res := store.EnforceDiskBudget(0, nil)
	assert.Empty(t, res.EvictedIDs)
	close(release)

	require.Eventually(t, func() bool { return store.Exists("victim") && !store.IsLeased("victim") }, 2*time.Second, 10*time.Millisecond)
	assert.FileExists(t, master)
}

// --- Antigravity #4: duplicate submissions ------------------------------------------

func TestLease_DuplicateJobRejected(t *testing.T) {
	store, _, _ := newRetentionStore(t)
	var calls atomic.Int32
	release := make(chan struct{})
	proc := func(ctx context.Context, job DownloadJob) (provider.Image, error) {
		calls.Add(1)
		<-release
		return job.Image, nil
	}
	ResetConfig()
	p := NewPipeline(context.Background(), GetConfig(NewMockPreferences()), store, proc, nil, nil)
	p.Start(2)
	defer p.Stop()

	job := DownloadJob{Ctx: context.Background(), Image: provider.Image{ID: "dup1"}, ReuseOnly: true}
	require.True(t, p.Submit(context.Background(), job))
	require.Eventually(t, func() bool { return calls.Load() == 1 }, 2*time.Second, 5*time.Millisecond)
	require.True(t, p.Submit(context.Background(), job))
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), calls.Load(), "second job for an in-flight ID must not be processed")
	close(release)
	require.Eventually(t, func() bool { return store.Exists("dup1") && !store.IsLeased("dup1") }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, int32(1), calls.Load())
}

// --- Codex #3: early compatibility rejection ----------------------------------------

// Ported from TestReview_IncompatibleReadmissionDeletesRetainedMaster.
func TestReview_IncompatibleReadmissionKeepsRetainedMaster(t *testing.T) {
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	store, fm, _ := newRetentionStore(t)
	master := addWithMaster(t, store, fm, "victim", "q", 100)
	store.RetainByQueryID("q", false)
	entry, ok := store.GetRetained("victim")
	require.True(t, ok)
	mockOS := new(MockOS)
	mockOS.On("GetMonitors").Return([]Monitor{{ID: 0, Rect: image.Rect(0, 0, 1920, 1080)}}, nil)
	mockIP := new(MockImageProcessor)
	mockIP.On("CheckCompatibility", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(errors.New("incompatible after monitor/settings change"))
	wp := &Plugin{cfg: cfg, store: store, fm: fm, os: mockOS, imgProcessor: mockIP}
	_, err := wp.ProcessImageJob(context.Background(), DownloadJob{Image: entry.Image, ReuseOnly: true})
	require.ErrorContains(t, err, "incompatible")
	_, retained := store.GetRetained("victim")
	require.True(t, retained)
	require.FileExists(t, master, "a rejected local reuse job must preserve its retained master")
}

// --- Codex #2: atomic ownership transfer --------------------------------------------

// Ported from TestReview_ReadmissionSaveFailureLosesBothIndexes (legacy mirror write fails).
// The ID is namespaced: the probe's bare "victim" with Provider "Wallhaven" is renamed to
// "Wallhaven_victim" (files included) by the legacy-ID migration on reload, which would make
// the file assertion fail for a reason unrelated to ownership.
func TestReview_ReadmissionSaveFailureKeepsOwnership(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	master := addWithMaster(t, store, fm, "Wallhaven_victim", "q", 100)
	store.RetainByQueryID("q", false)
	entry, ok := store.GetRetained("Wallhaven_victim")
	require.True(t, ok)
	// Force only the active-cache save to fail after the retained record was committed.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "cache.json.tmp"), 0755))
	require.True(t, store.Add(entry.Image))
	reloaded := NewImageStore()
	reloaded.SetFileManager(fm, filepath.Join(dir, "cache.json"))
	require.NoError(t, reloaded.LoadCache())
	fm.CleanupOrphans(reloaded.KnownFileIDs())
	require.FileExists(t, master, "a failed save must not erase the only durable ownership record")
}

// Same, with the authoritative index write failing: the error propagates and the
// previous index (which still owns the master) is left in place.
func TestReview_IndexSaveFailurePropagatesAndKeepsOwnership(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	master := addWithMaster(t, store, fm, "Wallhaven_victim", "q", 100)
	store.RetainByQueryID("q", false)
	entry, ok := store.GetRetained("Wallhaven_victim")
	require.True(t, ok)
	require.NoError(t, os.Mkdir(filepath.Join(dir, cacheIndexFileName+".tmp"), 0755))
	require.True(t, store.Add(entry.Image))
	require.Error(t, store.SaveCache(), "persistence errors must propagate")

	reloaded := NewImageStore()
	reloaded.SetFileManager(fm, filepath.Join(dir, "cache.json"))
	require.NoError(t, reloaded.LoadCache())
	known := reloaded.KnownFileIDs()
	assert.True(t, known["Wallhaven_victim"], "the master keeps an owner after the failed save")
	fm.CleanupOrphans(known)
	require.FileExists(t, master)
}

// --- Codex #4: corrupt cached master ------------------------------------------------

// Ported from TestReview_CorruptCachedMasterNeverRecovers.
func TestReview_CorruptCachedMasterIsInvalidated(t *testing.T) {
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	cfg.SetSmartFitMode(SmartFitNormal)
	store, fm, _ := newRetentionStore(t)
	// addWithMaster writes non-image bytes, modeling a truncated/corrupted master.
	master := addWithMaster(t, store, fm, "victim", "q", 100)
	img, ok := store.GetByID("victim")
	require.True(t, ok)
	mockOS := new(MockOS)
	mockOS.On("GetMonitors").Return([]Monitor{{ID: 0, Rect: image.Rect(0, 0, 1920, 1080)}}, nil)
	mockIP := new(MockImageProcessor)
	mockIP.On("CheckCompatibility", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	wp := &Plugin{cfg: cfg, store: store, fm: fm, os: mockOS, imgProcessor: mockIP}
	_, err := wp.ProcessImageJob(context.Background(), DownloadJob{Image: img})
	require.ErrorContains(t, err, "failed to open master")
	require.NoFileExists(t, master, "an undecodable cached master must be invalidated so a later fetch can repair it")
}

func TestCorruptRetainedMasterDropsEntry(t *testing.T) {
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	cfg.SetSmartFitMode(SmartFitNormal)
	store, fm, _ := newRetentionStore(t)
	master := addWithMaster(t, store, fm, "victim", "q", 100)
	store.RetainByQueryID("q", false)
	entry, _ := store.GetRetained("victim")
	mockOS := new(MockOS)
	mockOS.On("GetMonitors").Return([]Monitor{{ID: 0, Rect: image.Rect(0, 0, 1920, 1080)}}, nil)
	mockIP := new(MockImageProcessor)
	mockIP.On("CheckCompatibility", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	wp := &Plugin{cfg: cfg, store: store, fm: fm, os: mockOS, imgProcessor: mockIP}
	out, err := wp.ProcessImageJob(context.Background(), DownloadJob{Image: entry.Image, ReuseOnly: true})
	require.Error(t, err)
	assert.Empty(t, out.ID)
	assert.NoFileExists(t, master)
	_, retained := store.GetRetained("victim")
	assert.False(t, retained, "the next fetch must take the normal (paced) download path")
}

// --- Antigravity #3: retained index without the legacy cache file -------------------

func TestLoadCache_RetainedIndexWithoutLegacyCache(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	master := addWithMaster(t, store, fm, "Wallhaven_kept1", "q", 10)
	store.RetainByQueryID("q", false)
	require.NoError(t, store.SaveCache())
	require.NoError(t, os.Remove(filepath.Join(dir, "cache.json")))

	reloaded := NewImageStore()
	reloaded.SetAsyncSave(false)
	reloaded.SetFileManager(fm, filepath.Join(dir, "cache.json"))
	require.NoError(t, reloaded.LoadCache())
	_, ok := reloaded.GetRetained("Wallhaven_kept1")
	require.True(t, ok, "retained index must load even when image_cache_map.json is missing")

	// A following Sync/save must not wipe it, and orphan cleanup keeps the master.
	reloaded.Sync(100, nil, map[string]bool{})
	reloaded.CleanupOrphanFiles(nil)
	assert.FileExists(t, master)
	again := NewImageStore()
	again.SetFileManager(fm, filepath.Join(dir, "cache.json"))
	require.NoError(t, again.LoadCache())
	_, ok = again.GetRetained("Wallhaven_kept1")
	assert.True(t, ok)
}

// --- Antigravity #2: orphan cleanup vs in-flight downloads (pre-existing race) ------

func TestCleanupOrphanFiles_SkipsInFlightJob(t *testing.T) {
	store, _, dir := newRetentionStore(t)
	fresh := filepath.Join(dir, "Wallhaven_fresh.jpg")
	writeFile(t, fresh, 10)
	freshDeriv := filepath.Join(dir, FittedRootDir, QualityDir, StandardDir, "1920x1080", "Wallhaven_fresh.jpg")
	writeFile(t, freshDeriv, 10)
	stranger := filepath.Join(dir, "Wallhaven_stranger.jpg")
	writeFile(t, stranger, 10)

	// A pipeline job is downloading/fitting "fresh" and has not been admitted yet.
	require.NoError(t, store.AcquireJobLease(context.Background(), "Wallhaven_fresh"))
	store.CleanupOrphanFiles(nil)
	assert.FileExists(t, fresh, "in-flight download must survive orphan cleanup")
	assert.FileExists(t, freshDeriv)
	assert.NoFileExists(t, stranger, "true orphans are still removed")

	// Once the job is gone without admission, the files really are orphans.
	store.ReleaseJobLease("Wallhaven_fresh")
	store.CleanupOrphanFiles(nil)
	assert.NoFileExists(t, fresh)
	assert.NoFileExists(t, freshDeriv)
}

// --- Antigravity #4: no provider fetch when re-admission fills the rotation ---------

func runQueryEnabled(t *testing.T, retainedCount int) int32 {
	return runQueryEnabledAdmitting(t, retainedCount, map[bool]int{true: retainedCount, false: 0}[retainedCount >= 100])
}

// runQueryEnabledAdmitting re-enables a query with retainedCount retained images, of which
// the (mock) pipeline admits the first admit, and returns how many provider fetches ran.
func runQueryEnabledAdmitting(t *testing.T, retainedCount, admit int) int32 {
	t.Helper()
	isolateUserCache(t)
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	cfg.SetCacheSize(Cache100Images)
	cfg.Queries = []ImageQuery{}
	qid, err := cfg.AddImageQuery("Fill", "http://mock.url", true)
	require.NoError(t, err)

	store, fm, _ := newRetentionStore(t)
	for i := 0; i < retainedCount; i++ {
		addWithMaster(t, store, fm, "Wallhaven_img"+string(rune('a'+i/26))+string(rune('a'+i%26)), qid, 1)
	}
	store.RetainByQueryID(qid, false)
	require.Equal(t, retainedCount, store.RetainedCount())

	var fetches atomic.Int32
	mp := new(MockImageProvider)
	mp.On("ID").Return("Wallhaven")
	mp.On("FetchImages", mock.Anything, mock.Anything, mock.Anything).Run(func(mock.Arguments) { fetches.Add(1) }).Return([]provider.Image{}, nil)
	var submitted atomic.Int32
	sub := new(MockPipeline)
	sub.On("Submit", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		submitted.Add(1)
		job := args.Get(1).(DownloadJob)
		if int(submitted.Load()) <= admit {
			// Model successful re-admission: the job lands, then signals completion.
			e, _ := store.GetRetained(job.Image.ID)
			store.Add(e.Image)
		}
		job.finish() // the rest finish without being admitted (e.g. incompatible)
	}).Return(true)
	mockPM := new(MockPluginManager)
	mockPM.On("NotifyUser", mock.Anything, mock.Anything).Return()

	wp := &Plugin{
		cfg: cfg, store: store, fm: fm, manager: mockPM, jobSubmitter: sub,
		queryPages:         make(map[string]*util.SafeCounter),
		fetchingInProgress: util.NewSafeBool(),
		interrupt:          util.NewSafeBool(),
		providers:          map[string]provider.ImageProvider{"Wallhaven": mp},
	}
	t.Cleanup(func() { wp.CancelQueryContext(qid) })
	wp.onQueryEnabled(qid)
	require.Eventually(t, func() bool {
		return int(submitted.Load()) >= min(retainedCount, 100)
	}, 2*time.Second, 10*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	return fetches.Load()
}

func TestQueryEnabled_SkipsFetchWhenReadmissionFillsRotation(t *testing.T) {
	assert.Equal(t, int32(0), runQueryEnabled(t, 100), "a full rotation from disk needs no provider fetch")
}

func TestQueryEnabled_FetchesWhenReadmissionIsPartial(t *testing.T) {
	assert.Positive(t, runQueryEnabled(t, 5), "control: a partial re-admission still fetches")
}

// --- Antigravity #5: reused job failures must not corrupt the store -----------------

func reuseTestPlugin(t *testing.T, compat error) (*Plugin, *ImageStore, *FileManager, string) {
	t.Helper()
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	cfg.SetSmartFitMode(SmartFitNormal)
	store, fm, dir := newRetentionStore(t)
	mockOS := new(MockOS)
	mockOS.On("GetMonitors").Return([]Monitor{{ID: 0, Rect: image.Rect(0, 0, 1920, 1080)}}, nil)
	mockOS.On("GetDesktopDimension").Return(1920, 1080, nil)
	mockIP := new(MockImageProcessor)
	mockIP.On("CheckCompatibility", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(compat)
	mockIP.On("FitImage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(image.NewRGBA(image.Rect(0, 0, 4, 3)), nil)
	return &Plugin{cfg: cfg, store: store, fm: fm, os: mockOS, imgProcessor: mockIP}, store, fm, dir
}

func retainValidJPEG(t *testing.T, store *ImageStore, dir, id string, width int) string {
	t.Helper()
	master := filepath.Join(dir, id+".jpg")
	require.NoError(t, os.WriteFile(master, tinyJPEG(t), 0644))
	require.True(t, store.Add(provider.Image{ID: id, Path: "http://example.invalid/" + id + ".jpg", SourceQueryID: "q", Width: width, Height: width, Enriched: true, IsFavorited: true, Seen: true}))
	store.RetainByQueryID("q", false)
	return master
}

func TestReuse_CancelledDerivativeKeepsRetained(t *testing.T) {
	wp, store, _, dir := reuseTestPlugin(t, nil)
	master := retainValidJPEG(t, store, dir, "cancel1", 0)
	entry, _ := store.GetRetained("cancel1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := wp.ProcessImageJob(ctx, DownloadJob{Ctx: ctx, Image: entry.Image, ReuseOnly: true})
	require.Error(t, err)
	assert.Empty(t, out.ID, "a cancelled re-admission must not be admitted as a derivative-less entry")
	_, retained := store.GetRetained("cancel1")
	assert.True(t, retained)
	assert.FileExists(t, master)
}

func TestReuse_IncompatibleRetainedStaysRetained(t *testing.T) {
	wp, store, _, dir := reuseTestPlugin(t, errors.New("aspect ratio"))
	master := retainValidJPEG(t, store, dir, "narrow1", 0) // no dims: early check cannot reject
	entry, _ := store.GetRetained("narrow1")
	out, err := wp.ProcessImageJob(context.Background(), DownloadJob{Ctx: context.Background(), Image: entry.Image, ReuseOnly: true})
	require.ErrorContains(t, err, "incompatible")
	assert.Empty(t, out.ID, "must stay out of rotation (and evictable), not become an active zombie")
	e, retained := store.GetRetained("narrow1")
	require.True(t, retained)
	assert.True(t, e.Image.ProcessingFlags["incompatible:1920x1080"], "rejection tag persisted on the retained entry")
	assert.Equal(t, 4, e.Image.Width, "probed dimensions persisted (Golden Rule 2)")
	assert.FileExists(t, master)
}

// --- Antigravity #6: Remove and the avoid set ---------------------------------------

func TestRemove_RetainedImageIsBlockedAndDeleted(t *testing.T) {
	store, fm, _ := newRetentionStore(t)
	master := addWithMaster(t, store, fm, "shown", "q", 10)
	store.RetainByQueryID("q", false)

	_, ok := store.Remove("shown")
	require.True(t, ok, "Remove must handle an image that is retained (e.g. still on screen)")
	_, retained := store.GetRetained("shown")
	assert.False(t, retained)
	require.True(t, fm.WaitTimeout(2*time.Second))
	assert.NoFileExists(t, master)
	assert.False(t, store.Add(provider.Image{ID: "shown"}), "blocked")
}

func TestLoadAvoidSet_PurgesRetained(t *testing.T) {
	store, fm, _ := newRetentionStore(t)
	master := addWithMaster(t, store, fm, "blocked1", "q", 10)
	store.RetainByQueryID("q", false)
	store.LoadAvoidSet(map[string]bool{"blocked1": true})
	_, retained := store.GetRetained("blocked1")
	assert.False(t, retained)
	require.True(t, fm.WaitTimeout(2*time.Second))
	assert.NoFileExists(t, master)
}

// --- Antigravity #7: maintenance after Sync and after downloads ---------------------

func TestResyncStore_CleansRetainedDerivatives(t *testing.T) {
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	store, fm, dir := newRetentionStore(t)
	addWithMaster(t, store, fm, "old1", "gone-query", 10)
	deriv := filepath.Join(dir, FittedRootDir, QualityDir, StandardDir, "1920x1080", "old1.jpg")
	writeFile(t, deriv, 10)
	wp := &Plugin{cfg: cfg, store: store, fm: fm}

	wp.resyncStore() // Sync retains old1 (its query is not active)
	require.True(t, fm.WaitTimeout(2*time.Second))
	_, retained := store.GetRetained("old1")
	require.True(t, retained)
	assert.NoFileExists(t, deriv, "derivatives of images Sync retained are cleaned right away")
}

// --- Antigravity #8: metadata of re-admitted images ---------------------------------

func TestReuse_FavoriteAndSeenRecomputed(t *testing.T) {
	wp, store, _, dir := reuseTestPlugin(t, nil)
	wp.cfg.SetSmartFitMode(SmartFitOff)
	retainValidJPEG(t, store, dir, "fav1", 4)
	entry, _ := store.GetRetained("fav1")
	assert.False(t, entry.Image.Seen, "retaining resets Seen so seenCount is not inflated on re-admission")

	out, err := wp.ProcessImageJob(context.Background(), DownloadJob{Ctx: context.Background(), Image: entry.Image, ReuseOnly: true})
	require.NoError(t, err)
	assert.False(t, out.IsFavorited, "stale favorite flag must be recomputed (no favoriter: not favorited)")
	require.True(t, store.Add(out))
	assert.Equal(t, 0, store.SeenCount())
}

func TestReuse_UnenrichedCachedEntryRetriesEnrichmentOnly(t *testing.T) {
	wp, store, _, dir := reuseTestPlugin(t, nil)
	var requests atomic.Int32
	wp.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("network disabled in test")
	})}
	id := "Wallhaven_soft1"
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".jpg"), tinyJPEG(t), 0644))
	require.True(t, store.Add(provider.Image{ID: id, Path: "http://example.invalid/soft1.jpg", Provider: "Wallhaven"})) // enrichment had soft-failed

	cp := &countingProvider{MockImageProvider: new(MockImageProvider)}
	cp.MockImageProvider.On("ID").Return("Wallhaven")
	out, err := wp.ProcessImageJob(context.Background(), DownloadJob{
		Ctx: context.Background(), Image: provider.Image{ID: id, Path: "http://example.invalid/soft1.jpg", Provider: "Wallhaven"}, Provider: cp,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), cp.enrichCalls.Load(), "soft-failed enrichment is retried")
	assert.Equal(t, int32(0), requests.Load(), "but the master is not downloaded again")
	assert.True(t, out.Enriched)
	assert.Equal(t, "artist-soft1", out.Attribution)
}

func TestMergeCachedMetadata_FillsMissingTextFields(t *testing.T) {
	out := mergeCachedMetadata(
		provider.Image{ID: "x", Title: "T", Artist: "A", Year: "1900", DownloadLocation: "http://dl"},
		provider.Image{ID: "x"},
	)
	assert.Equal(t, "T", out.Title)
	assert.Equal(t, "A", out.Artist)
	assert.Equal(t, "1900", out.Year)
	assert.Equal(t, "http://dl", out.DownloadLocation)
	kept := mergeCachedMetadata(provider.Image{Title: "new"}, provider.Image{Title: "cached"})
	assert.Equal(t, "cached", kept.Title, "cached enriched values win")
}

// --- Antigravity #9: concurrent saves -----------------------------------------------

func TestSaveCache_ConcurrentSavesAreSerialized(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	store.SetAsyncSave(true)
	store.SetDebounceDuration(time.Hour) // only explicit saves
	for i := 0; i < 20; i++ {
		addWithMaster(t, store, fm, "img"+string(rune('a'+i)), "q", 1)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.SaveCache()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, cacheIndexFileName))
	require.NoError(t, err)
	var idx cacheIndexFile
	require.NoError(t, json.Unmarshal(data, &idx))
	assert.Len(t, idx.Images, 20)
	_, err = os.Stat(filepath.Join(dir, cacheIndexFileName+".tmp"))
	assert.True(t, os.IsNotExist(err), "no temp file left behind")
}

// The delete claim re-checks ownership at delete time, not from the plan made earlier:
// a job that starts on an ID after planning (here: while another file is being deleted)
// keeps its files, for eviction and orphan cleanup alike.
func TestLease_LiveCheckAtDeleteTime(t *testing.T) {
	t.Run("eviction", func(t *testing.T) {
		store, fm, _ := newRetentionStore(t)
		first := addWithMaster(t, store, fm, "first", "q", 100)
		second := addWithMaster(t, store, fm, "second", "q", 100)
		store.RetainByQueryID("q", false)
		setLastUsed(store, "first", time.Now().Add(-2*time.Hour))
		setLastUsed(store, "second", time.Now().Add(-time.Hour))
		fm.removeFn = func(path string) error {
			if path == first {
				require.NoError(t, store.AcquireJobLease(context.Background(), "second"))
			}
			return os.Remove(path)
		}
		res := store.EnforceDiskBudget(0, nil)
		assert.Equal(t, []string{"first"}, res.EvictedIDs)
		assert.NoFileExists(t, first)
		assert.FileExists(t, second, "leased after planning, before its delete: must be skipped")
		_, ok := store.GetRetained("second")
		assert.True(t, ok)
	})
	t.Run("orphans", func(t *testing.T) {
		store, fm, dir := newRetentionStore(t)
		a := filepath.Join(dir, "orphanA.jpg")
		b := filepath.Join(dir, "orphanB.jpg")
		writeFile(t, a, 1)
		writeFile(t, b, 1)
		var once sync.Once
		fm.removeFn = func(path string) error {
			// Whichever orphan goes first, a job starts on the other one (never on the ID
			// being deleted: that lease would correctly wait for this delete to finish).
			once.Do(func() {
				other := "orphanA"
				if path == a {
					other = "orphanB"
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				require.NoError(t, store.AcquireJobLease(ctx, other))
			})
			return os.Remove(path)
		}
		store.CleanupOrphanFiles(nil)
		_, errA := os.Stat(a)
		_, errB := os.Stat(b)
		assert.True(t, (errA == nil) != (errB == nil), "exactly one orphan deleted; the newly leased one survives")
	})
}

// Antigravity #4 (shuffle timing): Submit returns before a job is processed, so the shuffle
// refresh waits until the re-admitted images have actually landed in the store.
func TestReadmit_ShuffleWaitsForAdmission(t *testing.T) {
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	cfg.SetCacheSize(Cache100Images)
	store, fm, _ := newRetentionStore(t)
	addWithMaster(t, store, fm, "Wallhaven_r1", "q1", 1)
	store.RetainByQueryID("q1", false)
	entry, _ := store.GetRetained("Wallhaven_r1")

	var job DownloadJob
	jobs := make(chan DownloadJob, 1)
	sub := new(MockPipeline)
	sub.On("Submit", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		jobs <- args.Get(1).(DownloadJob) // queued, not yet processed
	}).Return(true)
	mc := &MonitorController{ID: 0, Commands: make(chan Command, 8)}
	wp := &Plugin{cfg: cfg, store: store, fm: fm, jobSubmitter: sub, Monitors: map[int]*MonitorController{0: mc},
		fetchingInProgress: util.NewSafeBool()}
	wp.fetchingInProgress.Set(true) // keep RequestFetch a no-op in this test
	t.Cleanup(func() { wp.CancelQueryContext("q1") })

	go wp.reenableQuery("q1")
	job = <-jobs
	select {
	case cmd := <-mc.Commands:
		t.Fatalf("shuffle refresh (%v) sent before the job finished", cmd)
	case <-time.After(300 * time.Millisecond):
	}
	require.True(t, store.Add(entry.Image)) // the worker's result lands...
	start := time.Now()
	job.finish() // ...and the pipeline signals completion
	select {
	case cmd := <-mc.Commands:
		assert.Equal(t, CmdUpdateShuffle, cmd)
		assert.Less(t, time.Since(start), 2*time.Second, "completion signal, not the 30 s safety timeout")
	case <-time.After(5 * time.Second):
		t.Fatal("no shuffle refresh after completion")
	}
}
