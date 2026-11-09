package wallpaper

import (
	"context"
	"sync"
	"time"

	"github.com/dixieflatline76/Spice/v2/pkg/provider"
	"github.com/dixieflatline76/Spice/v2/util/log"
	"golang.org/x/time/rate"
)

// Startup restore of dormant images.
//
// An image in rotation whose derivatives were invalidated (for example by a settings
// change, or by the processing-flags mismatch fixed alongside this) keeps its master on
// disk but has no fitted file, so it sits in no resolution bucket and never shows. Normal
// backlog healing only re-fits it if a fetch happens to return it again.
//
// On every startup, after the monitors exist and after the first Sync, such images are
// queued as ReuseOnly jobs: local re-fit from the cached master through the pipeline (job
// lease, store admission), with no network and no enrichment. No persisted marker is used:
// the check is a scan of the store plus one stat per dormant image, so when nothing is
// dormant it does nothing, and it also repairs images left dormant by a settings change in
// an earlier session.

// defaultRestorePace spaces restore jobs for providers without a process limiter.
const defaultRestorePace = 200 * time.Millisecond

// restoreLimiter returns the limiter that paces restore jobs for an image: its provider's
// normal process limiter (shared with downloads) when it has one, else a default one.
func (wp *Plugin) restoreLimiter(providerID string) *rate.Limiter {
	if p, ok := wp.providers[providerID]; ok && p != nil {
		if l := wp.getProcessLimiter(p); l != nil {
			return l
		}
	}
	wp.restorePaceMu.Lock()
	defer wp.restorePaceMu.Unlock()
	if wp.restoreFallbackLimiter == nil {
		pace := wp.restorePace
		if pace <= 0 {
			pace = defaultRestorePace
		}
		wp.restoreFallbackLimiter = rate.NewLimiter(rate.Every(pace), 1)
	}
	return wp.restoreFallbackLimiter
}

// dormantImages returns images in rotation that lack a derivative for a connected
// monitor but whose master is on disk (restorable), and how many lack a master.
// Images that fit no connected monitor keep their existing incompatible handling.
func (wp *Plugin) dormantImages() (restorable []provider.Image, noMaster int) {
	resolutions := wp.getResolutionsForDerivatives()
	for _, img := range wp.store.List() {
		if wp.cfg != nil && wp.cfg.InAvoidSet(img.ID) {
			continue
		}
		if wp.allMonitorDerivativesExist(img) || taggedIncompatibleWithAll(img, resolutions) {
			continue
		}
		if _, ok := wp.masterOnDisk(img.ID, masterExtFor(img)); !ok {
			noMaster++
			continue
		}
		restorable = append(restorable, img)
	}
	return restorable, noMaster
}

// restoreDormantImages re-fits dormant images from their cached masters and returns how
// many are back in rotation. Blocking; Activate runs it in a goroutine.
func (wp *Plugin) restoreDormantImages(ctx context.Context) int {
	if _, ok := wp.store.(retainedStore); !ok || wp.fm == nil {
		return 0
	}
	wp.downloadMutex.RLock()
	submitter := wp.jobSubmitter
	wp.downloadMutex.RUnlock()
	if submitter == nil {
		return 0
	}

	candidates, noMaster := wp.dormantImages()
	if len(candidates) == 0 {
		if noMaster > 0 {
			log.Debugf("[Restore] %d images in rotation lack derivatives and a cached master; normal fetches will heal them.", noMaster)
		}
		return 0
	}
	log.Debugf("[Restore] Re-fitting %d dormant images from cache...", len(candidates))

	var wg sync.WaitGroup
	submitted := make([]string, 0, len(candidates))
	for _, img := range candidates {
		if err := wp.restoreLimiter(img.Provider).Wait(ctx); err != nil {
			break // shutting down
		}
		wg.Add(1)
		var once sync.Once
		done := func() { once.Do(wg.Done) }
		// No provider on the job: ReuseOnly work never touches the network or enrichment,
		// and the pacing above already used the provider's process limiter.
		if !submitter.Submit(ctx, DownloadJob{Ctx: ctx, Image: img, ReuseOnly: true, Done: done}) {
			done()
			break
		}
		submitted = append(submitted, img.ID)
	}

	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-ctx.Done():
		return 0
	}

	restored := 0
	for _, id := range submitted {
		if img, ok := wp.store.GetByID(id); ok && wp.allMonitorDerivativesExist(img) {
			restored++
		}
	}
	log.Printf("Restored %d images to rotation from cache (%d dormant, %d without a cached master).", restored, len(candidates), noMaster)
	if restored > 0 {
		wp.dispatch(-1, CmdUpdateShuffle)
	}
	return restored
}
