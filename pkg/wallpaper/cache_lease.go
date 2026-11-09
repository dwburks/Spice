package wallpaper

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/dixieflatline76/Spice/v2/util/log"
)

// Per-ID ownership protocol for cache files.
//
// Two kinds of owner exist for an image ID, both tracked under store.mu:
//
//   - Job lease (s.leases): held by a pipeline job from just before ProcessImageJob until
//     its result has been admitted to the store (or dropped) by stateManagerLoop. It covers
//     reuse, download, derivative generation and admission as one unit. Only one job may
//     hold an ID; a second job for the same ID is rejected as a duplicate.
//   - Delete claim (s.deleting): held by any code that deletes an ID's files (eviction,
//     retained-derivative cleanup, orphan cleanup, user/Sync deletes) for exactly the time
//     it touches the disk plus the index commit. A claim is only granted after a live check,
//     under store.mu, that no job holds the ID and the ID is not in rotation.
//
// While a delete claim is held, AcquireJobLease waits (the explicit retry path) and
// Add/replace refuse the ID, so no active record can ever point at a file being deleted.
// File I/O never happens while store.mu is held.

// errJobInFlight rejects a duplicate job for an ID another job is processing.
var errJobInFlight = errors.New("image is already being processed")

// leaseRetryInterval is how often a job re-checks an ID that a deleter currently owns.
const leaseRetryInterval = 10 * time.Millisecond

// deleteKind selects the live precondition for a delete claim.
type deleteKind int

const (
	// deleteUnowned: files of an ID that neither rotation nor the retained index owns
	// (orphans, user-deleted/blocked images, Sync deletions).
	deleteUnowned deleteKind = iota
	// deleteEvict: a retained entry's master and derivatives (disk budget).
	deleteEvict
	// deleteRetainedDerivatives: a retained entry's derivatives only.
	deleteRetainedDerivatives
)

// AcquireJobLease reserves id for one pipeline job. It waits while a deleter owns the ID
// and returns errJobInFlight if another job already holds it.
func (s *ImageStore) AcquireJobLease(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	for {
		s.mu.Lock()
		if !s.deleting[id] && !s.derivDeleting[id] {
			if s.leases[id] {
				s.mu.Unlock()
				return errJobInFlight
			}
			s.leases[id] = true
			s.mu.Unlock()
			return nil
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(leaseRetryInterval):
		}
	}
}

// ReleaseJobLease ends a job's ownership of id (after admission or when the result is
// dropped). A delete that was refused while the job held the ID is completed now.
func (s *ImageStore) ReleaseJobLease(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	delete(s.leases, id)
	finish := s.takePendingDeleteLocked(id)
	s.mu.Unlock()
	if finish {
		s.asyncDeleteUnownedIDs([]string{id})
	}
}

// heldLocked reports whether a job or a display holds id. CALLER MUST HOLD s.mu.
func (s *ImageStore) heldLocked(id string) bool {
	return s.leases[id] || s.displayRefs[id] > 0
}

// takePendingDeleteLocked reports (and clears) a deferred delete that may now run: the ID
// is no longer held and still belongs to no one. CALLER MUST HOLD s.mu.Lock().
func (s *ImageStore) takePendingDeleteLocked(id string) bool {
	if !s.pendingDelete[id] || s.heldLocked(id) {
		return false
	}
	delete(s.pendingDelete, id)
	_, retained := s.retained[id]
	return !s.idSet[id] && !retained
}

// ReserveDisplay records that monitorID is about to show id (before the OS call), so no
// deleter can remove its files during the switch. A previous unpublished reservation on
// the same monitor is dropped.
func (s *ImageStore) ReserveDisplay(monitorID int, id string) {
	if id == "" {
		return
	}
	var finish []string
	s.mu.Lock()
	if old, ok := s.pendingByMonitor[monitorID]; ok && old != "" {
		if s.releaseDisplayRefLocked(old) {
			finish = append(finish, old)
		}
	}
	s.pendingByMonitor[monitorID] = id
	s.displayRefs[id]++
	s.mu.Unlock()
	s.asyncDeleteUnownedIDs(finish)
}

// CommitDisplay records that monitorID now shows id and releases what it showed before.
func (s *ImageStore) CommitDisplay(monitorID int, id string) {
	if id == "" {
		return
	}
	var finish []string
	s.mu.Lock()
	if s.pendingByMonitor[monitorID] == id {
		delete(s.pendingByMonitor, monitorID) // the reservation becomes the shown reference
	} else {
		s.displayRefs[id]++
	}
	if old, ok := s.shownByMonitor[monitorID]; ok && old != "" {
		if s.releaseDisplayRefLocked(old) {
			finish = append(finish, old)
		}
	}
	s.shownByMonitor[monitorID] = id
	s.mu.Unlock()
	s.asyncDeleteUnownedIDs(finish)
}

// CancelDisplay drops a reservation that was never published (e.g. its file was missing).
func (s *ImageStore) CancelDisplay(monitorID int, id string) {
	var finish []string
	s.mu.Lock()
	if s.pendingByMonitor[monitorID] == id {
		delete(s.pendingByMonitor, monitorID)
		if s.releaseDisplayRefLocked(id) {
			finish = append(finish, id)
		}
	}
	s.mu.Unlock()
	s.asyncDeleteUnownedIDs(finish)
}

// IsDisplayed reports whether any monitor shows or is switching to id.
func (s *ImageStore) IsDisplayed(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.displayRefs[id] > 0
}

// releaseDisplayRefLocked drops one display reference. Returns true if a deferred delete
// of id should now run. CALLER MUST HOLD s.mu.Lock().
func (s *ImageStore) releaseDisplayRefLocked(id string) bool {
	if s.displayRefs[id] <= 1 {
		delete(s.displayRefs, id)
	} else {
		s.displayRefs[id]--
	}
	return s.takePendingDeleteLocked(id)
}

// IsLeased reports whether a job currently holds id.
func (s *ImageStore) IsLeased(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.leases[id]
}

// canDeleteLocked is the live check for a delete claim. CALLER MUST HOLD s.mu.
// Nothing a job, a monitor, or another deleter holds is ever deletable.
func (s *ImageStore) canDeleteLocked(id string, kind deleteKind) bool {
	if s.heldLocked(id) || s.deleting[id] || s.derivDeleting[id] || s.idSet[id] {
		return false
	}
	e, retained := s.retained[id]
	switch kind {
	case deleteUnowned:
		return !retained
	case deleteEvict:
		return retained
	case deleteRetainedDerivatives:
		return retained && !e.DerivativesCleared
	}
	return false
}

// withDeleteClaim runs del (file I/O, outside s.mu) only if the live check for kind passes,
// then runs commit (index update, under s.mu) before the claim is released, so nothing can
// observe the files gone while the index still claims them. Returns false if not claimed.
func (s *ImageStore) withDeleteClaim(id string, kind deleteKind, del func() error, commit func(err error)) bool {
	s.mu.Lock()
	if !s.canDeleteLocked(id, kind) {
		if kind == deleteUnowned && s.heldLocked(id) && !s.idSet[id] {
			if _, retained := s.retained[id]; !retained {
				// Finish it when the job or display lets go (takePendingDeleteLocked).
				s.pendingDelete[id] = true
			}
		}
		s.mu.Unlock()
		return false
	}
	owner := s.deleting
	if kind == deleteRetainedDerivatives {
		owner = s.derivDeleting // the master stays: the entry remains visible and reusable
	}
	owner[id] = true
	s.mu.Unlock()

	err := del()

	s.mu.Lock()
	if commit != nil {
		commit(err)
	}
	delete(owner, id)
	s.mu.Unlock()
	return true
}

// finishInvalidation deletes the derivatives of IDs Sync invalidated (claims already taken
// in Sync via s.derivDeleting), then releases the claims. Retained entries are marked as
// having no derivatives.
func (s *ImageStore) finishInvalidation(ids []string) {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	files := s.fm.ListFilesByID(func(id string) bool { return want[id] })
	failed := make(map[string]bool)
	for _, id := range ids {
		if f := files[id]; f != nil {
			if err := s.fm.removePaths(f.Derivatives, 0); err != nil {
				log.Debugf("Store: Could not delete stale derivatives of %s: %v", id, err)
				failed[id] = true
			}
		}
	}
	s.mu.Lock()
	for _, id := range ids {
		delete(s.derivDeleting, id)
		if e, ok := s.retained[id]; ok && !failed[id] {
			// In memory only: the flag just saves a later cleanup pass a walk, so it is
			// persisted with the next save rather than forcing one here.
			e.DerivativesCleared = true
			s.retained[id] = e
		}
	}
	s.mu.Unlock()
}

// deleteUnownedIDs deletes every file of the given IDs that no one owns, one claim per ID.
// Blocking; IDs that are leased, active or retained at delete time are skipped.
func (s *ImageStore) deleteUnownedIDs(ids []string) {
	if s.fm == nil || len(ids) == 0 {
		return
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	files := s.fm.ListFilesByID(func(id string) bool { return want[id] })
	for _, id := range ids {
		f := files[id]
		if f == nil {
			// Nothing on disk yet, but a job may still be writing it: finish later.
			s.MarkPendingDeleteIfUnowned(id)
			continue
		}
		s.withDeleteClaim(id, deleteUnowned, func() error {
			return s.fm.removePaths(append(append([]string{}, f.Masters...), f.Derivatives...), 0)
		}, func(err error) {
			if err != nil {
				log.Printf("Store: Failed to delete files of %s (orphan cleanup will retry): %v", id, err)
			}
		})
	}
}

// asyncDeleteUnownedIDs runs deleteUnownedIDs in a tracked background goroutine.
func (s *ImageStore) asyncDeleteUnownedIDs(ids []string) {
	if s.fm == nil || len(ids) == 0 {
		return
	}
	ids = append([]string(nil), ids...)
	s.fm.runAsync(func() { s.deleteUnownedIDs(ids) })
}

// CleanupOrphanFiles deletes files whose ID is not active, not retained, not leased and not
// in extraKnown (e.g. images on screen). Every deletion is a live-checked claim, so images
// downloaded or re-admitted while the scan runs are never touched. IDs that differ from a
// known ID only by case are kept on every OS. Returns the number of files removed.
func (s *ImageStore) CleanupOrphanFiles(extraKnown map[string]bool) int {
	if s.fm == nil {
		return 0
	}
	log.Print("FileManager: Starting orphan cleanup...")
	removed := s.fm.sweepStaleTempDownloads()

	files := s.fm.ListFilesByID(nil)
	known := s.KnownFileIDs()
	for id, ok := range extraKnown {
		if ok {
			known[id] = true
		}
	}
	knownFold := make(map[string]bool, len(known))
	for id := range known {
		knownFold[strings.ToLower(id)] = true
	}

	for id, f := range files {
		if s.fm.closing.Load() {
			log.Debug("CleanupOrphans: Aborted orphan cleanup due to shutdown.")
			break
		}
		if known[id] || knownFold[strings.ToLower(id)] {
			continue
		}
		paths := append(append([]string{}, f.Masters...), f.Derivatives...)
		s.withDeleteClaim(id, deleteUnowned, func() error {
			err := s.fm.removePaths(paths, orphanDeletePacing)
			if err == nil {
				removed += len(paths)
			}
			return err
		}, nil)
	}
	log.Printf("FileManager: Orphan cleanup finished. Removed %d files.", removed)
	return removed
}

// MarkPendingDeleteIfUnowned defers deleting id's files until the job or display holding
// it lets go, provided nothing owns it by then (e.g. a download finished for an image the
// user blocked meanwhile).
func (s *ImageStore) MarkPendingDeleteIfUnowned(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, retained := s.retained[id]; retained || s.idSet[id] {
		return
	}
	if s.heldLocked(id) {
		s.pendingDelete[id] = true
	}
}

// defaultBudgetCheckDelay coalesces bursts of admissions into one disk-budget pass.
const defaultBudgetCheckDelay = 100 * time.Millisecond

// ScheduleBudgetCheck runs a debounced EnforceDiskBudget pass. It is called after a
// processed image (and its downloaded bytes) has been admitted to the store.
func (s *ImageStore) ScheduleBudgetCheck(budget int64) {
	if s.fm == nil {
		return
	}
	s.budgetMu.Lock()
	defer s.budgetMu.Unlock()
	if s.budgetTimer != nil {
		return // A pass is already scheduled; it will see these bytes too.
	}
	s.budgetTimer = time.AfterFunc(s.budgetDelay, func() {
		s.budgetMu.Lock()
		s.budgetTimer = nil
		s.budgetMu.Unlock()
		s.fm.runAsync(func() { s.EnforceDiskBudget(budget, nil) })
	})
}
