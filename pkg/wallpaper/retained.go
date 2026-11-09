package wallpaper

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dixieflatline76/Spice/v2/pkg/provider"
	"github.com/dixieflatline76/Spice/v2/util/log"
)

// RetainedEntry records a master file that left rotation but is kept on disk for reuse.
// It carries the enriched metadata needed to re-admit the image without a provider call.
//
// Invariant: an ID is either in the active store or in the retained index, never both.
type RetainedEntry struct {
	// Image is the enriched image as it was in the store. DerivativePaths and FilePath are
	// cleared (derivatives are regenerated locally on re-admission); Tuning, ProcessingFlags,
	// dimensions and attribution are kept.
	Image provider.Image `json:"image"`
	// Ext is the master file extension on disk (e.g. ".jpg").
	Ext string `json:"ext"`
	// Bytes is the logical size of the master file (from os.Stat / directory listing).
	Bytes int64 `json:"bytes"`
	// LastUsed is when the image was last in rotation. LRU eviction orders by this value,
	// never by filesystem access times (atime is disabled or relaxed on Windows/macOS/Linux).
	LastUsed time.Time `json:"last_used"`
	// Provider is the provider ID that produced the image.
	Provider string `json:"provider"`
	// DerivativesCleared is true once the fitted per-monitor files have been deleted.
	DerivativesCleared bool `json:"derivatives_cleared,omitempty"`
}

// EvictionResult summarizes one EnforceDiskBudget pass.
type EvictionResult struct {
	TotalBytes   int64 // Master bytes on disk (active + retained) after the pass
	BudgetBytes  int64
	EvictedIDs   []string
	FailedIDs    []string // Eviction attempted but files could not be removed; entry kept for retry
	DroppedStale int      // Retained entries dropped because their master was already gone
}

// masterExtFor returns the master file extension the downloader uses for img. It is
// always lowercase so the same file is found on case-sensitive and case-insensitive
// filesystems alike (a ".JPG" URL is stored as ".jpg").
func masterExtFor(img provider.Image) string {
	ext := strings.ToLower(filepath.Ext(extractFilenameFromURL(img.Path)))
	if ext == "" {
		if img.FileType == "image/png" {
			ext = ".png"
		} else {
			ext = ".jpg" // Default
		}
	}
	return ext
}

// retainedSnapshotLocked deep-copies the retained index for persistence. CALLER MUST HOLD s.mu.
func (s *ImageStore) retainedSnapshotLocked() map[string]RetainedEntry {
	snap := make(map[string]RetainedEntry, len(s.retained))
	for id, e := range s.retained {
		e.Image = copyImageMaps(e.Image)
		snap[id] = e
	}
	return snap
}

// copyImageMaps returns img with its map fields deep-copied.
func copyImageMaps(img provider.Image) provider.Image {
	if img.ProcessingFlags != nil {
		flags := make(map[string]bool, len(img.ProcessingFlags))
		for k, v := range img.ProcessingFlags {
			flags[k] = v
		}
		img.ProcessingFlags = flags
	}
	if img.DerivativePaths != nil {
		paths := make(map[string]string, len(img.DerivativePaths))
		for k, v := range img.DerivativePaths {
			paths[k] = v
		}
		img.DerivativePaths = paths
	}
	if img.Tuning != nil {
		tuning := make(map[string]provider.TuningOptions, len(img.Tuning))
		for k, v := range img.Tuning {
			tuning[k] = v
		}
		img.Tuning = tuning
	}
	return img
}

// newRetainedEntry builds a retained entry from an active image leaving rotation.
func newRetainedEntry(img provider.Image, now time.Time, detach bool) RetainedEntry {
	img = copyImageMaps(img)
	img.DerivativePaths = nil
	img.FilePath = ""
	// A re-admitted image is new to the rotation again (as it was when downloads were
	// deleted and re-fetched); keeping Seen would skew seenCount and the fetch heuristics.
	img.Seen = false
	if detach {
		img.SourceQueryID = ""
	}
	return RetainedEntry{
		Image:    img,
		Ext:      masterExtFor(img),
		LastUsed: now,
		Provider: img.Provider,
	}
}

// retainLocked moves images that are leaving rotation into the retained index.
// CALLER MUST HOLD s.mu.Lock(). Images already deleted by the user (avoid set) are skipped.
func (s *ImageStore) retainLocked(imgs []provider.Image, detach bool) []string {
	now := time.Now()
	ids := make([]string, 0, len(imgs))
	for _, img := range imgs {
		if s.idSet[img.ID] || s.avoidSet[img.ID] {
			continue
		}
		s.retained[img.ID] = newRetainedEntry(img, now, detach)
		ids = append(ids, img.ID)
	}
	return ids
}

// refreshRetainedSizes stats the masters of newly retained IDs to record ext and size.
// Entries without a master on disk are dropped (nothing to retain). File I/O happens
// outside s.mu; the results are applied under s.mu.
func (s *ImageStore) refreshRetainedSizes(ids []string) {
	if s.fm == nil || len(ids) == 0 {
		return
	}
	type found struct {
		ext  string
		size int64
		ok   bool
	}

	s.mu.RLock()
	want := make(map[string]string, len(ids))
	for _, id := range ids {
		if e, ok := s.retained[id]; ok {
			want[id] = e.Ext
		}
	}
	s.mu.RUnlock()

	results := make(map[string]found, len(want))
	for id, preferred := range want {
		ext, size, ok := s.fm.findMaster(id, preferred)
		results[id] = found{ext: ext, size: size, ok: ok}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, res := range results {
		e, ok := s.retained[id]
		if !ok {
			continue // Re-admitted meanwhile
		}
		if !res.ok {
			delete(s.retained, id)
			log.Debugf("[Retained] No master on disk for %s; not retaining.", id)
			continue
		}
		e.Ext = res.ext
		e.Bytes = res.size
		s.retained[id] = e
	}
	s.scheduleSaveLocked()
}

// GetRetained returns the retained entry for id. Entries whose files are being deleted are not returned.
func (s *ImageStore) GetRetained(id string) (RetainedEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.deleting[id] {
		return RetainedEntry{}, false
	}
	e, ok := s.retained[id]
	if !ok {
		return RetainedEntry{}, false
	}
	e.Image = copyImageMaps(e.Image)
	return e, true
}

// DropRetained removes an entry from the retained index (its master is gone).
func (s *ImageStore) DropRetained(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.retained[id]; ok {
		delete(s.retained, id)
		s.scheduleSaveLocked()
	}
}

// RetainedCount returns the number of retained entries.
func (s *ImageStore) RetainedCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.retained)
}

// RetainedForQuery returns up to limit retained entries still linked to queryID,
// most recently used first. limit <= 0 means no limit.
func (s *ImageStore) RetainedForQuery(queryID string, limit int) []RetainedEntry {
	s.mu.RLock()
	var out []RetainedEntry
	for id, e := range s.retained {
		if s.deleting[id] || e.Image.SourceQueryID != queryID || queryID == "" {
			continue
		}
		e.Image = copyImageMaps(e.Image)
		out = append(out, e)
	}
	s.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastUsed.Equal(out[j].LastUsed) {
			return out[i].LastUsed.After(out[j].LastUsed)
		}
		return out[i].Image.ID < out[j].Image.ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// DetachRetainedQuery unlinks retained entries from queryID so re-enabling (or re-creating)
// the query does not resurrect them. They stay reusable if a fetch returns them again.
func (s *ImageStore) DetachRetainedQuery(queryID string) {
	if queryID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for id, e := range s.retained {
		if e.Image.SourceQueryID == queryID {
			e.Image.SourceQueryID = ""
			s.retained[id] = e
			changed = true
		}
	}
	if changed {
		s.scheduleSaveLocked()
	}
}

// RetainByQueryID removes a query's images from rotation and keeps their master files as
// retained entries. With detach, the entries are unlinked from the query (query deleted or
// its URL changed) so they are only reused if a fetch returns them again.
func (s *ImageStore) RetainByQueryID(queryID string, detach bool) {
	s.mu.Lock()
	removed := s.removeQueryImagesLocked(queryID)
	ids := s.retainLocked(removed, detach)
	if detach {
		for id, e := range s.retained {
			if e.Image.SourceQueryID == queryID {
				e.Image.SourceQueryID = ""
				s.retained[id] = e
			}
		}
	}
	s.scheduleSaveLocked()
	s.mu.Unlock()

	log.Debugf("[Retained] Query %s left rotation: %d images retained on disk.", queryID, len(ids))
	s.refreshRetainedSizes(ids)
}

// KnownFileIDs returns every ID whose files belong in the cache directory: active
// images plus retained entries. Used by orphan cleanup.
func (s *ImageStore) KnownFileIDs() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	known := make(map[string]bool, len(s.idSet)+len(s.retained))
	for id := range s.idSet {
		known[id] = true
	}
	for id := range s.retained {
		known[id] = true
	}
	for id := range s.leases {
		known[id] = true
	}
	for id := range s.displayRefs {
		known[id] = true
	}
	return known
}

// UpdateRetainedMetadata records what a failed re-admission learned (probed dimensions,
// incompatibility tags) on the retained entry, so the next attempt does not redo it.
func (s *ImageStore) UpdateRetainedMetadata(img provider.Image) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.retained[img.ID]
	if !ok {
		return false
	}
	if img.Width > 0 && img.Height > 0 {
		e.Image.Width, e.Image.Height = img.Width, img.Height
	}
	for k, v := range img.ProcessingFlags {
		if strings.HasPrefix(k, "incompatible:") && v {
			if e.Image.ProcessingFlags == nil {
				e.Image.ProcessingFlags = make(map[string]bool)
			}
			e.Image.ProcessingFlags[k] = true
		}
	}
	s.retained[img.ID] = e
	s.scheduleSaveLocked()
	return true
}

// EnforceDiskBudget keeps the master files of active + retained images within budget bytes
// and deletes the derivatives of retained images.
//
//   - Retained entries are evicted least-recently-used first (by LastUsed in the index).
//   - Active images are never evicted; neither are IDs in protected (e.g. on screen), nor a
//     retained ID that differs only by case from an active/protected ID (on a
//     case-insensitive filesystem they share a file; the rule is applied on every OS).
//   - Every deletion goes through a per-ID delete claim with a live check at delete time
//     (see cache_lease.go): an ID that a job holds, or that is back in rotation, is skipped.
//   - An entry is removed from the index only after its files are gone, inside the claim.
//     A failed delete (e.g. file open on Windows) is logged and retried on the next pass.
//   - Retained entries whose master is already missing are dropped from the index.
//
// File I/O runs without holding s.mu. A call made while a pass is running makes that pass
// run once more (so changes made during a pass are never missed).
func (s *ImageStore) EnforceDiskBudget(budget int64, protected map[string]bool) EvictionResult {
	res := EvictionResult{BudgetBytes: budget}
	if s.fm == nil {
		return res
	}
	for {
		if !s.evictMu.TryLock() {
			// The running pass may have scanned before the latest change: it runs again.
			s.evictAgain.Store(true)
			log.Debugf("[Retained] Eviction already in progress; it will run again.")
			return res
		}
		s.evictAgain.Store(false)
		res = s.enforceDiskBudgetOnce(budget, protected)
		s.evictMu.Unlock()
		if !s.evictAgain.Load() {
			return res
		}
	}
}

// enforceDiskBudgetOnce is one EnforceDiskBudget pass. CALLER MUST HOLD s.evictMu.
func (s *ImageStore) enforceDiskBudgetOnce(budget int64, protected map[string]bool) EvictionResult {
	res := EvictionResult{BudgetBytes: budget}

	masters, err := s.fm.ScanMasters()
	if err != nil {
		log.Printf("Store: Failed to scan cache for eviction: %v", err)
		return res
	}

	protectedFold := make(map[string]bool, len(protected))
	for id, ok := range protected {
		if ok {
			protectedFold[strings.ToLower(id)] = true
		}
	}

	// Phase 1 (locked, no I/O): reconcile the index with the disk scan and plan.
	s.mu.Lock()
	activeFold := make(map[string]bool, len(s.idSet)+len(s.leases))
	var total int64
	for id := range s.idSet {
		activeFold[strings.ToLower(id)] = true
		if mf, ok := masters[id]; ok {
			total += mf.Size
		}
	}
	for id := range s.leases {
		activeFold[strings.ToLower(id)] = true
	}
	for id := range s.displayRefs {
		activeFold[strings.ToLower(id)] = true
	}
	changed := false
	candidates := make([]RetainedEntry, 0, len(s.retained))
	missing := make(map[string]string) // id -> ext; re-checked on disk before dropping
	for id, e := range s.retained {
		if s.deleting[id] || s.heldLocked(id) {
			continue
		}
		mf, ok := masters[id]
		if !ok {
			missing[id] = e.Ext
			continue
		}
		if e.Bytes != mf.Size || e.Ext != mf.Ext {
			e.Bytes, e.Ext = mf.Size, mf.Ext
			s.retained[id] = e
			changed = true
		}
		total += mf.Size
		candidates = append(candidates, e)
	}
	if changed {
		s.scheduleSaveLocked()
	}
	s.mu.Unlock()

	// The scan above ran before the lock: a master that appeared since (e.g. a download
	// admitted and then retained in between) is only dropped after a fresh disk check.
	if len(missing) > 0 {
		gone := make([]string, 0, len(missing))
		for id, ext := range missing {
			if _, _, found := s.fm.findMaster(id, ext); !found {
				gone = append(gone, id)
			}
		}
		s.mu.Lock()
		for _, id := range gone {
			if _, ok := s.retained[id]; ok && !s.deleting[id] && !s.heldLocked(id) {
				delete(s.retained, id)
				res.DroppedStale++
			}
		}
		if len(gone) > 0 {
			s.scheduleSaveLocked()
		}
		s.mu.Unlock()
	}

	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].LastUsed.Equal(candidates[j].LastUsed) {
			return candidates[i].LastUsed.Before(candidates[j].LastUsed)
		}
		return candidates[i].Image.ID < candidates[j].Image.ID
	})

	var victims, derivCleanup []string
	victimSet := make(map[string]bool)
	projected := total
	for _, e := range candidates {
		id := e.Image.ID
		fold := strings.ToLower(id)
		if protected[id] || protectedFold[fold] || activeFold[fold] {
			continue
		}
		if projected > budget {
			victims = append(victims, id)
			victimSet[id] = true
			projected -= e.Bytes
		}
	}
	for _, e := range candidates {
		id := e.Image.ID
		if !e.DerivativesCleared && !victimSet[id] && !protected[id] && !protectedFold[strings.ToLower(id)] {
			derivCleanup = append(derivCleanup, id)
		}
	}
	if len(victims) == 0 && len(derivCleanup) == 0 {
		res.TotalBytes = total
		return res
	}

	// Phase 2: one listing of the files involved, then one claim per ID.
	want := make(map[string]bool, len(victims)+len(derivCleanup))
	for _, id := range victims {
		want[id] = true
	}
	for _, id := range derivCleanup {
		want[id] = true
	}
	files := s.fm.ListFilesByID(func(id string) bool { return want[id] })

	for _, id := range victims {
		f := files[id]
		var paths []string
		if f != nil {
			paths = append(append(paths, f.Masters...), f.Derivatives...)
		}
		claimed := s.withDeleteClaim(id, deleteEvict, func() error {
			return s.fm.removePaths(paths, 0)
		}, func(err error) {
			if err != nil {
				log.Printf("Store: Failed to evict retained image %s (will retry): %v", id, err)
				res.FailedIDs = append(res.FailedIDs, id)
				return
			}
			if e, ok := s.retained[id]; ok {
				delete(s.retained, id)
				total -= e.Bytes
			}
			res.EvictedIDs = append(res.EvictedIDs, id)
			s.scheduleSaveLocked()
		})
		if !claimed {
			log.Debugf("[Retained] Skipping eviction of %s: in use.", id)
		}
	}
	for _, id := range derivCleanup {
		f := files[id]
		if f == nil || len(f.Derivatives) == 0 {
			s.mu.Lock()
			if e, ok := s.retained[id]; ok && s.canDeleteLocked(id, deleteRetainedDerivatives) {
				e.DerivativesCleared = true
				s.retained[id] = e
				s.scheduleSaveLocked()
			}
			s.mu.Unlock()
			continue
		}
		paths := append([]string(nil), f.Derivatives...)
		s.withDeleteClaim(id, deleteRetainedDerivatives, func() error {
			return s.fm.removePaths(paths, 0)
		}, func(err error) {
			if err != nil {
				log.Debugf("[Retained] Could not delete derivatives of %s (will retry): %v", id, err)
				return
			}
			if e, ok := s.retained[id]; ok {
				e.DerivativesCleared = true
				s.retained[id] = e
				s.scheduleSaveLocked()
			}
		})
	}

	res.TotalBytes = total
	if len(res.EvictedIDs) > 0 || len(res.FailedIDs) > 0 {
		log.Printf("Store: Disk budget eviction removed %d retained images (%d failed). Cache masters: %d MB of %d MB budget.",
			len(res.EvictedIDs), len(res.FailedIDs), total/bytesPerMB, budget/bytesPerMB)
	}
	return res
}

// applyRenamedPaths points cached paths at files renamed by NormalizeExtensions and
// lowercases retained master extensions. Returns true if anything changed.
func applyRenamedPaths(images []provider.Image, retained map[string]RetainedEntry, renamed map[string]string) bool {
	changed := false
	for i := range images {
		if np, ok := renamed[images[i].FilePath]; ok {
			images[i].FilePath = np
			changed = true
		}
		for res, p := range images[i].DerivativePaths {
			if np, ok := renamed[p]; ok {
				images[i].DerivativePaths[res] = np
				changed = true
			}
		}
	}
	for id, e := range retained {
		if lower := strings.ToLower(e.Ext); lower != e.Ext {
			e.Ext = lower
			retained[id] = e
			changed = true
		}
	}
	return changed
}

// RetainActive moves an active image out of rotation into the retained index, carrying
// the dimensions and incompatibility tags a job just learned. Used when a cached image
// fits none of the connected monitors: its master stays (owned by the retained index,
// evictable) instead of an unusable active entry pinning it forever.
func (s *ImageStore) RetainActive(img provider.Image) bool {
	s.mu.Lock()
	idx := -1
	for i, existing := range s.images {
		if existing.ID == img.ID {
			idx = i
			break
		}
	}
	if idx == -1 {
		s.mu.Unlock()
		return false
	}
	existing := s.images[idx]
	if img.Width > 0 && img.Height > 0 {
		existing.Width, existing.Height = img.Width, img.Height
	}
	for k, v := range img.ProcessingFlags {
		if v && strings.HasPrefix(k, "incompatible:") {
			if existing.ProcessingFlags == nil {
				existing.ProcessingFlags = make(map[string]bool)
			}
			existing.ProcessingFlags[k] = true
		}
	}
	s.images = append(s.images[:idx], s.images[idx+1:]...)
	delete(s.idSet, img.ID)
	s.rebuildInternalStateLocked()
	ids := s.retainLocked([]provider.Image{existing}, false)
	s.scheduleSaveLocked()
	s.mu.Unlock()
	log.Debugf("[Retained] %s fits no connected monitor; moved out of rotation (master kept).", img.ID)
	s.refreshRetainedSizes(ids)
	return true
}

// RetagQueryIDs re-links images (in rotation and retained) whose SourceQueryID was renamed
// (old -> new), e.g. by the query-ID migration. Returns how many were re-tagged.
func (s *ImageStore) RetagQueryIDs(remap map[string]string) int {
	if len(remap) == 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for i := range s.images {
		if newID, ok := remap[s.images[i].SourceQueryID]; ok && s.images[i].SourceQueryID != "" {
			s.images[i].SourceQueryID = newID
			n++
		}
	}
	for id, e := range s.retained {
		if newID, ok := remap[e.Image.SourceQueryID]; ok && e.Image.SourceQueryID != "" {
			e.Image.SourceQueryID = newID
			s.retained[id] = e
			n++
		}
	}
	if n > 0 {
		s.scheduleSaveLocked()
	}
	return n
}
