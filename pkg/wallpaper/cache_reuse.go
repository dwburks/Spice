package wallpaper

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"sync"
	"time"

	"github.com/disintegration/imaging"

	"github.com/dixieflatline76/Spice/v2/pkg/provider"
	"github.com/dixieflatline76/Spice/v2/util/log"
)

// errIncompatibleAll: the image fits none of the connected monitors.
var errIncompatibleAll = errors.New("incompatible image skipped (fits zero monitors)")

// errCorruptMaster: the master file opened but could not be decoded. Unlike open errors
// (permissions, a lock on Windows) this is a property of the file, so it is invalidated.
var errCorruptMaster = errors.New("master file is not a decodable image")

// openMaster decodes a master, distinguishing an unreadable file (transient) from an
// undecodable one (errCorruptMaster).
func openMaster(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open master %s: %w", path, err)
	}
	defer f.Close()
	img, err := imaging.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("failed to open master %s: %w: %v", path, errCorruptMaster, err)
	}
	return img, nil
}

// reuseInfo says whether a job reuses a cached master and who owns it.
type reuseInfo struct {
	ok           bool
	fromRetained bool // retained index (else the active store)
	masterPath   string
}

// jobArtifacts lists the files a job itself created, the only ones its failure may delete.
type jobArtifacts struct {
	master      bool
	masterPath  string
	derivatives []string
}

// taggedIncompatibleWithAll reports whether img carries an incompatibility tag for every
// resolution (set by the compatibility check or while fitting).
func taggedIncompatibleWithAll(img provider.Image, resolutions []Resolution) bool {
	if len(resolutions) == 0 {
		return false
	}
	for _, res := range resolutions {
		if !img.ProcessingFlags[fmt.Sprintf("incompatible:%dx%d", res.Width, res.Height)] {
			return false
		}
	}
	return true
}

// rejectIncompatible handles an image that fits none of the connected monitors.
//   - Downloaded by this job: returned (as before) so its rejection tags are stored with
//     a masterless entry; cleanupFailedJob deletes the master it downloaded.
//   - Reused: nothing is admitted or replaced. The tags are recorded on a retained entry,
//     and an active image is moved to the retained index (out of rotation, evictable,
//     reconsidered if settings or monitors change) - its files stay with their owner.
func (wp *Plugin) rejectIncompatible(img provider.Image, reuse reuseInfo) (provider.Image, error) {
	if !reuse.ok {
		return img, errIncompatibleAll
	}
	if rs, ok := wp.store.(retainedStore); ok {
		if reuse.fromRetained {
			rs.UpdateRetainedMetadata(img)
		} else {
			rs.RetainActive(img)
		}
	}
	return provider.Image{}, fmt.Errorf("%w: %s", errIncompatibleAll, img.ID)
}

// ErrDownloadTooLarge is returned when an original image exceeds the max download size.
// It is a per-image skip, not a provider failure: it never triggers retries or page wrapping.
var ErrDownloadTooLarge = errors.New("image exceeds max download size")

// retainedStore is the retained-index surface of *ImageStore. It is kept out of
// StoreInterface so test doubles of the store keep working unchanged; callers fall
// back to the old behavior when the store does not implement it.
type retainedStore interface {
	GetRetained(id string) (RetainedEntry, bool)
	DropRetained(id string)
	RetainedForQuery(queryID string, limit int) []RetainedEntry
	RetainByQueryID(queryID string, detach bool)
	KnownFileIDs() map[string]bool
	EnforceDiskBudget(budget int64, protected map[string]bool) EvictionResult
	CleanupOrphanFiles(extraKnown map[string]bool) int
	UpdateRetainedMetadata(img provider.Image) bool
	RetainActive(img provider.Image) bool
	Exists(id string) bool
}

// maxDownloadBytes returns the configured download size cap in bytes.
func (wp *Plugin) maxDownloadBytes() int64 {
	if wp.cfg == nil {
		return DefaultMaxDownloadSizeMB * bytesPerMB
	}
	return wp.cfg.GetMaxDownloadSizeBytes()
}

// cacheDiskBudgetBytes returns the configured disk budget for master files in bytes.
func (wp *Plugin) cacheDiskBudgetBytes() int64 {
	if wp.cfg == nil {
		return DefaultCacheDiskBudgetMB * bytesPerMB
	}
	return wp.cfg.GetCacheDiskBudgetBytes()
}

// knownOversized reports whether img is known to exceed the download cap, either from
// the size the provider reported or from an earlier aborted download this session.
func (wp *Plugin) knownOversized(img provider.Image) bool {
	if img.FileSize > 0 {
		if limit := wp.maxDownloadBytes(); limit > 0 && img.FileSize > limit {
			return true
		}
	}
	_, seen := wp.oversizedIDs.Load(img.ID)
	return seen
}

// masterOnDisk returns the master path for id/ext if the file exists.
func (wp *Plugin) masterOnDisk(id, ext string) (string, bool) {
	if wp.fm == nil || ext == "" {
		return "", false
	}
	path, err := wp.fm.GetMasterPath(id, ext)
	if err != nil {
		return "", false
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return "", false
	}
	return path, true
}

// reuseCachedMaster checks whether img can be served from disk without a download:
// its master file exists and the retained index or the active store already holds its
// enriched metadata. On success img is replaced by the merged metadata.
func (wp *Plugin) reuseCachedMaster(img *provider.Image) reuseInfo {
	rs, ok := wp.store.(retainedStore)
	if !ok || wp.fm == nil {
		return reuseInfo{}
	}

	var cached provider.Image
	var ext string
	fromRetained := false
	if e, ok := rs.GetRetained(img.ID); ok {
		cached, ext, fromRetained = e.Image, e.Ext, true
	} else if existing, ok := wp.store.GetByID(img.ID); ok {
		cached, ext = existing, masterExtFor(existing)
	} else {
		return reuseInfo{}
	}

	masterPath, ok := wp.masterOnDisk(img.ID, ext)
	if !ok {
		if fromRetained {
			// Disk is the source of truth: the file is gone, so is the entry.
			rs.DropRetained(img.ID)
		}
		return reuseInfo{}
	}

	*img = mergeCachedMetadata(*img, cached)
	return reuseInfo{ok: true, fromRetained: fromRetained, masterPath: masterPath}
}

// cleanupFailedJob removes what a failed job is responsible for: only the files the job
// itself created (a downloaded master, generated derivatives). Files that already existed
// belong to the active store or the retained index and are never touched, with one
// exception: a cached master that proved undecodable is removed (and its retained entry
// dropped) so a normal, paced download can replace it. The job still holds its lease.
func (wp *Plugin) cleanupFailedJob(img provider.Image, reuse reuseInfo, created *jobArtifacts, err error) {
	if wp.fm == nil {
		return
	}
	for _, p := range created.derivatives {
		_ = os.Remove(p)
	}
	if created.master && created.masterPath != "" {
		log.Debugf("Job failed for %s (Err: %v), removing its download...", img.ID, err)
		_ = os.Remove(created.masterPath)
	}
	if reuse.ok && errors.Is(err, errCorruptMaster) {
		log.Printf("Cached master for %s is not a valid image; removing it so it can be downloaded again.", img.ID)
		if rmErr := os.Remove(reuse.masterPath); rmErr != nil && !os.IsNotExist(rmErr) {
			log.Printf("Failed to remove corrupt master %s: %v", reuse.masterPath, rmErr)
		}
		if reuse.fromRetained {
			if rs, ok := wp.store.(retainedStore); ok {
				rs.DropRetained(img.ID)
			}
		}
	}
}

// updateRetainedMetadata stores what a failed re-admission learned on the retained entry.
func (wp *Plugin) updateRetainedMetadata(img provider.Image) {
	if rs, ok := wp.store.(retainedStore); ok {
		rs.UpdateRetainedMetadata(img)
	}
}

// mergeCachedMetadata combines a fresh job image with the enriched copy we already have.
// The cached copy wins for enriched/user metadata; the job supplies its query linkage and
// anything the cache lacks. Derivatives are always recomputed.
func mergeCachedMetadata(job, cached provider.Image) provider.Image {
	out := copyImageMaps(cached)
	if job.SourceQueryID != "" {
		out.SourceQueryID = job.SourceQueryID
	}
	if out.Path == "" {
		out.Path = job.Path
	}
	if out.ViewURL == "" {
		out.ViewURL = job.ViewURL
	}
	if out.Attribution == "" {
		out.Attribution = job.Attribution
	}
	if out.Title == "" {
		out.Title = job.Title
	}
	if out.Artist == "" {
		out.Artist = job.Artist
	}
	if out.Year == "" {
		out.Year = job.Year
	}
	if out.DownloadLocation == "" {
		out.DownloadLocation = job.DownloadLocation
	}
	if out.Provider == "" {
		out.Provider = job.Provider
	}
	if out.FileType == "" {
		out.FileType = job.FileType
	}
	if out.Width == 0 || out.Height == 0 {
		out.Width, out.Height = job.Width, job.Height
	}
	if job.FileSize > 0 {
		out.FileSize = job.FileSize
	}
	for k, v := range job.ProcessingFlags {
		if out.ProcessingFlags == nil {
			out.ProcessingFlags = make(map[string]bool)
		}
		if _, exists := out.ProcessingFlags[k]; !exists {
			out.ProcessingFlags[k] = v
		}
	}
	out.DerivativePaths = nil
	out.FilePath = ""
	return out
}

// displayedImageIDs snapshots the image currently set on every monitor.
// Lock order: wp.monMu is released before each mc.mu is taken (never held across Fyne).
func (wp *Plugin) displayedImageIDs() map[string]bool {
	wp.monMu.RLock()
	mcs := make([]*MonitorController, 0, len(wp.Monitors))
	for _, mc := range wp.Monitors {
		mcs = append(mcs, mc)
	}
	wp.monMu.RUnlock()

	ids := make(map[string]bool, len(mcs))
	for _, mc := range mcs {
		mc.mu.RLock()
		if mc.State.CurrentID != "" {
			ids[mc.State.CurrentID] = true
		}
		if mc.State.CurrentImage.ID != "" {
			ids[mc.State.CurrentImage.ID] = true
		}
		mc.mu.RUnlock()
	}
	return ids
}

// runCacheMaintenance deletes unknown files (when cleanOrphans) and evicts retained
// masters down to the disk budget. Images on screen are never deleted. Blocking; call
// from a background goroutine.
func (wp *Plugin) runCacheMaintenance(cleanOrphans bool) {
	if wp.fm == nil || wp.store == nil {
		return
	}
	rs, ok := wp.store.(retainedStore)
	if !ok {
		if cleanOrphans {
			wp.fm.CleanupOrphans(wp.store.GetKnownIDs())
		}
		return
	}

	protected := wp.displayedImageIDs()
	if cleanOrphans {
		// Live-checked per ID at delete time: in-flight downloads are never touched.
		rs.CleanupOrphanFiles(protected)
	}
	rs.EnforceDiskBudget(wp.cacheDiskBudgetBytes(), protected)
}

// scheduleCacheMaintenance runs an eviction pass in a tracked background goroutine.
func (wp *Plugin) scheduleCacheMaintenance(cleanOrphans bool) {
	if wp.fm == nil {
		return
	}
	wp.fm.runAsync(func() { wp.runCacheMaintenance(cleanOrphans) })
}

// resyncStore reconciles the store with the configuration, then cleans the derivatives of
// any images Sync moved out of rotation and re-checks the disk budget right away (instead
// of waiting for the nightly run).
func (wp *Plugin) resyncStore() {
	wp.syncStoreWithConfig()
	wp.scheduleCacheMaintenance(false)
}

// removeQueryFromRotation takes a query's images out of rotation, keeping their masters.
// detach unlinks the retained entries from the query (deleted, or URL changed).
func (wp *Plugin) removeQueryFromRotation(queryID string, detach bool) {
	if rs, ok := wp.store.(retainedStore); ok {
		rs.RetainByQueryID(queryID, detach)
		wp.scheduleCacheMaintenance(false)
		return
	}
	wp.store.RemoveByQueryID(queryID)
}

// readmitBatch tracks the re-admission jobs submitted for one re-enabled query.
type readmitBatch struct {
	ids []string
	wg  sync.WaitGroup
}

// readmitShuffleTimeout is only a safety net: the batch normally completes through the
// jobs' Done signals (admitted, rejected or dropped).
const readmitShuffleTimeout = 30 * time.Second

// wait blocks until every submitted job has finished, ctx ends, or the safety timeout.
func (b *readmitBatch) wait(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	case <-time.After(readmitShuffleTimeout):
		log.Printf("Re-admission did not finish within %v; refreshing rotation anyway.", readmitShuffleTimeout)
	}
}

// readmitRetained submits a re-enabled query's retained images for re-admission straight
// from disk: no provider API call and no download (derivatives are regenerated locally).
// At most CacheSize entries (most recently used first) are submitted.
func (wp *Plugin) readmitRetained(queryID string) *readmitBatch {
	batch := &readmitBatch{}
	rs, ok := wp.store.(retainedStore)
	if !ok {
		return batch
	}
	limit := 0
	if wp.cfg != nil {
		limit = wp.cfg.GetCacheSize().Size()
	}
	entries := rs.RetainedForQuery(queryID, limit)
	if len(entries) == 0 {
		return batch
	}

	wp.downloadMutex.RLock()
	submitter := wp.jobSubmitter
	wp.downloadMutex.RUnlock()
	if submitter == nil {
		return batch
	}

	queryCtx := wp.GetOrCreateQueryContext(queryID)
	for _, e := range entries {
		img := e.Image
		img.SourceQueryID = queryID
		if wp.cfg != nil && wp.cfg.InAvoidSet(img.ID) {
			continue
		}
		batch.wg.Add(1)
		var once sync.Once
		done := func() { once.Do(batch.wg.Done) }
		// No provider: local work skips the API/download pacing lanes entirely.
		if !submitter.Submit(queryCtx, DownloadJob{Ctx: queryCtx, Image: img, ReuseOnly: true, Done: done}) {
			done()
			break
		}
		batch.ids = append(batch.ids, img.ID)
	}
	if len(batch.ids) > 0 {
		log.Printf("Query %s re-enabled: re-admitting %d retained images from disk (no downloads).", queryID, len(batch.ids))
	}
	return batch
}

// reenableQuery re-admits a query's retained images, refreshes shuffles once every
// re-admission job has finished, and fetches from the provider only if the rotation is
// still short of CacheSize after that.
func (wp *Plugin) reenableQuery(queryID string) {
	batch := wp.readmitRetained(queryID)
	if len(batch.ids) > 0 {
		batch.wait(wp.GetOrCreateQueryContext(queryID))
		wp.dispatch(-1, CmdUpdateShuffle)
		admitted := 0
		if rs, ok := wp.store.(retainedStore); ok {
			for _, id := range batch.ids {
				if rs.Exists(id) {
					admitted++
				}
			}
		}
		if wp.rotationFilledBy(admitted) {
			log.Debugf("[Plugin] Query %s: %d re-admitted images fill the rotation; skipping provider fetch.", queryID, admitted)
			return
		}
	}
	wp.RequestFetch()
}

// rotationFilledBy reports whether n re-admitted images fill the rotation (CacheSize).
func (wp *Plugin) rotationFilledBy(n int) bool {
	if wp.cfg == nil || n == 0 {
		return false
	}
	limit := wp.cfg.GetCacheSize().Size()
	return limit > 0 && n >= limit
}
