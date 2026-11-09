package wallpaper

// Regression tests for round 2 of the feat/cache-reuse reviews.
// TestRound2_* are the Codex round-2 probes, ported unchanged (assertions kept).
// The remaining tests cover the Antigravity round-2 findings, the ".JPG" extension
// item, and the storage settings UI.

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dixieflatline76/Spice/v2/pkg/provider"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
	"github.com/dixieflatline76/Spice/v2/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestRound2_MaintenanceProtectsNewlyDisplayedImage(t *testing.T) {
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	cfg.SetCacheDiskBudgetMB(1)
	store, fm, dir := newRetentionStore(t)
	master := addWithMaster(t, store, fm, "Wallhaven_displayed", "q", int(2*bytesPerMB))
	img, ok := store.GetByID("Wallhaven_displayed")
	require.True(t, ok)
	img.FilePath = master
	img.DerivativePaths = map[string]string{"1920x1080": master}
	require.True(t, store.replace(img))
	orphan := filepath.Join(dir, "orphan.jpg")
	writeFile(t, orphan, 1)

	mockOS := new(MockOS)
	info, err := os.Stat(master)
	require.NoError(t, err)
	mockOS.On("Stat", master).Return(info, nil)
	shown := make(chan struct{})
	mockOS.On("SetWallpaper", master, 0).Run(func(mock.Arguments) { close(shown) }).Return(nil)
	mc := NewMonitorController(0, Monitor{ID: 0, Rect: image.Rect(0, 0, 1920, 1080)}, store, fm, mockOS, cfg, nil)
	mc.Start()
	defer mc.Stop()
	wp := &Plugin{cfg: cfg, store: store, fm: fm, Monitors: map[int]*MonitorController{0: mc}}
	// The real actor displays the image after maintenance took its protected-ID snapshot.
	// Its query then leaves rotation before the budget pass begins.
	fm.removeFn = func(path string) error {
		if path == orphan {
			mc.Commands <- CmdNext
			select {
			case <-shown:
			case <-time.After(2 * time.Second):
				t.Fatal("actor did not display image")
			}
			mc.mu.RLock()
			current := mc.State.CurrentImage.ID
			mc.mu.RUnlock()
			require.Equal(t, img.ID, current)
			store.RetainByQueryID("q", false)
		}
		return os.Remove(path)
	}
	wp.runCacheMaintenance(true)
	require.True(t, wp.displayedImageIDs()[img.ID])
	require.FileExists(t, master, "maintenance must protect the image currently displayed, not just its earlier snapshot")
}

func TestRound2_RetainedRejectionTagsFollowChangedSettings(t *testing.T) {
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	store, fm, _ := newRetentionStore(t)
	master := addWithMaster(t, store, fm, "Wallhaven_tags", "q", 100)
	require.NoError(t, os.WriteFile(master, tinyJPEG(t), 0644))
	img, ok := store.GetByID("Wallhaven_tags")
	require.True(t, ok)
	img.Width, img.Height = 1080, 1920 // portrait source rejected on landscape under the previous mode
	img.ProcessingFlags = map[string]bool{"SmartFit": true, "FitQuality": true, "incompatible:1920x1080": true}
	require.True(t, store.replace(img))
	store.RetainByQueryID("q", false)
	cfg.SetSmartFitMode(SmartFitOff)
	store.Sync(100, map[string]bool{"SmartFit": false, "FitQuality": false}, map[string]bool{"q": true})
	entry, ok := store.GetRetained(img.ID)
	require.True(t, ok)
	mockOS := new(MockOS)
	mockOS.On("GetMonitors").Return([]Monitor{{ID: 0, Rect: image.Rect(0, 0, 1920, 1080)}}, nil)
	mockIP := new(MockImageProcessor)
	mockIP.On("CheckCompatibility", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	wp := &Plugin{cfg: cfg, store: store, fm: fm, os: mockOS, imgProcessor: mockIP}
	_, err := wp.ProcessImageJob(context.Background(), DownloadJob{Image: entry.Image, ReuseOnly: true})
	require.NoError(t, err, "a rejection under old Smart Fit settings must not reject an otherwise compatible local image")
}

func TestRound2_BudgetCheckedAfterDownloadedBytesArrive(t *testing.T) {
	isolateUserCache(t)
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	cfg.SetCacheDiskBudgetMB(1)
	cfg.Queries = []ImageQuery{}
	qid, err := cfg.AddImageQuery("Budget", "http://mock.url", true)
	require.NoError(t, err)
	store, fm, _ := newRetentionStore(t)
	retainedMaster := addWithMaster(t, store, fm, "Wallhaven_old", "other", int(800*1024))
	store.RetainByQueryID("other", false)
	entry, ok := store.GetRetained("Wallhaven_old")
	require.True(t, ok)
	require.False(t, entry.DerivativesCleared)
	release := make(chan struct{})
	var releaseOnce sync.Once
	pipeline := NewPipeline(context.Background(), cfg, store, func(ctx context.Context, job DownloadJob) (provider.Image, error) {
		<-release
		path, err := fm.GetMasterPath(job.Image.ID, ".jpg")
		if err != nil {
			return provider.Image{}, err
		}
		if err := os.WriteFile(path, make([]byte, 500*1024), 0644); err != nil {
			return provider.Image{}, err
		}
		return job.Image, nil
	}, nil, nil)
	pipeline.Start(1)
	defer func() { releaseOnce.Do(func() { close(release) }); pipeline.Stop() }()
	mp := new(MockImageProvider)
	mp.On("ID").Return("Wallhaven")
	mp.On("FetchImages", mock.Anything, "http://mock.url", 1).Return([]provider.Image{{ID: "new", Path: "http://example.invalid/new.jpg", SourceQueryID: qid}}, nil)
	pm := new(MockPluginManager)
	pm.On("NotifyUser", mock.Anything, mock.Anything).Return()
	wp := &Plugin{cfg: cfg, store: store, fm: fm, manager: pm, jobSubmitter: pipeline,
		queryPages: make(map[string]*util.SafeCounter), fetchingInProgress: util.NewSafeBool(), interrupt: util.NewSafeBool(), providers: map[string]provider.ImageProvider{"Wallhaven": mp}}
	wp.FetchNewImages(false)
	require.Eventually(t, func() bool {
		e, ok := store.GetRetained("Wallhaven_old")
		return ok && e.DerivativesCleared && !wp.fetchingInProgress.Value()
	}, 2*time.Second, 10*time.Millisecond, "scheduled maintenance should finish before the blocked download")
	releaseOnce.Do(func() { close(release) })
	require.Eventually(t, func() bool { return store.Exists("Wallhaven_new") && !store.IsLeased("Wallhaven_new") }, 2*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { _, err := os.Stat(retainedMaster); return os.IsNotExist(err) }, 500*time.Millisecond, 10*time.Millisecond, "the now-over-budget cache should evict the 800 KiB retained master after the 500 KiB download")
}

func TestRound2_FirstMigrationFailurePreservesLegacyOwnership(t *testing.T) {
	dir := t.TempDir()
	fm := NewFileManager(dir)
	require.NoError(t, fm.EnsureDirs())
	cachePath := filepath.Join(dir, "image_cache_map.json")
	master := filepath.Join(dir, "Wallhaven_legacy.jpg")
	require.NoError(t, os.WriteFile(master, tinyJPEG(t), 0644))
	img := provider.Image{ID: "Wallhaven_legacy", Provider: "Wallhaven", Path: "http://example.invalid/legacy.jpg", SourceQueryID: "disabled"}
	data, err := json.Marshal([]provider.Image{img})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cachePath, data, 0644))
	// Start with the exact pre-upgrade layout: only the legacy active cache exists.
	store := NewImageStore()
	store.SetAsyncSave(false)
	store.SetFileManager(fm, cachePath)
	require.NoError(t, store.LoadCache())
	require.NoFileExists(t, filepath.Join(dir, cacheIndexFileName))
	// Make the first authoritative-index write fail, while its legacy mirror can succeed.
	require.NoError(t, os.Mkdir(filepath.Join(dir, cacheIndexFileName+".tmp"), 0755))
	store.Sync(100, nil, map[string]bool{})
	require.Equal(t, 1, store.RetainedCount())
	require.Error(t, store.SaveCache())
	reloaded := NewImageStore()
	reloaded.SetFileManager(fm, cachePath)
	require.NoError(t, reloaded.LoadCache())
	reloaded.CleanupOrphanFiles(nil)
	require.FileExists(t, master, "a failed first migration must preserve the only durable ownership record")
}

func TestRound2_ReadFailureMustNotAdmitRetainedImage(t *testing.T) {
	wp, store, _, dir := reuseTestPlugin(t, nil)
	id := "Wallhaven_io"
	master := filepath.Join(dir, id+".jpg")
	require.NoError(t, os.WriteFile(master, tinyJPEG(t), 0644))
	require.True(t, store.Add(provider.Image{ID: id, Path: "http://example.invalid/io.jpg", Provider: "Wallhaven", SourceQueryID: "q"}))
	store.RetainByQueryID("q", false)
	entry, ok := store.GetRetained(id)
	require.True(t, ok)
	require.NoError(t, os.Chmod(master, 0000))
	defer os.Chmod(master, 0644)
	f, readErr := os.Open(master)
	if readErr == nil {
		f.Close()
		t.Skip("host permissions do not support this unreadable-file probe")
	}
	out, err := wp.ProcessImageJob(context.Background(), DownloadJob{Image: entry.Image, ReuseOnly: true})
	require.ErrorContains(t, err, "failed to open master")
	p := &Pipeline{store: store}
	p.admitResult(ProcessResult{Image: out, Error: err})
	require.False(t, store.Exists(id), "an I/O failure must not publish a derivative-less active entry")
	_, retained := store.GetRetained(id)
	require.True(t, retained)
	require.True(t, fileExists(master))
}

// --- Antigravity R2 #1: failed backlog healing must not delete active derivatives ----

func TestFailedBacklogHeal_KeepsActiveDerivatives(t *testing.T) {
	wp, store, _, dir := reuseTestPlugin(t, nil)
	wp.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	})}
	id := "Wallhaven_heal2"
	deriv := filepath.Join(dir, FittedRootDir, QualityDir, StandardDir, "1920x1080", id+".jpg")
	writeFile(t, deriv, 10) // valid derivative for the existing monitor; master is gone
	require.True(t, store.Add(provider.Image{ID: id, Path: "http://example.invalid/heal2.jpg", Provider: "Wallhaven",
		Enriched: true, DerivativePaths: map[string]string{"1920x1080": deriv}, FilePath: deriv}))

	cp := &countingProvider{MockImageProvider: new(MockImageProvider)}
	cp.MockImageProvider.On("ID").Return("Wallhaven")
	out, err := wp.ProcessImageJob(context.Background(), DownloadJob{Ctx: context.Background(),
		Image: provider.Image{ID: id, Path: "http://example.invalid/heal2.jpg", Provider: "Wallhaven"}, Provider: cp})
	require.Error(t, err)
	assert.Empty(t, out.ID)
	assert.FileExists(t, deriv, "a failed job deletes only files it created")
	img, ok := store.GetByID(id)
	require.True(t, ok)
	assert.Equal(t, deriv, img.DerivativePaths["1920x1080"])
}

// An active cached image that fits no monitor moves to the retained index (files kept,
// tags recorded) instead of becoming an unevictable active zombie.
func TestReuse_ActiveIncompatibleMovesToRetained(t *testing.T) {
	wp, store, _, dir := reuseTestPlugin(t, errors.New("aspect ratio"))
	id := "Wallhaven_tall"
	master := filepath.Join(dir, id+".jpg")
	require.NoError(t, os.WriteFile(master, tinyJPEG(t), 0644))
	require.True(t, store.Add(provider.Image{ID: id, Path: "http://example.invalid/tall.jpg", Provider: "Wallhaven", Enriched: true, SourceQueryID: "q"}))
	img, _ := store.GetByID(id)
	out, err := wp.ProcessImageJob(context.Background(), DownloadJob{Ctx: context.Background(), Image: img})
	require.ErrorContains(t, err, "incompatible")
	assert.Empty(t, out.ID)
	assert.False(t, store.Exists(id))
	e, ok := store.GetRetained(id)
	require.True(t, ok)
	assert.True(t, e.Image.ProcessingFlags["incompatible:1920x1080"])
	assert.FileExists(t, master)
}

// --- Antigravity R2 #2: Sync's derivative invalidation goes through the claim protocol -

func TestSync_InvalidationSkipsLeasedImage(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	addWithMaster(t, store, fm, "inflight", "q", 10)
	deriv := filepath.Join(dir, FittedRootDir, QualityDir, StandardDir, "1920x1080", "inflight.jpg")
	writeFile(t, deriv, 10)
	img, _ := store.GetByID("inflight")
	img.ProcessingFlags = map[string]bool{"SmartFit": true}
	img.DerivativePaths = map[string]string{"1920x1080": deriv}
	require.True(t, store.replace(img))

	require.NoError(t, store.AcquireJobLease(context.Background(), "inflight"))
	store.Sync(100, map[string]bool{"SmartFit": false}, nil)
	require.True(t, fm.WaitTimeout(2*time.Second))
	got, _ := store.GetByID("inflight")
	assert.Equal(t, deriv, got.DerivativePaths["1920x1080"], "the in-flight job owns the image")
	assert.FileExists(t, deriv)
	store.ReleaseJobLease("inflight")
}

func TestSync_InvalidationBlocksPublicationUntilDeleted(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	addWithMaster(t, store, fm, "stale", "q", 10)
	deriv := filepath.Join(dir, FittedRootDir, QualityDir, StandardDir, "1920x1080", "stale.jpg")
	writeFile(t, deriv, 10)
	img, _ := store.GetByID("stale")
	img.ProcessingFlags = map[string]bool{"SmartFit": true}
	img.DerivativePaths = map[string]string{"1920x1080": deriv}
	require.True(t, store.replace(img))

	var replaced atomic.Bool
	var leaseErr atomic.Value
	fm.removeFn = func(path string) error {
		if path == deriv {
			replaced.Store(store.replace(img)) // a job trying to publish the stale derivative
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if err := store.AcquireJobLease(ctx, "stale"); err != nil {
				leaseErr.Store(err)
			} else {
				store.ReleaseJobLease("stale")
			}
		}
		return os.Remove(path)
	}
	store.Sync(100, map[string]bool{"SmartFit": false}, nil)
	require.True(t, fm.WaitTimeout(2*time.Second))
	assert.False(t, replaced.Load(), "publication refused while the invalidation claim is held")
	assert.NotNil(t, leaseErr.Load(), "a job waits for the invalidation to finish")
	assert.NoFileExists(t, deriv)
	got, _ := store.GetByID("stale")
	assert.Empty(t, got.DerivativePaths)
}

// --- Antigravity R2 #3: lease release finishes skipped deletes; inactive downloads retained

func TestLeaseRelease_FinishesSkippedDelete(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	master := addWithMaster(t, store, fm, "blockme", "q", 10)
	deriv := filepath.Join(dir, FittedRootDir, QualityDir, StandardDir, "1920x1080", "blockme.jpg")
	writeFile(t, deriv, 10)
	require.NoError(t, store.AcquireJobLease(context.Background(), "blockme"))
	_, ok := store.Remove("blockme") // user blocks it while a job is running
	require.True(t, ok)
	require.True(t, fm.WaitTimeout(2*time.Second))
	assert.FileExists(t, master, "the job still owns the files")

	store.ReleaseJobLease("blockme")
	require.True(t, fm.WaitTimeout(2*time.Second))
	assert.NoFileExists(t, master, "the skipped delete completes when the lease is released")
	assert.NoFileExists(t, deriv)
}

func TestAdmit_BlockedResultFilesDeletedAfterRelease(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	master := filepath.Join(dir, "late.jpg")
	require.NoError(t, store.AcquireJobLease(context.Background(), "late"))
	writeFile(t, master, 10) // the job downloaded it...
	store.LoadAvoidSet(map[string]bool{"late": true})
	p := &Pipeline{store: store}
	p.admitResult(ProcessResult{Image: provider.Image{ID: "late"}, JobID: "late", leased: true})
	require.True(t, fm.WaitTimeout(2*time.Second))
	assert.False(t, store.Exists("late"))
	assert.NoFileExists(t, master, "a blocked download is deleted, not orphaned")
}

func TestAdd_InactiveQueryDownloadIsRetained(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	master := filepath.Join(dir, "Wallhaven_mid.jpg")
	writeFile(t, master, 10)
	store.SetQueryActiveFunc(func(string) bool { return false }) // disabled mid-download
	assert.False(t, store.Add(provider.Image{ID: "Wallhaven_mid", Path: "http://example.invalid/mid.jpg", SourceQueryID: "q1", Provider: "Wallhaven"}))
	require.True(t, fm.WaitTimeout(2*time.Second))
	e, ok := store.GetRetained("Wallhaven_mid")
	require.True(t, ok, "finished download is retained for its query, not orphaned")
	assert.Equal(t, "q1", e.Image.SourceQueryID)
	assert.Equal(t, int64(10), e.Bytes)
	store.CleanupOrphanFiles(nil)
	assert.FileExists(t, master)
}

// --- Antigravity R2 #4: crash/failure between index and mirror; retained ID migration --

func TestPersist_MirrorFailureAfterIndexUsesNewState(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	addWithMaster(t, store, fm, "Wallhaven_a", "q", 10)
	addWithMaster(t, store, fm, "Wallhaven_b", "q2", 10)
	require.NoError(t, store.SaveCache())
	// Index commits, mirror write fails (crash between the two writes).
	require.NoError(t, os.Mkdir(filepath.Join(dir, "cache.json.tmp"), 0755))
	store.RetainByQueryID("q2", false)

	reloaded := NewImageStore()
	reloaded.SetFileManager(fm, filepath.Join(dir, "cache.json"))
	require.NoError(t, reloaded.LoadCache())
	assert.True(t, reloaded.Exists("Wallhaven_a"))
	assert.False(t, reloaded.Exists("Wallhaven_b"), "the newer index wins over the stale mirror")
	_, ok := reloaded.GetRetained("Wallhaven_b")
	assert.True(t, ok)
}

func TestPersist_IndexFailureKeepsOldStateWithoutHybrid(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	addWithMaster(t, store, fm, "Wallhaven_a", "q", 10)
	addWithMaster(t, store, fm, "Wallhaven_b", "q", 10)
	require.NoError(t, store.SaveCache())
	require.NoError(t, os.Mkdir(filepath.Join(dir, cacheIndexFileName+".tmp"), 0755))
	_, ok := store.Remove("Wallhaven_b") // the user deletes B; the save fails
	require.True(t, ok)
	require.Error(t, store.SaveCache())

	reloaded := NewImageStore()
	reloaded.SetFileManager(fm, filepath.Join(dir, "cache.json"))
	require.NoError(t, reloaded.LoadCache())
	_, retained := reloaded.GetRetained("Wallhaven_b")
	assert.False(t, retained, "a failed save restores the old state, never a hybrid that resurrects B as retained")
}

func TestPersist_OlderVersionRewroteMirrorIsMerged(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	addWithMaster(t, store, fm, "Wallhaven_a", "q", 10)
	addWithMaster(t, store, fm, "Wallhaven_b", "q", 10)
	require.NoError(t, store.SaveCache())
	// An older Spice version runs and rewrites the legacy file with only A.
	a, _ := store.GetByID("Wallhaven_a")
	data, err := json.Marshal([]provider.Image{a})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cache.json"), data, 0644))

	reloaded := NewImageStore()
	reloaded.SetFileManager(fm, filepath.Join(dir, "cache.json"))
	require.NoError(t, reloaded.LoadCache())
	assert.True(t, reloaded.Exists("Wallhaven_a"))
	assert.False(t, reloaded.Exists("Wallhaven_b"))
	_, ok := reloaded.GetRetained("Wallhaven_b")
	assert.True(t, ok, "what the index owned but the older version dropped stays owned (retained)")
}

func TestLoadCache_MigratesRetainedLegacyIDs(t *testing.T) {
	store, _, dir := newRetentionStore(t)
	writeFile(t, filepath.Join(dir, "victim.jpg"), 10)
	idx := cacheIndexFile{Version: 2, Images: []provider.Image{},
		Retained: map[string]RetainedEntry{"victim": {Image: provider.Image{ID: "victim", Provider: "Wallhaven"}, Ext: ".jpg", Bytes: 10, Provider: "Wallhaven", LastUsed: time.Now()}}}
	data, err := json.Marshal(idx)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, cacheIndexFileName), data, 0644))

	require.NoError(t, store.LoadCache())
	_, old := store.GetRetained("victim")
	assert.False(t, old)
	e, ok := store.GetRetained("Wallhaven_victim")
	require.True(t, ok, "retained keys are migrated like active IDs")
	assert.Equal(t, "Wallhaven_victim", e.Image.ID)
	assert.FileExists(t, filepath.Join(dir, "Wallhaven_victim.jpg"))
	store.CleanupOrphanFiles(nil)
	assert.FileExists(t, filepath.Join(dir, "Wallhaven_victim.jpg"))
}

// --- Antigravity R2 #5: budget pass rechecks the disk before dropping an entry ------

func TestEnforceDiskBudget_RechecksMissingMasterBeforeDropping(t *testing.T) {
	store, fm, dir := newRetentionStore(t)
	late := filepath.Join(dir, "late.jpg")
	store.mu.Lock()
	store.retained["late"] = RetainedEntry{Image: provider.Image{ID: "late"}, Ext: ".jpg", Bytes: 10, LastUsed: time.Now()}
	store.mu.Unlock()
	fm.scanHook = func() { writeFile(t, late, 10) } // appears after the scan, before the lock
	store.EnforceDiskBudget(1<<30, nil)
	_, ok := store.GetRetained("late")
	assert.True(t, ok, "an entry is dropped only after a fresh disk check")

	fm.scanHook = nil
	require.NoError(t, os.Remove(late))
	res := store.EnforceDiskBudget(1<<30, nil)
	assert.Equal(t, 1, res.DroppedStale)
}

// --- Antigravity R2 #6: re-enable completion signal and fetch-if-short --------------

func TestReenable_FetchesWhenReadmissionFallsShort(t *testing.T) {
	assert.Positive(t, runQueryEnabledAdmitting(t, 100, 99), "99 of 100 admitted: rotation still short, fetch")
}

// --- Antigravity R2 #7: budget enforced when downloaded bytes are admitted -----------

// Rewritten from the round-1 version (which started over budget with a no-op Submit):
// the cache starts UNDER budget and crosses it only when the real download lands.
func TestFetchCycle_EnforcesDiskBudget(t *testing.T) {
	isolateUserCache(t)
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	cfg.SetCacheDiskBudgetMB(1)
	cfg.Queries = []ImageQuery{}
	_, err := cfg.AddImageQuery("Budget", "http://mock.url", true)
	require.NoError(t, err)

	store, fm, _ := newRetentionStore(t)
	old := addWithMaster(t, store, fm, "Wallhaven_old", "other", 800*1024)
	store.RetainByQueryID("other", false)
	require.Empty(t, store.EnforceDiskBudget(cfg.GetCacheDiskBudgetBytes(), nil).EvictedIDs, "starts under budget")

	pipeline := NewPipeline(context.Background(), cfg, store, func(ctx context.Context, job DownloadJob) (provider.Image, error) {
		time.Sleep(150 * time.Millisecond) // the download takes a while
		path, _ := fm.GetMasterPath(job.Image.ID, ".jpg")
		if err := os.WriteFile(path, make([]byte, 500*1024), 0644); err != nil {
			return provider.Image{}, err
		}
		return job.Image, nil
	}, nil, nil)
	pipeline.Start(1)
	defer pipeline.Stop()

	mp := new(MockImageProvider)
	mp.On("ID").Return("Wallhaven")
	mp.On("FetchImages", mock.Anything, "http://mock.url", 1).Return([]provider.Image{{ID: "new1", Path: "http://example.invalid/new1.jpg"}}, nil)
	mockPM := new(MockPluginManager)
	mockPM.On("NotifyUser", mock.Anything, mock.Anything).Return()
	wp := &Plugin{
		cfg: cfg, store: store, fm: fm, manager: mockPM, jobSubmitter: pipeline,
		queryPages:         make(map[string]*util.SafeCounter),
		fetchingInProgress: util.NewSafeBool(),
		interrupt:          util.NewSafeBool(),
		providers:          map[string]provider.ImageProvider{"Wallhaven": mp},
	}
	wp.FetchNewImages(false)
	require.Eventually(t, func() bool { return store.Exists("Wallhaven_new1") }, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		_, still := store.GetRetained("Wallhaven_old")
		return !still
	}, 2*time.Second, 10*time.Millisecond, "admitting the download pushed the cache over budget")
	assert.NoFileExists(t, old)
}

// --- Display references --------------------------------------------------------------

func TestDisplayRef_BlockedImageDeletedAfterSwitch(t *testing.T) {
	store, fm, _ := newRetentionStore(t)
	master := addWithMaster(t, store, fm, "onscreen", "q", 10)
	addWithMaster(t, store, fm, "nextone", "q", 10)
	store.ReserveDisplay(0, "onscreen")
	store.CommitDisplay(0, "onscreen")
	_, ok := store.Remove("onscreen") // user deletes the current wallpaper
	require.True(t, ok)
	require.True(t, fm.WaitTimeout(2*time.Second))
	assert.FileExists(t, master, "still on screen")

	store.ReserveDisplay(0, "nextone")
	store.CommitDisplay(0, "nextone") // the actor moved on
	require.True(t, fm.WaitTimeout(2*time.Second))
	assert.NoFileExists(t, master, "deleted once no monitor shows it")
}

func TestDisplayRef_ReservationProtectsDuringSwitch(t *testing.T) {
	store, fm, _ := newRetentionStore(t)
	master := addWithMaster(t, store, fm, "incoming", "q", 10)
	store.RetainByQueryID("q", false)
	store.ReserveDisplay(1, "incoming") // switching to it, not yet published
	store.EnforceDiskBudget(0, nil)
	store.CleanupOrphanFiles(nil)
	assert.FileExists(t, master)
	store.CancelDisplay(1, "incoming")
	store.EnforceDiskBudget(0, nil)
	assert.NoFileExists(t, master)
}

// --- ".JPG" extensions ------------------------------------------------------------

func TestMasterExt_LowercaseAndExistingFilesNormalized(t *testing.T) {
	assert.Equal(t, ".jpg", masterExtFor(provider.Image{Path: "https://upload.example/Foo.JPG"}))

	store, fm, dir := newRetentionStore(t)
	id := "Wikimedia_42"
	upper := filepath.Join(dir, id+".JPG")
	writeFile(t, upper, 10)
	derivUpper := filepath.Join(dir, FittedRootDir, QualityDir, StandardDir, "1920x1080", id+".JPG")
	writeFile(t, derivUpper, 10)
	require.True(t, store.Add(provider.Image{ID: id, Path: "https://upload.example/Foo.JPG", Provider: "Wikimedia",
		FilePath: derivUpper, DerivativePaths: map[string]string{"1920x1080": derivUpper}}))
	require.NoError(t, store.SaveCache())

	reloaded := NewImageStore()
	reloaded.SetAsyncSave(false)
	reloaded.SetFileManager(fm, filepath.Join(dir, "cache.json"))
	require.NoError(t, reloaded.LoadCache())

	names := map[string]bool{}
	for _, n := range dirEntries(t, dir) {
		names[n] = true
	}
	assert.True(t, names[id+".jpg"], "master renamed to lowercase on every OS: %v", names)
	assert.False(t, names[id+".JPG"])
	img, ok := reloaded.GetByID(id)
	require.True(t, ok)
	lowerDeriv := filepath.Join(dir, FittedRootDir, QualityDir, StandardDir, "1920x1080", id+".jpg")
	assert.Equal(t, lowerDeriv, img.DerivativePaths["1920x1080"])
	assert.Equal(t, lowerDeriv, img.FilePath)
	assert.FileExists(t, lowerDeriv)
	reloaded.Sync(100, nil, nil) // master check uses lowercase extensions
	assert.True(t, reloaded.Exists(id), "not treated as missing (no re-download)")
}

// --- Storage settings UI -----------------------------------------------------------

func TestConfig_StorageSettingsDefaultsAndIndexes(t *testing.T) {
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	assert.Equal(t, int64(25)*bytesPerMB, cfg.GetMaxDownloadSizeBytes())
	assert.Equal(t, 1, cfg.GetMaxDownloadSizeIndex())
	assert.Equal(t, int64(2048)*bytesPerMB, cfg.GetCacheDiskBudgetBytes())
	assert.Equal(t, 2, cfg.GetCacheDiskBudgetIndex())

	cfg.SetMaxDownloadSizeIndex(4)
	assert.Equal(t, int64(0), cfg.GetMaxDownloadSizeBytes(), "No limit is stored as 0")
	assert.Equal(t, 4, cfg.GetMaxDownloadSizeIndex())
	cfg.SetMaxDownloadSizeIndex(3)
	assert.Equal(t, int64(100)*bytesPerMB, cfg.GetMaxDownloadSizeBytes())
	cfg.SetMaxDownloadSizeIndex(99) // out of range: ignored
	assert.Equal(t, 3, cfg.GetMaxDownloadSizeIndex())

	cfg.SetCacheDiskBudgetIndex(0)
	assert.Equal(t, int64(500)*bytesPerMB, cfg.GetCacheDiskBudgetBytes())
	cfg.SetCacheDiskBudgetIndex(4)
	assert.Equal(t, int64(10240)*bytesPerMB, cfg.GetCacheDiskBudgetBytes())
	cfg.SetCacheDiskBudgetMB(777) // not an option: index falls back to the default's
	assert.Equal(t, 2, cfg.GetCacheDiskBudgetIndex())
}

func TestDownloadCap_ZeroMeansUnlimited(t *testing.T) {
	wp, dir := newCapPlugin(t, 0)
	assert.Equal(t, int64(0), wp.maxDownloadBytes())
	assert.False(t, wp.knownOversized(provider.Image{ID: "x", FileSize: 1 << 40}))
	big := int64(30) * bytesPerMB // over the 25 MB default
	body := &countingBody{r: strings.NewReader(strings.Repeat("x", int(big)))}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: big, Body: body, Header: http.Header{}, Request: r}, nil
	})}
	master := filepath.Join(dir, "Wallhaven_huge.jpg")
	_, err := wp.downloadMasterFile(context.Background(), client, "http://example.invalid/huge.jpg", master, nil)
	require.NoError(t, err)
	info, err := os.Stat(master)
	require.NoError(t, err)
	assert.Equal(t, big, info.Size())
}

func TestCacheUsage_SplitsRotationAndKept(t *testing.T) {
	store, fm, _ := newRetentionStore(t)
	addWithMaster(t, store, fm, "act", "q", int(3*bytesPerMB))
	addWithMaster(t, store, fm, "kept", "r", int(bytesPerMB))
	store.RetainByQueryID("r", false)
	active, kept := store.CacheUsage()
	assert.Equal(t, 3*bytesPerMB, active)
	assert.Equal(t, bytesPerMB, kept)

	wp := &Plugin{store: store}
	assert.Equal(t, "Using 4 MB (3 MB in rotation, 1 MB kept)", wp.CacheUsageSummary())
	assert.Equal(t, "1.2 GB", formatBytes(1288490188))
	assert.Equal(t, "2 GB", formatBytes(2048*bytesPerMB))
	assert.Equal(t, "840 MB", formatBytes(840*bytesPerMB))
}

func TestGeneralTab_StorageSettingsItems(t *testing.T) {
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	store, fm, _ := newRetentionStore(t)
	kept := addWithMaster(t, store, fm, "kept", "r", int(600*bytesPerMB/100)) // 6 MB
	store.RetainByQueryID("r", false)
	inRotation := addWithMaster(t, store, fm, "act", "q", int(bytesPerMB))
	wp := &Plugin{cfg: cfg, store: store, fm: fm}

	panel := NewPrefsPanelBuilder(wp, nil).BuildGeneralTabSchema()
	var items []schema.ItemSchema
	for _, sec := range panel.Sections {
		if sec.Title == "Wallpaper Cycle & Cache" {
			items = sec.Items
		}
	}
	require.NotEmpty(t, items)
	idx := -1
	for i, it := range items {
		if si, ok := it.(schema.SelectItem); ok && si.Name == "cacheSize" {
			idx = i
		}
	}
	require.GreaterOrEqual(t, idx, 0)
	require.Greater(t, len(items), idx+3)

	maxDL, ok := items[idx+1].(schema.SelectItem)
	require.True(t, ok)
	assert.Equal(t, "maxDownloadSize", maxDL.Name)
	assert.Equal(t, "Max Download Size:", maxDL.Label)
	assert.Equal(t, []string{"10 MB", "25 MB", "50 MB", "100 MB", "No limit"}, maxDL.Options)
	assert.Equal(t, 1, maxDL.InitialValue)

	budget, ok := items[idx+2].(schema.SelectItem)
	require.True(t, ok)
	assert.Equal(t, "cacheDiskSpace", budget.Name)
	assert.Equal(t, []string{"500 MB", "1 GB", "2 GB", "5 GB", "10 GB"}, budget.Options)
	assert.Equal(t, 2, budget.InitialValue)

	usage, ok := items[idx+3].(schema.LabelItem)
	require.True(t, ok)
	assert.Equal(t, "cacheDiskUsage", usage.ID)
	assert.Equal(t, "Using 7 MB (1 MB in rotation, 6 MB kept)", usage.Text)

	// Staged apply: nothing changes until ApplyFunc runs (the Apply Changes button).
	maxDL.ApplyFunc(4)
	assert.Equal(t, int64(0), cfg.GetMaxDownloadSizeBytes())

	// Lowering the budget trims retained files right away, never images in rotation.
	cfg.SetCacheDiskBudgetMB(1) // simulate a budget below current use
	wp.ApplyCacheDiskBudgetIndex(-1)
	require.True(t, fm.WaitTimeout(2*time.Second))
	assert.NoFileExists(t, kept)
	assert.FileExists(t, inRotation)
	budget.ApplyFunc(0)
	assert.Equal(t, int64(500)*bytesPerMB, cfg.GetCacheDiskBudgetBytes())
}
