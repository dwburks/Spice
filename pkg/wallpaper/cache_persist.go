package wallpaper

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dixieflatline76/Spice/v2/pkg/provider"
	"github.com/dixieflatline76/Spice/v2/util/log"
)

// Persistence of the active store and the retained index.
//
// Both collections are written together into ONE file (image_cache_index.json) that is
// replaced atomically (temp file in the same directory, then rename). Moving an image
// between rotation and the retained index therefore can never be persisted half-way.
//
// The legacy image_cache_map.json (active images only) is still written, AFTER the index,
// as a mirror for older Spice versions. Each index records the digest of the mirror it is
// about to write (MirrorDigest) and of the mirror that was on disk before (PrevMirrorDigest).
// On load, the mirror on disk is compared by content, never by timestamp:
//
//   - matches MirrorDigest: normal; the index is authoritative.
//   - matches PrevMirrorDigest: a crash or failure hit between the two writes; the
//     index is newer and authoritative (nothing is resurrected from the stale mirror).
//   - matches neither: an older Spice version rewrote the mirror; its active list is
//     used and everything else the index owned is kept as retained (nothing is orphaned).
//
// A legacy-only cache (first start after upgrading) is never rewritten before the first
// index has been committed, so a failed first migration leaves the legacy ownership intact.

// cacheIndexFileName is the authoritative cache file, next to the legacy cache file.
const cacheIndexFileName = "image_cache_index.json"

// saveRetryDelay is how long an async save waits before retrying after a write failure.
const saveRetryDelay = 30 * time.Second

type cacheIndexFile struct {
	Version          int                      `json:"version"`
	Images           []provider.Image         `json:"images"`
	Retained         map[string]RetainedEntry `json:"retained"`
	MirrorDigest     string                   `json:"mirror_digest,omitempty"`
	PrevMirrorDigest string                   `json:"prev_mirror_digest,omitempty"`
}

type cacheSnapshot struct {
	gen       uint64
	images    []provider.Image
	retained  map[string]RetainedEntry
	cachePath string
	indexPath string
}

// indexPathFor returns the index path that belongs to a legacy cache path.
func indexPathFor(cachePath string) string {
	if cachePath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(cachePath), cacheIndexFileName)
}

func contentDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// snapshotLocked deep-copies everything that is persisted. CALLER MUST HOLD s.mu (read or write).
func (s *ImageStore) snapshotLocked() cacheSnapshot {
	images := make([]provider.Image, len(s.images))
	for i, img := range s.images {
		images[i] = copyImageMaps(img)
	}
	return cacheSnapshot{
		gen:       s.saveGen.Add(1),
		images:    images,
		retained:  s.retainedSnapshotLocked(),
		cachePath: s.cachePath,
		indexPath: indexPathFor(s.cachePath),
	}
}

// writeFileAtomic writes data to path via a temp file in the same directory and a rename.
// The temp file is closed before the rename (required on Windows).
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(tmp), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", filepath.Base(tmp), err)
	}
	return nil
}

// writeSnapshot persists snap: the index first (the commit point), then the legacy mirror.
// Writes are serialized by writeMu (a leaf lock that may be taken while holding s.mu in
// sync-save mode, never the other way round); a snapshot older than one already written is
// skipped so saves never go backwards. Errors are returned; on an index failure nothing on
// disk changes, so the last durable ownership record survives.
func (s *ImageStore) writeSnapshot(snap cacheSnapshot) error {
	if s.saveFunc != nil {
		s.saveFunc()
	}
	if snap.cachePath == "" {
		return nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if snap.gen <= s.lastWrittenGen {
		return nil
	}

	mirror, err := json.MarshalIndent(snap.images, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cache mirror: %w", err)
	}
	mirrorDigest := contentDigest(mirror)
	index, err := json.MarshalIndent(cacheIndexFile{
		Version:          2,
		Images:           snap.images,
		Retained:         snap.retained,
		MirrorDigest:     mirrorDigest,
		PrevMirrorDigest: s.lastMirrorDigest,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cache index: %w", err)
	}

	if err := writeFileAtomic(snap.indexPath, index); err != nil {
		log.Printf("Store: Failed to save cache index (previous state kept): %v", err)
		return err
	}
	s.lastWrittenGen = snap.gen

	if err := writeFileAtomic(snap.cachePath, mirror); err != nil {
		// The index is committed; its PrevMirrorDigest still matches the mirror on disk.
		log.Printf("Store: Failed to save legacy cache mirror: %v", err)
		return err
	}
	s.lastMirrorDigest = mirrorDigest
	return nil
}

// persistedState is what loadPersisted read from disk.
type persistedState struct {
	images       []provider.Image
	retained     map[string]RetainedEntry
	mirrorDigest string // digest of the legacy file as found on disk ("" if absent)
}

// loadPersisted reads the index and/or legacy cache for cachePath. It does file I/O and
// touches no store state, so callers run it without holding s.mu.
func loadPersisted(cachePath string) (persistedState, error) {
	st := persistedState{images: make([]provider.Image, 0), retained: make(map[string]RetainedEntry)}
	if cachePath == "" {
		return st, nil
	}

	var index *cacheIndexFile
	var indexTime time.Time
	indexPath := indexPathFor(cachePath)
	if info, err := os.Stat(indexPath); err == nil {
		data, rerr := os.ReadFile(indexPath)
		var idx cacheIndexFile
		if rerr == nil {
			rerr = json.Unmarshal(data, &idx)
		}
		if rerr != nil {
			log.Printf("Store: Failed to read cache index, falling back to legacy cache: %v", rerr)
		} else {
			index, indexTime = &idx, info.ModTime()
		}
	}

	var legacy []provider.Image
	haveLegacy := false
	if data, err := os.ReadFile(cachePath); err == nil {
		st.mirrorDigest = contentDigest(data)
		if rerr := json.Unmarshal(data, &legacy); rerr != nil {
			if index == nil {
				return persistedState{}, rerr
			}
			log.Printf("Store: Ignoring unreadable legacy cache: %v", rerr)
		} else {
			haveLegacy = true
		}
	} else if !os.IsNotExist(err) && index == nil {
		return persistedState{}, err
	}

	switch {
	case index != nil && (!haveLegacy || st.mirrorDigest == index.MirrorDigest || st.mirrorDigest == index.PrevMirrorDigest):
		st.images = index.Images
		if index.Retained != nil {
			st.retained = index.Retained
		}
	case index != nil:
		// An older Spice version rewrote the legacy file: trust its active list and keep
		// everything else the index owned as retained (nothing loses its owner).
		log.Printf("Store: Legacy cache was changed by another version; merging with the cache index.")
		st.images = legacy
		inLegacy := make(map[string]bool, len(legacy))
		for _, img := range legacy {
			inLegacy[img.ID] = true
		}
		if index.Retained != nil {
			st.retained = index.Retained
		}
		for _, img := range index.Images {
			if !inLegacy[img.ID] {
				if _, ok := st.retained[img.ID]; !ok {
					st.retained[img.ID] = newRetainedEntry(img, indexTime, false)
				}
			}
		}
	case haveLegacy:
		st.images = legacy
	}
	if st.images == nil {
		st.images = make([]provider.Image, 0)
	}
	return st, nil
}
