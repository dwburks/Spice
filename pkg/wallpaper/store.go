package wallpaper

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dixieflatline76/Spice/v2/pkg/provider"
	"github.com/dixieflatline76/Spice/v2/util/log"
)

// ImageStore is a thread-safe storage for wallpaper images.
type ImageStore struct {
	mu       sync.RWMutex
	images   []provider.Image
	idSet    map[string]bool
	pathSet  map[string]int // filePath -> index for O(1) MarkSeen
	avoidSet map[string]bool

	seenCount int // Pre-calculated O(1) SeenCount

	cachePath string
	fm        *FileManager
	asyncSave bool

	saveTimer *time.Timer
	saveMu    sync.Mutex

	// Testing hook
	saveFunc func()

	// Update Notification
	updateCh chan struct{}

	// Resolution Buckets (Performance optimization)
	// Map "WidthxHeight" -> List of Image IDs compatible with that resolution
	resolutionBuckets map[string][]string

	debounceDuration time.Duration
	os               OS

	// QueryActiveFunc checks if a given source query ID is still active in the configuration.
	QueryActiveFunc func(string) bool

	// retained indexes master files that left rotation but stay on disk for reuse
	// (see retained.go). Guarded by mu. An ID is never in both idSet and retained.
	retained map[string]RetainedEntry
	// deleting marks IDs whose files a deleter currently owns (cache_lease.go). Guarded by mu.
	deleting map[string]bool
	// derivDeleting marks IDs whose derivatives (only) are being deleted. Guarded by mu.
	derivDeleting map[string]bool
	// leases marks IDs a pipeline job currently owns (cache_lease.go). Guarded by mu.
	leases map[string]bool
	// Display references (cache_lease.go), guarded by mu: images on screen or being
	// switched to per monitor, reference-counted per ID. Deleters never touch them.
	displayRefs      map[string]int
	shownByMonitor   map[int]string
	pendingByMonitor map[int]string
	// pendingDelete marks unowned IDs whose delete was refused while a job or a display
	// held them; it is completed when the last holder lets go. Guarded by mu.
	pendingDelete map[string]bool
	// Debounced disk-budget check after admissions (cache_lease.go). budgetMu is a leaf lock.
	budgetMu    sync.Mutex
	budgetTimer *time.Timer
	budgetDelay time.Duration
	evictAgain  atomic.Bool
	// evictMu serializes EnforceDiskBudget passes (TryLock only; taken before mu, never
	// while holding mu, and never nested with wp.monMu or mc.mu).
	evictMu sync.Mutex

	// Persistence (cache_persist.go). writeMu serializes file writes; it is a leaf lock
	// that may be taken while holding mu (sync-save mode), never the reverse.
	writeMu          sync.Mutex
	saveGen          atomic.Uint64
	lastWrittenGen   uint64 // guarded by writeMu
	lastMirrorDigest string // guarded by writeMu: digest of the legacy mirror on disk
}

func NewImageStore() *ImageStore {
	store := &ImageStore{
		images:            make([]provider.Image, 0),
		idSet:             make(map[string]bool),
		pathSet:           make(map[string]int),
		avoidSet:          make(map[string]bool),
		asyncSave:         true,
		debounceDuration:  2 * time.Second,
		updateCh:          make(chan struct{}),
		resolutionBuckets: make(map[string][]string),
		retained:          make(map[string]RetainedEntry),
		deleting:          make(map[string]bool),
		derivDeleting:     make(map[string]bool),
		leases:            make(map[string]bool),
		displayRefs:       make(map[string]int),
		shownByMonitor:    make(map[int]string),
		pendingByMonitor:  make(map[int]string),
		pendingDelete:     make(map[string]bool),
		budgetDelay:       defaultBudgetCheckDelay,
	}
	return store
}

func (s *ImageStore) SetDebounceDuration(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.debounceDuration = d
}

// SetQueryActiveFunc sets the callback used to check if an image's source query is still active
func (s *ImageStore) SetQueryActiveFunc(fn func(string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.QueryActiveFunc = fn
}

// SetOS sets the OS interface for filesystem operations.
func (s *ImageStore) SetOS(os OS) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.os = os
}

func (s *ImageStore) getStatFunc() func(string) (os.FileInfo, error) {
	if s.os != nil {
		return s.os.Stat
	}
	return os.Stat
}

func (s *ImageStore) SetAsyncSave(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asyncSave = enabled
}

func (s *ImageStore) SetFileManager(fm *FileManager, cacheFile string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fm = fm
	s.cachePath = cacheFile
}

// replace performs a full struct replacement for an existing image.
// This is unexported because full replacement is dangerous — callers who only
// need to change one field should use SetFavorited, SetTuningOptions, or ClearDerivatives.
// The only legitimate caller is stateManagerLoop (pipeline), which always has
// a fully-populated Image from ProcessImageJob.
func (s *ImageStore) replace(img provider.Image) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.deleting[img.ID] || s.derivDeleting[img.ID] {
		return false
	}
	for i, existing := range s.images {
		if existing.ID == img.ID {
			// Diagnostic: Log when DerivativePaths are being cleared.
			// This is legitimate when files are missing (applyImage stale detection),
			// but suspicious if it happens during pipeline processing.
			if len(img.DerivativePaths) == 0 && len(existing.DerivativePaths) > 0 {
				log.Debugf("[Store] WARNING: replace() clearing DerivativePaths for %s (had %d paths)", img.ID, len(existing.DerivativePaths))
			}
			// Preserve user-set Tuning - the store is authoritative for user metadata.
			// The pipeline never writes Tuning, so existing store values always win.
			// The else branch ensures that if the user removed all tuning
			// while the pipeline was in-flight, stale tuning from MergeExistingMetadata
			// are not resurrected.
			if len(existing.Tuning) > 0 {
				img.Tuning = make(map[string]provider.TuningOptions, len(existing.Tuning))
				for k, v := range existing.Tuning {
					img.Tuning[k] = v
				}
			} else {
				img.Tuning = nil
			}
			// Incremental bucket update: remove old entries, add new ones
			s.removeFromBucketsLocked(existing.ID, existing.DerivativePaths)
			s.images[i] = img
			s.addToBucketsLocked(img.ID, img.DerivativePaths)
			s.scheduleSaveLocked()
			return true
		}
	}
	return false
}

// SetFavorited updates only the IsFavorited flag for an image.
func (s *ImageStore) SetFavorited(id string, favorited bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.images {
		if s.images[i].ID == id {
			s.images[i].IsFavorited = favorited
			s.scheduleSaveLocked()
			return true
		}
	}
	return false
}

// SetTuningOptions updates the tuning options for a specific resolution on an image.
// Pass an empty TuningOptions struct to clear the overrides.
func (s *ImageStore) SetTuningOptions(id string, resKey string, opts provider.TuningOptions) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.images {
		if s.images[i].ID == id {
			if s.images[i].Tuning == nil {
				s.images[i].Tuning = make(map[string]provider.TuningOptions)
			}
			// If it's the default/empty state, remove it from the map to clean up JSON
			defaultOpts := provider.TuningOptions{Anchor: provider.AnchorAuto}
			if opts == defaultOpts {
				delete(s.images[i].Tuning, resKey)
			} else {
				s.images[i].Tuning[resKey] = opts
			}
			s.scheduleSaveLocked()
			return true
		}
	}
	return false
}

// SetDerivativePath updates the derivative path for a specific resolution.
func (s *ImageStore) SetDerivativePath(id string, resKey string, path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.images {
		if s.images[i].ID == id {
			if s.images[i].DerivativePaths == nil {
				s.images[i].DerivativePaths = make(map[string]string)
			}
			s.images[i].DerivativePaths[resKey] = path
			s.addToBucketsLocked(id, map[string]string{resKey: path})
			s.scheduleSaveLocked()
			return true
		}
	}
	return false
}

// ClearDerivatives resets an image's DerivativePaths and ProcessingFlags.
// Used by monitor_controller when a derivative file is missing from disk.
func (s *ImageStore) ClearDerivatives(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.images {
		if s.images[i].ID == id {
			// Incremental bucket update: remove this image's entries
			s.removeFromBucketsLocked(id, s.images[i].DerivativePaths)
			s.images[i].DerivativePaths = make(map[string]string)
			s.images[i].ProcessingFlags = make(map[string]bool)
			s.scheduleSaveLocked()
			return true
		}
	}
	return false
}

// rebuildBucketsLocked re-scans ALL images and rebuilds resolution buckets from scratch.
// Use only for batch operations (e.g. RemoveByQueryID, Sync). For single-image
// mutations, prefer addToBucketsLocked/removeFromBucketsLocked.
// CALLER MUST HOLD s.mu.Lock().
func (s *ImageStore) rebuildBucketsLocked() {
	s.resolutionBuckets = make(map[string][]string)
	for _, img := range s.images {
		for res := range img.DerivativePaths {
			s.resolutionBuckets[res] = append(s.resolutionBuckets[res], img.ID)
		}
	}
}

// addToBucketsLocked adds a single image's derivative paths to the resolution buckets.
// CALLER MUST HOLD s.mu.Lock().
func (s *ImageStore) addToBucketsLocked(id string, derivativePaths map[string]string) {
	for res := range derivativePaths {
		s.resolutionBuckets[res] = append(s.resolutionBuckets[res], id)
	}
}

// removeFromBucketsLocked removes a single image's entries from the resolution buckets.
// CALLER MUST HOLD s.mu.Lock().
func (s *ImageStore) removeFromBucketsLocked(id string, derivativePaths map[string]string) {
	for res := range derivativePaths {
		ids := s.resolutionBuckets[res]
		for j, bucketID := range ids {
			if bucketID == id {
				s.resolutionBuckets[res] = append(ids[:j], ids[j+1:]...)
				break
			}
		}
		// Clean up empty bucket entries
		if len(s.resolutionBuckets[res]) == 0 {
			delete(s.resolutionBuckets, res)
		}
	}
}

// scheduleSaveLocked handles persistence.
// CALLER MUST HOLD s.mu.Lock()
func (s *ImageStore) scheduleSaveLocked() {
	if !s.asyncSave {
		// Sync mode (tests): snapshot while locked and save immediately.
		if err := s.writeSnapshot(s.snapshotLocked()); err != nil {
			log.Printf("Store: Failed to save cache: %v", err)
		}
		return
	}
	s.scheduleSaveAfter(s.debounceDuration)
}

// scheduleSaveAfter (re)arms the debounced async save. Takes saveMu only.
func (s *ImageStore) scheduleSaveAfter(d time.Duration) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	if s.saveTimer != nil {
		s.saveTimer.Stop()
	}
	s.saveTimer = time.AfterFunc(d, func() {
		if err := s.SaveCache(); err != nil {
			log.Printf("Store: Cache save failed, retrying in %v: %v", saveRetryDelay, err)
			s.scheduleSaveAfter(saveRetryDelay)
		}
	})
}

// Add adds a new image to the store. Returns true if added, false if it was already present or blocked.
func (s *ImageStore) Add(img provider.Image) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.idSet[img.ID]; exists {
		// Favorites Upsert: selectively update only the fields the Favorites
		// provider can authoritatively set, preserving all store-managed metadata
		// (DerivativePaths, ProcessingFlags, Tuning, Width, Height, etc.).
		//
		// Uses non-empty-wins for Attribution/ViewURL because AddFavorite is
		// async (queues a job), but RequestFetch fires immediately after — the
		// fetch can race with the metadata.json write, arriving with empty fields.
		if img.SourceQueryID == FavoritesQueryID {
			for i, existing := range s.images {
				if existing.ID == img.ID {
					// Provider-authoritative fields: always update
					s.images[i].Provider = img.Provider
					s.images[i].SourceQueryID = img.SourceQueryID
					s.images[i].IsFavorited = true

					// Download URL: always update (Favorites provider serves via local API)
					if img.Path != "" {
						s.images[i].Path = img.Path
					}

					// Non-empty-wins: preserve existing if incoming is empty (race protection)
					if img.Attribution != "" {
						s.images[i].Attribution = img.Attribution
					}
					if img.ViewURL != "" {
						s.images[i].ViewURL = img.ViewURL
					}

					// Seen: preserve if already seen
					if existing.Seen && !img.Seen {
						s.images[i].Seen = true
					}

					// FilePath: update pathSet if changed
					if img.FilePath != "" && img.FilePath != existing.FilePath {
						if existing.FilePath != "" {
							delete(s.pathSet, existing.FilePath)
						}
						s.images[i].FilePath = img.FilePath
						s.pathSet[img.FilePath] = i
					}

					s.scheduleSaveLocked()
					return true
				}
			}
		}
		return false
	}

	// Ownership: never admit an ID whose files a deleter currently owns.
	if s.deleting[img.ID] || s.derivDeleting[img.ID] {
		log.Debugf("Store: Rejecting %s while its files are being deleted.", img.ID)
		return false
	}

	// Active Query Check: Reject images from queries disabled mid-download. The finished
	// download is kept as a retained entry (linked to its query) instead of being orphaned.
	if s.QueryActiveFunc != nil && img.SourceQueryID != "" {
		if !s.QueryActiveFunc(img.SourceQueryID) {
			log.Debugf("Store: Query %s is inactive; retaining finished image %s instead of admitting it.", img.SourceQueryID, img.ID)
			if ids := s.retainLocked([]provider.Image{img}, false); len(ids) > 0 {
				s.scheduleSaveLocked()
				if s.fm != nil {
					s.fm.runAsync(func() { s.refreshRetainedSizes(ids) })
				}
			}
			return false
		}
	}

	if s.avoidSet[img.ID] {
		return false
	}

	s.images = append(s.images, img)
	s.idSet[img.ID] = true
	if _, wasRetained := s.retained[img.ID]; wasRetained {
		// Re-admitted from the retained index: the master moves back to the active set.
		delete(s.retained, img.ID)
		log.Debugf("[Retained] Re-admitted %s to rotation.", img.ID)
	}
	if img.FilePath != "" {
		s.pathSet[img.FilePath] = len(s.images) - 1
	}
	if img.Seen {
		s.seenCount++
	}

	// Add to buckets
	for res := range img.DerivativePaths {
		s.resolutionBuckets[res] = append(s.resolutionBuckets[res], img.ID)
	}

	s.scheduleSaveLocked()
	s.notifyUpdateLocked()
	return true
}

// Get returns the image at the given index from the global list. Returns false if index is out of bounds.
func (s *ImageStore) Get(index int) (provider.Image, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if index >= 0 && index < len(s.images) {
		return s.images[index], true
	}
	return provider.Image{}, false
}

// GetByID returns the image with the given ID. Returns false if not found.
func (s *ImageStore) GetByID(id string) (provider.Image, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, img := range s.images {
		if img.ID == id {
			return img, true
		}
	}
	return provider.Image{}, false
}

// Exists checks if an image ID is already in the store.
func (s *ImageStore) Exists(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.idSet[id]
}

func (s *ImageStore) Contains(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.idSet[id]
}

// Count returns the total number of images in the store.
func (s *ImageStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.images)
}

func (s *ImageStore) MarkSeen(filePath string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, exists := s.pathSet[filePath]
	if !exists {
		// Fallback: This might happen if FilePath changed or wasn't indexed.
		// We still want to be robust but indexed is preferred.
		for i, img := range s.images {
			if img.FilePath == filePath {
				if !s.images[i].Seen {
					s.images[i].Seen = true
					s.seenCount++
					s.scheduleSaveLocked()
				}
				return
			}
		}
		return
	}

	if !s.images[idx].Seen {
		s.images[idx].Seen = true
		s.seenCount++
		s.scheduleSaveLocked()
	}
}

// Remove deletes an image from the store by its ID. It also deletes physical files asynchronously.
func (s *ImageStore) Remove(id string) (provider.Image, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, item := range s.images {
		if item.ID == id {
			idx = i
			break
		}
	}

	if idx == -1 {
		// Not in rotation, but possibly retained (e.g. still on screen after its query was
		// disabled): blocking it must drop the entry and its files too.
		e, retained := s.retained[id]
		if !retained {
			return provider.Image{}, false
		}
		delete(s.retained, id)
		s.avoidSet[id] = true
		s.scheduleSaveLocked()
		s.asyncDeleteUnownedIDs([]string{id})
		return e.Image, true
	}

	img := s.images[idx]
	if img.Seen {
		s.seenCount--
	}

	// Remove from slice
	s.images = append(s.images[:idx], s.images[idx+1:]...)
	delete(s.idSet, id)
	if img.FilePath != "" {
		delete(s.pathSet, img.FilePath)
	}

	// Rebuild pathSet indices for remaining (O(N) but Remove is rare)
	for i := idx; i < len(s.images); i++ {
		if s.images[i].FilePath != "" {
			s.pathSet[s.images[i].FilePath] = i
		}
	}

	s.removeFromBucketsLocked(id, img.DerivativePaths) // Incremental bucket removal
	s.avoidSet[id] = true
	s.scheduleSaveLocked()
	s.asyncDeleteUnownedIDs([]string{id})

	return img, true
}

// SeenCount returns the number of images that have been marked as seen.
func (s *ImageStore) SeenCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.seenCount
}

// Clear resets the store to an empty state, but preserves the avoid set (blocklist).
func (s *ImageStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.images = make([]provider.Image, 0)
	s.idSet = make(map[string]bool)
	s.pathSet = make(map[string]int)
	s.resolutionBuckets = make(map[string][]string)
	s.seenCount = 0
	s.scheduleSaveLocked()
}

// ResetFavorites clears the IsFavorited flag on all images in the store.
func (s *ImageStore) ResetFavorites() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.images {
		s.images[i].IsFavorited = false
	}
	s.scheduleSaveLocked()
}

func (s *ImageStore) Wipe() {
	s.Clear()
	s.mu.Lock()
	s.avoidSet = make(map[string]bool)
	s.retained = make(map[string]RetainedEntry)
	s.scheduleSaveLocked()
	s.mu.Unlock()
	if s.fm != nil {
		s.fm.runAsync(func() { s.CleanupOrphanFiles(nil) })
	}
}

func (s *ImageStore) GetKnownIDs() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	known := make(map[string]bool, len(s.idSet))
	for k, v := range s.idSet {
		known[k] = v
	}
	return known
}

// RemoveByQueryID removes all images associated with a specific provider query ID from rotation.
// Their master files are kept as retained entries (still linked to the query, so re-enabling it
// can re-admit them without network access) and are only deleted by disk-budget eviction.
// Favorites copies are the exception: clearing favorites deletes their cached files as before.
func (s *ImageStore) RemoveByQueryID(queryID string) {
	if queryID == FavoritesQueryID {
		s.mu.Lock()
		toDelete := s.removeQueryImagesLocked(queryID)
		s.scheduleSaveLocked()
		s.mu.Unlock()
		ids := make([]string, len(toDelete))
		for i, img := range toDelete {
			ids[i] = img.ID
		}
		s.asyncDeleteUnownedIDs(ids)
		return
	}
	s.RetainByQueryID(queryID, false)
}

// removeQueryImagesLocked drops a query's images from the active set and returns them.
// CALLER MUST HOLD s.mu.Lock().
func (s *ImageStore) removeQueryImagesLocked(queryID string) []provider.Image {
	var remaining []provider.Image
	var removed []provider.Image
	newSeenCount := 0
	for _, img := range s.images {
		if img.SourceQueryID == queryID {
			removed = append(removed, img)
			delete(s.idSet, img.ID)
			log.Debugf("[RemoveByQueryID] Matched %s (Source: %s). Removing from rotation.", img.ID, img.SourceQueryID)
			if img.FilePath != "" {
				delete(s.pathSet, img.FilePath)
			}
		} else {
			remaining = append(remaining, img)
			if img.Seen {
				newSeenCount++
			}
		}
	}
	s.images = remaining
	s.seenCount = newSeenCount

	// Re-index pathSet
	s.pathSet = make(map[string]int)
	for i, img := range s.images {
		if img.FilePath != "" {
			s.pathSet[img.FilePath] = i
		}
	}
	s.rebuildBucketsLocked() // Ensure buckets are updated after batch removal
	return removed
}

func (s *ImageStore) List() []provider.Image {
	s.mu.RLock()
	defer s.mu.RUnlock()
	res := make([]provider.Image, len(s.images))
	copy(res, s.images)
	return res
}

// LoadAvoidSet replaces the blocklist. Retained entries that are blocked are dropped and
// their files deleted (blocked images are never kept for reuse).
func (s *ImageStore) LoadAvoidSet(avoidSet map[string]bool) {
	s.mu.Lock()
	s.avoidSet = make(map[string]bool)
	var purge []string
	for k, v := range avoidSet {
		s.avoidSet[k] = v
		if _, ok := s.retained[k]; v && ok {
			delete(s.retained, k)
			purge = append(purge, k)
		}
	}
	if len(purge) > 0 {
		s.scheduleSaveLocked()
	}
	s.mu.Unlock()
	s.asyncDeleteUnownedIDs(purge)
}

// LoadCache loads the active store and the retained index (see cache_persist.go).
// The retained index is loaded even when the legacy active cache file is missing.
func (s *ImageStore) LoadCache() error {
	s.mu.RLock()
	cachePath := s.cachePath
	s.mu.RUnlock()

	// File I/O happens before taking the write lock: lowercase cached file extensions
	// (so every OS finds the same files), then read the persisted state.
	var renamed map[string]string
	if s.fm != nil {
		renamed = s.fm.NormalizeExtensions()
	}
	st, err := loadPersisted(cachePath)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	s.lastMirrorDigest = st.mirrorDigest
	s.writeMu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.images = st.images
	s.retained = st.retained
	extMigrated := applyRenamedPaths(s.images, s.retained, renamed)

	// Namespacing Migration: Upgrade legacy IDs to namespaced format
	migrationOccurred := s.migrateLegacyIDsLocked() || extMigrated

	s.idSet = make(map[string]bool)
	s.pathSet = make(map[string]int)
	s.resolutionBuckets = make(map[string][]string)
	s.seenCount = 0
	for i, img := range s.images {
		s.idSet[img.ID] = true
		if img.FilePath != "" {
			s.pathSet[img.FilePath] = i
		}
		if img.Seen {
			s.seenCount++
		}
		// Populate buckets from cache
		for res := range img.DerivativePaths {
			s.resolutionBuckets[res] = append(s.resolutionBuckets[res], img.ID)
		}
	}
	// Invariant: an ID is active or retained, never both (active wins).
	for id := range s.retained {
		if s.idSet[id] {
			delete(s.retained, id)
		}
	}

	if migrationOccurred {
		log.Printf("Migration: Namespacing upgrade complete. Saving updated cache.")
		// No lock needed as we're inside LoadCache which holds mu.Lock()
		s.scheduleSaveLocked()
	}
	return nil
}

// SaveCache writes the store and retained index now. It returns the write error, if any;
// on failure the previously persisted index is left in place.
func (s *ImageStore) SaveCache() error {
	s.mu.RLock()
	snap := s.snapshotLocked()
	s.mu.RUnlock()
	return s.writeSnapshot(snap)
}

// GetIDsForResolution returns a list of IDs compatible with the given resolution.
func (s *ImageStore) GetIDsForResolution(resolution string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids, exists := s.resolutionBuckets[resolution]
	if !exists {
		return nil
	}

	res := make([]string, len(ids))
	copy(res, ids)
	return res
}

// GetBucketSize returns the number of images available for a specific resolution.
func (s *ImageStore) GetBucketSize(resolution string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.resolutionBuckets[resolution])
}

// ImageSyncAction defines the cleanup action required for an image.
type ImageSyncAction int

const (
	ImageActionKeep       ImageSyncAction = iota
	ImageActionDelete                     // Prune: Delete everything (Master + Derivatives)
	ImageActionInvalidate                 // Refresh: Delete only derivatives (Keep Master)
	ImageActionRetain                     // Leave rotation: keep Master as a retained entry
)

func (s *ImageStore) Sync(limit int, targetFlags map[string]bool, activeQueryIDs map[string]bool) {
	if s.fm == nil {
		return
	}

	if limit <= 0 {
		limit = 50
	}

	s.mu.RLock()
	candidates := make([]provider.Image, len(s.images))
	copy(candidates, s.images)
	s.mu.RUnlock()

	badIDs := make(map[string]ImageSyncAction)

	// 1. Determine actions for all candidates
	for _, img := range candidates {
		action := s.determineSyncAction(img, activeQueryIDs, targetFlags)
		if action != ImageActionKeep {
			badIDs[img.ID] = action
		}
	}

	s.mu.Lock()
	var finalImages []provider.Image
	var idsToDelete []string
	var idsToInvalidate []string
	var toRetain []provider.Image

	// 2. Apply Actions (Delete/Invalidate)
	for _, img := range s.images {
		action, exists := badIDs[img.ID]
		if !exists || action == ImageActionKeep {
			finalImages = append(finalImages, img)
			continue
		}

		if action == ImageActionInvalidate {
			// Live re-check: a job may own the image (its result carries the current
			// settings) or may already have replaced it with matching derivatives.
			if s.leases[img.ID] || s.deleting[img.ID] || s.derivDeleting[img.ID] || (len(targetFlags) > 0 && s.flagsMatch(img, targetFlags)) {
				finalImages = append(finalImages, img)
				continue
			}
			// Invalidate: Keep Master, Reset Flags/Paths. The derivative files are deleted
			// under a delete claim taken right here, so no job can reuse or publish them
			// until they are gone (cache_lease.go).
			s.removeFromBucketsLocked(img.ID, img.DerivativePaths)
			s.derivDeleting[img.ID] = true
			img.DerivativePaths = make(map[string]string)
			img.ProcessingFlags = make(map[string]bool)
			for k, v := range targetFlags {
				img.ProcessingFlags[k] = v
			}
			finalImages = append(finalImages, img)
			idsToInvalidate = append(idsToInvalidate, img.ID)
		} else if action == ImageActionRetain {
			toRetain = append(toRetain, img)
			delete(s.idSet, img.ID)
		} else {
			// ActionDelete
			idsToDelete = append(idsToDelete, img.ID)
			delete(s.idSet, img.ID)
		}
	}

	// 3. Prune Limit
	if len(finalImages) > limit {
		excess := len(finalImages) - limit
		toPrune := finalImages[:excess]
		finalImages = finalImages[excess:]
		// Over the rotation limit: keep the masters as retained entries; the disk
		// budget decides when their bytes are actually reclaimed.
		toRetain = append(toRetain, toPrune...)
		for _, img := range toPrune {
			delete(s.idSet, img.ID)
		}
	}

	// 4. Update State
	s.images = finalImages
	s.rebuildInternalStateLocked()
	retainedIDs := s.retainLocked(toRetain, false)

	// 4b. Retained entries processed under other settings: their rejection tags and
	// derivatives are stale. Reset their flags (dropping "incompatible:" tags) and delete
	// their derivatives under a claim, like active invalidation.
	if len(targetFlags) > 0 {
		for id, e := range s.retained {
			if s.leases[id] || s.deleting[id] || s.derivDeleting[id] || s.flagsMatch(e.Image, targetFlags) {
				continue
			}
			flags := make(map[string]bool, len(targetFlags))
			for k, v := range targetFlags {
				flags[k] = v
			}
			e.Image.ProcessingFlags = flags
			if !e.DerivativesCleared {
				s.derivDeleting[id] = true
				idsToInvalidate = append(idsToInvalidate, id)
			}
			s.retained[id] = e
		}
	}
	s.scheduleSaveLocked()
	s.mu.Unlock()

	s.refreshRetainedSizes(retainedIDs)

	// 5. Async Cleanup
	log.Debugf("[Sync] Completed. Final Count: %d. Deleting: %d. Invalidating: %d. Retaining: %d.", len(finalImages), len(idsToDelete), len(idsToInvalidate), len(retainedIDs))
	if len(idsToDelete) > 0 {
		log.Debugf("[Sync] Deleting IDs: %v", idsToDelete)
	}
	s.performAsyncCleanup(idsToDelete, idsToInvalidate)
}

// determineSyncAction decides what to do with an image during sync.
func (s *ImageStore) determineSyncAction(img provider.Image, activeQueryIDs map[string]bool, targetFlags map[string]bool) ImageSyncAction {
	isProtected := img.IsFavorited || img.Provider == "Favorites" || strings.Contains(img.ID, "_favorite_images_")

	// Strict Mode Check
	if activeQueryIDs != nil && !isProtected {
		isActive := img.SourceQueryID != "" && activeQueryIDs[img.SourceQueryID]
		isOrphan := img.SourceQueryID == ""
		if isOrphan || !isActive {
			if s.avoidSet[img.ID] {
				return ImageActionDelete
			}
			log.Debugf("[Sync] Removing %s from rotation (retaining master). Orphan: %v, ActiveSource: %v (SourceID: '%s')", img.ID, isOrphan, isActive, img.SourceQueryID)
			return ImageActionRetain
		}
	}

	// AvoidSet Check
	if s.avoidSet[img.ID] && !isProtected {
		return ImageActionDelete
	}

	// Master File Check
	if !s.masterFileExists(img.ID) {
		// If it's a known incompatible image, keep it in the DB (as a zombie)
		// so we don't forget its processing flags and redownload it over and over!
		if s.hasIncompatibleFlags(img) {
			return ImageActionKeep
		}
		return ImageActionDelete
	}

	// Flag Mismatch Check
	if len(targetFlags) > 0 {
		if !s.flagsMatch(img, targetFlags) {
			return ImageActionInvalidate
		}
	}

	return ImageActionKeep
}

func (s *ImageStore) hasIncompatibleFlags(img provider.Image) bool {
	for k, v := range img.ProcessingFlags {
		if v && strings.HasPrefix(k, "incompatible:") {
			return true
		}
	}
	return false
}

func (s *ImageStore) masterFileExists(id string) bool {
	statFunc := s.getStatFunc()

	// Check JPG
	masterPath, err := s.fm.GetMasterPath(id, ".jpg")
	if err == nil {
		if _, err := statFunc(masterPath); err == nil {
			return true
		}
	}

	// Check JPEG (Critical for Pexels which uses .jpeg)
	masterPath, err = s.fm.GetMasterPath(id, ".jpeg")
	if err == nil {
		if _, err := statFunc(masterPath); err == nil {
			return true
		}
	}

	// Check PNG
	masterPath, err = s.fm.GetMasterPath(id, ".png")
	if err == nil {
		if _, err := statFunc(masterPath); err == nil {
			return true
		}
	}

	return false
}

func (s *ImageStore) flagsMatch(img provider.Image, target map[string]bool) bool {
	// Compare only the processing-mode keys from `target`.
	// img.ProcessingFlags may also contain "incompatible:<WxH>" metadata tags
	// that are NOT part of the processing mode, so a strict length comparison
	// would always fail for images that have any incompatibility tags.
	for k, v := range target {
		if img.ProcessingFlags[k] != v {
			return false
		}
	}
	// Reverse check: ensure the image doesn't have processing-mode keys
	// that aren't in target (guards against future flag additions).
	for k, v := range img.ProcessingFlags {
		if strings.HasPrefix(k, "incompatible:") {
			continue // skip metadata tags
		}
		if target[k] != v {
			return false
		}
	}
	return true
}

func (s *ImageStore) rebuildInternalStateLocked() {
	s.idSet = make(map[string]bool)
	s.pathSet = make(map[string]int)
	s.resolutionBuckets = make(map[string][]string)
	s.seenCount = 0
	for i, img := range s.images {
		s.idSet[img.ID] = true
		if img.FilePath != "" {
			s.pathSet[img.FilePath] = i
		}
		if img.Seen {
			s.seenCount++
		}
		for res := range img.DerivativePaths {
			s.resolutionBuckets[res] = append(s.resolutionBuckets[res], img.ID)
		}
	}
}

func (s *ImageStore) performAsyncCleanup(idsToDelete, idsToInvalidate []string) {
	if s.fm == nil {
		return
	}
	s.asyncDeleteUnownedIDs(idsToDelete)
	if len(idsToInvalidate) > 0 {
		ids := append([]string(nil), idsToInvalidate...)
		s.fm.runAsync(func() { s.finishInvalidation(ids) })
	}
}

// WaitTimeout waits for in-flight store file operations to drain up to timeout.
func (s *ImageStore) WaitTimeout(timeout time.Duration) bool {
	if s.fm != nil {
		return s.fm.WaitTimeout(timeout)
	}
	return true
}

// Close closes the underlying FileManager if present.
func (s *ImageStore) Close() {
	if s.fm != nil {
		s.fm.Close()
	}
}

// migrateLegacyIDsLocked upgrades legacy image IDs to the namespaced format (Provider_ID).
// CALLER MUST HOLD s.mu.Lock()
func (s *ImageStore) migrateLegacyIDsLocked() bool {
	migrationOccurred := false
	for i, img := range s.images {
		// Rule: Only online providers had raw numeric/string IDs.
		// Skip Favorites (already namespaced via filenames) and already-namespaced IDs.
		if img.Provider != "" && img.Provider != "Favorites" {
			prefix := img.Provider + "_"
			if !strings.HasPrefix(img.ID, prefix) {
				oldID := img.ID
				newID := prefix + oldID
				log.Printf("Migration: Upgrading legacy image ID %s -> %s for provider %s", oldID, newID, img.Provider)

				// 1. Rename files on disk (Master and Derivatives)
				if s.fm != nil {
					if err := s.fm.RenameAllAssets(oldID, newID); err != nil {
						log.Printf("Migration: Failed to rename assets for %s: %v", oldID, err)
					}
				}

				// 2. Update Image Metadata
				s.images[i].ID = newID

				// 3. Update Derivative Paths
				if img.DerivativePaths != nil {
					newPaths := make(map[string]string)
					for res, oldPath := range img.DerivativePaths {
						// Derivative paths include the ID in the filename
						newPath := strings.Replace(oldPath, oldID, newID, 1)
						newPaths[res] = newPath
					}
					s.images[i].DerivativePaths = newPaths
				}

				// 4. Update FilePath
				if img.FilePath != "" {
					s.images[i].FilePath = strings.Replace(img.FilePath, oldID, newID, 1)
				}

				migrationOccurred = true
			}
		}
	}

	// Retained entries carry the same legacy IDs (and files) and are migrated alike.
	for oldID, e := range s.retained {
		p := e.Image.Provider
		if p == "" {
			p = e.Provider
		}
		if p == "" || p == "Favorites" || strings.HasPrefix(oldID, p+"_") {
			continue
		}
		newID := p + "_" + oldID
		log.Printf("Migration: Upgrading legacy retained image ID %s -> %s for provider %s", oldID, newID, p)
		if s.fm != nil {
			if err := s.fm.RenameAllAssets(oldID, newID); err != nil {
				log.Printf("Migration: Failed to rename assets for %s: %v", oldID, err)
			}
		}
		delete(s.retained, oldID)
		e.Image.ID = newID
		if _, exists := s.retained[newID]; !exists {
			s.retained[newID] = e
		}
		migrationOccurred = true
	}
	return migrationOccurred
}
