package wallpaper

import (
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dixieflatline76/Spice/v2/util/log"
)

// FileManager handles all file system operations for wallpaper images.
// It enforces the directory structure for Source + Derivative architecture.
type FileManager struct {
	rootDir string
	wg      sync.WaitGroup
	closing atomic.Bool

	// removeFn overrides os.Remove in removePaths (tests simulate a locked file
	// without depending on any one OS's open-file delete semantics).
	removeFn func(string) error
	// scanHook, if set, runs at the end of ScanMasters (tests model files appearing
	// between a scan and the lock that consumes it).
	scanHook func()
}

// NewFileManager creates a new FileManager with the given root directory.
// The rootDir is typically ".../wallpaper_downloads".
func NewFileManager(rootDir string) *FileManager {
	return &FileManager{
		rootDir: rootDir,
	}
}

// GetDownloadDir returns the root directory where images are downloaded.
func (fm *FileManager) GetDownloadDir() string {
	return fm.rootDir
}

// EnsureDirs creates necessary subdirectories for derivatives.
func (fm *FileManager) EnsureDirs() error {
	// Root dirs
	if err := os.MkdirAll(fm.rootDir, 0755); err != nil {
		return fmt.Errorf("failed to create root directory %s: %w", fm.rootDir, err)
	}

	fittedRoot := filepath.Join(fm.rootDir, FittedRootDir)

	// Create structure: fitted/{quality,flexibility}/{standard,faceboost,facecrop}
	modes := []string{QualityDir, FlexibilityDir}
	types := []string{StandardDir, FaceBoostDir, FaceCropDir}

	for _, mode := range modes {
		for _, t := range types {
			dir := filepath.Join(fittedRoot, mode, t)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", dir, err)
			}
		}
	}

	// On macOS, sweep orphaned .spice_tmp hardlinks left behind by previous runs
	if runtime.GOOS == "darwin" {
		if tmps, err := filepath.Glob(filepath.Join(fittedRoot, "*", "*", "*.spice_tmp")); err == nil {
			for _, f := range tmps {
				_ = os.Remove(f)
			}
		}
	}

	return nil
}

// validateID ensures the ID does not contain path traversal characters.
// Both separators are rejected on every OS: Windows treats '/' as a separator too, and
// an ID that is only valid on one platform would make the cache non-portable.
func (fm *FileManager) validateID(id string) error {
	if strings.Contains(id, "..") || strings.ContainsAny(id, `/\`) || strings.ContainsRune(id, 0) {
		return fmt.Errorf("invalid id: contains illegal characters")
	}
	return nil
}

// tempDownloadSuffix marks an in-progress master download. Downloads are written to
// "<id><ext>"+tempDownloadSuffix in the same directory and renamed into place only on success.
const tempDownloadSuffix = ".spice_dl"

// staleTempDownloadAge is how old an abandoned temp download must be before orphan cleanup removes it.
// Younger files may belong to a download that is still in flight.
const staleTempDownloadAge = time.Hour

// isImageExt reports whether ext (with dot, any case) is a cached image extension.
func isImageExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg", ".png":
		return true
	}
	return false
}

// isMasterCandidate reports whether a root-directory file name can be a master. Masters
// take their extension from the download URL (usually .jpg/.jpeg/.png, but not always),
// so everything except Spice's own bookkeeping files qualifies.
func isMasterCandidate(name string) bool {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".json"), strings.HasSuffix(lower, ".tmp"),
		strings.HasSuffix(lower, tempDownloadSuffix), strings.HasPrefix(name, "."):
		return false
	}
	return filepath.Ext(name) != ""
}

// findMaster locates id's master on disk, trying preferredExt first and then the usual
// image extensions (lowercase; see NormalizeExtensions). Returns the extension as found.
func (fm *FileManager) findMaster(id, preferredExt string) (string, int64, bool) {
	seen := map[string]bool{}
	for _, ext := range []string{preferredExt, strings.ToLower(preferredExt), ".jpg", ".jpeg", ".png"} {
		if ext == "" || seen[ext] {
			continue
		}
		seen[ext] = true
		path, err := fm.GetMasterPath(id, ext)
		if err != nil {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return ext, info.Size(), true
		}
	}
	return "", 0, false
}

// NormalizeExtensions renames cached masters and derivatives whose image extension is
// not lowercase (e.g. "<id>.JPG" from a ".JPG" URL) to the lowercase form, so the same
// lookup works on case-sensitive (Linux) and case-insensitive (Windows/macOS) filesystems.
// If a lowercase file already exists as a different file, the uppercase duplicate is
// removed. Returns old path -> new path for every file that changed name.
func (fm *FileManager) NormalizeExtensions() map[string]string {
	renamed := make(map[string]string)
	normalize := func(path string) {
		name := filepath.Base(path)
		trimmed := strings.TrimSuffix(name, ".spice_tmp")
		ext := filepath.Ext(trimmed)
		lower := strings.ToLower(ext)
		if ext == lower || !isImageExt(lower) || trimmed != name {
			return
		}
		target := filepath.Join(filepath.Dir(path), strings.TrimSuffix(name, ext)+lower)
		srcInfo, err := os.Stat(path)
		if err != nil {
			return
		}
		if dstInfo, err := os.Stat(target); err == nil && !os.SameFile(srcInfo, dstInfo) {
			// Case-sensitive filesystem with both spellings: keep the lowercase file.
			if err := os.Remove(path); err == nil {
				renamed[path] = target
			}
			return
		}
		// Same file (case-insensitive filesystem) or no lowercase file yet: case-only
		// renames are supported by os.Rename on Windows, macOS and Linux.
		if err := os.Rename(path, target); err != nil {
			log.Printf("FileManager: Failed to normalize extension of %s: %v", path, err)
			return
		}
		renamed[path] = target
	}

	if entries, err := os.ReadDir(fm.rootDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				normalize(filepath.Join(fm.rootDir, e.Name()))
			}
		}
	}
	_ = filepath.Walk(filepath.Join(fm.rootDir, FittedRootDir), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			normalize(path)
		}
		return nil
	})
	if len(renamed) > 0 {
		log.Printf("FileManager: Normalized %d cached file extensions to lowercase.", len(renamed))
	}
	return renamed
}

// MasterFileInfo describes a master file found on disk.
type MasterFileInfo struct {
	Ext  string // Extension as it appears on disk (e.g. ".jpg")
	Size int64  // Logical file size in bytes (os.FileInfo.Size, not block usage)
}

// ScanMasters lists the master files in the root directory, keyed by image ID.
// Sizes come from the directory listing (logical size), never from disk block usage.
func (fm *FileManager) ScanMasters() (map[string]MasterFileInfo, error) {
	entries, err := os.ReadDir(fm.rootDir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]MasterFileInfo{}, nil
		}
		return nil, err
	}
	masters := make(map[string]MasterFileInfo, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !isMasterCandidate(name) {
			continue
		}
		ext := filepath.Ext(name)
		info, err := e.Info()
		if err != nil {
			continue // Vanished between ReadDir and Info
		}
		masters[strings.TrimSuffix(name, ext)] = MasterFileInfo{Ext: ext, Size: info.Size()}
	}
	if fm.scanHook != nil {
		fm.scanHook()
	}
	return masters, nil
}

// orphanDeletePacing spaces out orphan deletions to keep disk I/O gentle.
const orphanDeletePacing = 50 * time.Millisecond

// IDFiles lists the files on disk that belong to one image ID.
type IDFiles struct {
	Masters     []string // Root-directory files named <id><ext>
	Derivatives []string // Fitted files named <id><ext> (or <id><ext>.spice_tmp)
}

// ListFilesByID indexes the cache tree by image ID in one pass (one ReadDir of the root,
// one walk of the fitted tree). If want is non-nil, only IDs for which it returns true
// are included.
func (fm *FileManager) ListFilesByID(want func(id string) bool) map[string]*IDFiles {
	out := make(map[string]*IDFiles)
	get := func(id string) *IDFiles {
		f := out[id]
		if f == nil {
			f = &IDFiles{}
			out[id] = f
		}
		return f
	}

	if entries, err := os.ReadDir(fm.rootDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !isMasterCandidate(e.Name()) {
				continue
			}
			name := e.Name()
			id := strings.TrimSuffix(name, filepath.Ext(name))
			if want == nil || want(id) {
				f := get(id)
				f.Masters = append(f.Masters, filepath.Join(fm.rootDir, name))
			}
		}
	}

	fittedRoot := filepath.Join(fm.rootDir, FittedRootDir)
	_ = filepath.Walk(fittedRoot, func(path string, info os.FileInfo, err error) error {
		if fm.closing.Load() {
			return filepath.SkipAll
		}
		if err != nil || info.IsDir() {
			return nil
		}
		trimmed := strings.TrimSuffix(info.Name(), ".spice_tmp")
		ext := filepath.Ext(trimmed)
		if !isImageExt(ext) {
			return nil
		}
		id := strings.TrimSuffix(trimmed, ext)
		if want == nil || want(id) {
			f := get(id)
			f.Derivatives = append(f.Derivatives, path)
		}
		return nil
	})
	return out
}

// removePaths deletes the given files, sleeping pace between removals. Files that are
// already gone are not failures; any other failure (e.g. a file held open on Windows) is
// returned so the caller keeps its bookkeeping and retries later.
func (fm *FileManager) removePaths(paths []string, pace time.Duration) error {
	removeFile := os.Remove
	if fm.removeFn != nil {
		removeFile = fm.removeFn
	}
	var errs error
	for _, p := range paths {
		if fm.closing.Load() {
			return errors.Join(errs, errors.New("aborted by shutdown"))
		}
		if pace > 0 {
			time.Sleep(pace)
		}
		if err := removeFile(p); err != nil && !os.IsNotExist(err) {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

// sweepStaleTempDownloads removes abandoned partial downloads (crash or kill mid-transfer).
// Younger temp files may belong to a download still in flight and are left alone.
func (fm *FileManager) sweepStaleTempDownloads() int {
	removed := 0
	entries, err := os.ReadDir(fm.rootDir)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), tempDownloadSuffix) {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > staleTempDownloadAge {
			if err := os.Remove(filepath.Join(fm.rootDir, e.Name())); err == nil {
				removed++
			}
		}
	}
	return removed
}

// runAsync runs fn in a tracked background goroutine so Close can drain it.
func (fm *FileManager) runAsync(fn func()) {
	if fm.closing.Load() {
		return
	}
	fm.wg.Add(1)
	go func() {
		defer fm.wg.Done()
		fn()
	}()
}

// GetMasterPath returns the absolute path for the Master (Raw) image.
// Master images are stored directly in the root directory.
func (fm *FileManager) GetMasterPath(id string, ext string) (string, error) {
	if err := fm.validateID(id); err != nil {
		return "", err
	}
	if strings.Contains(ext, "..") || strings.Contains(ext, string(filepath.Separator)) {
		return "", fmt.Errorf("invalid extension")
	}
	return filepath.Join(fm.rootDir, id+ext), nil
}

// GetDerivativePath returns the path for a processed image based on the type.
// derivativeType is now a relative path segment (e.g., "fitted/quality/standard")
func (fm *FileManager) GetDerivativePath(id string, ext string, derivativeType string) (string, error) {
	if err := fm.validateID(id); err != nil {
		return "", err
	}
	// Basic sanitation for derivative path segments
	if strings.Contains(derivativeType, "..") {
		return "", fmt.Errorf("invalid derivative type")
	}
	return filepath.Join(fm.rootDir, derivativeType, id+ext), nil
}

// DerivativeExists checks if a specific derivative exists on disk.
// derivativeDir should be the resolution folder name (e.g. "1920x1080")
func (fm *FileManager) DerivativeExists(id string, ext string, derivativeDir string) bool {
	path, err := fm.GetDerivativePath(id, ext, derivativeDir)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// DeepDeleteBatch removes all physical files (Master and Derivatives) associated with a list of IDs.
// It is significantly more efficient than calling DeepDelete in a loop for multiple IDs.
func (fm *FileManager) DeepDeleteBatch(ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	idMap := make(map[string]bool)
	for _, id := range ids {
		idMap[id] = true
	}

	filesToDelete := []string{}

	// 1. Scan Master (Root)
	entries, err := os.ReadDir(fm.rootDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			ext := filepath.Ext(name)
			fileID := strings.TrimSuffix(name, ext)
			if idMap[fileID] {
				filesToDelete = append(filesToDelete, filepath.Join(fm.rootDir, name))
				log.Debugf("DeepDeleteBatch: Found Master file %s", name)
			}
		}
	} else {
		log.Printf("DeepDeleteBatch: Failed to read root dir: %v", err)
	}

	// 2. Scan Derivatives (Recursive in FittedRoot)
	fittedRoot := filepath.Join(fm.rootDir, FittedRootDir)
	log.Debugf("DeepDeleteBatch: Scanning fitted root %s for %d IDs", fittedRoot, len(ids))

	err = filepath.Walk(fittedRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("DeepDeleteBatch: Error accessing path %s: %v", path, err)
			return nil // Skip access errors
		}
		if !info.IsDir() {
			name := info.Name()
			trimmed := strings.TrimSuffix(name, ".spice_tmp")
			ext := filepath.Ext(trimmed)
			fileID := strings.TrimSuffix(trimmed, ext)
			if idMap[fileID] {
				filesToDelete = append(filesToDelete, path)
				log.Debugf("DeepDeleteBatch: Found Derivative file %s", path)
			}
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		log.Printf("DeepDeleteBatch: Error walking fitted dir: %v", err)
	}

	log.Debugf("DeepDeleteBatch: Total files to delete: %d", len(filesToDelete))

	for _, f := range filesToDelete {
		if fm.closing.Load() {
			log.Debug("DeepDeleteBatch: Aborted remaining deletions due to shutdown.")
			return nil
		}
		if err := os.Remove(f); err != nil {
			if !os.IsNotExist(err) {
				log.Printf("DeepDeleteBatch: Failed to delete %s: %v", f, err)
			}
		} else {
			log.Debugf("DeepDeleteBatch: Successfully deleted %s", f)
		}
	}

	return nil
}

// DeepDelete removes the Master image and ALL its derivatives.
// It searches for files with the given ID in all known directories.
func (fm *FileManager) DeepDelete(id string) error {
	log.Printf("[DeepDelete] Requested for ID: %s", id)
	return fm.DeepDeleteBatch([]string{id})
}

// CleanupOrphans removes files from the root directory and subdirectories
// that are NOT present in the knownIDs map. It also removes abandoned temp downloads.
// The knownIDs map is a snapshot; prefer ImageStore.CleanupOrphanFiles, which re-checks
// ownership at delete time. This variant remains for stores without that support.
//
// IDs are matched case-insensitively as well as exactly, so the outcome is the same on
// case-insensitive (Windows/macOS defaults) and case-sensitive (Linux) filesystems: a file
// whose name differs from a known ID only by case is kept rather than deleted, because on
// a case-insensitive filesystem it IS that known image's file.
func (fm *FileManager) CleanupOrphans(knownIDs map[string]bool) {
	log.Print("FileManager: Starting orphan cleanup...")
	deletedCount := 0

	knownFold := make(map[string]bool, len(knownIDs))
	for id, ok := range knownIDs {
		if ok {
			knownFold[strings.ToLower(id)] = true
		}
	}
	isKnown := func(id string) bool {
		return knownIDs[id] || knownFold[strings.ToLower(id)]
	}

	deletedCount += fm.sweepStaleTempDownloads()

	// 1. Clean Root (Masters)
	entries, err := os.ReadDir(fm.rootDir)
	if err == nil {
		for _, entry := range entries {
			if fm.closing.Load() {
				log.Debug("CleanupOrphans: Aborted orphan cleanup due to shutdown.")
				return
			}
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			ext := filepath.Ext(name)
			if !isImageExt(ext) {
				continue
			}

			id := strings.TrimSuffix(name, ext)

			if !isKnown(id) {
				fullPath := filepath.Join(fm.rootDir, name)
				time.Sleep(50 * time.Millisecond) // Pacer
				if err := os.Remove(fullPath); err == nil {
					deletedCount++
				}
			}
		}
	}

	// 2. Clean Derivatives (Recursive in FittedRoot)
	fittedRoot := filepath.Join(fm.rootDir, FittedRootDir)
	err = filepath.Walk(fittedRoot, func(path string, info os.FileInfo, err error) error {
		if fm.closing.Load() {
			return filepath.SkipAll
		}
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			name := info.Name()
			trimmed := strings.TrimSuffix(name, ".spice_tmp")
			ext := filepath.Ext(trimmed)
			if !isImageExt(ext) {
				return nil
			}

			id := strings.TrimSuffix(trimmed, ext)

			if !isKnown(id) {
				// Orphan
				time.Sleep(50 * time.Millisecond) // Pacer
				if err := os.Remove(path); err == nil {
					deletedCount++
				}
			}
		}
		return nil
	})

	if err != nil && !os.IsNotExist(err) {
		log.Printf("FileManager: Error walking fitted dir during cleanup: %v", err)
	}

	log.Printf("FileManager: Orphan cleanup finished. Removed %d files.", deletedCount)
}

// DeleteDerivatives removes ONLY the processed versions of an image, keeping the Master.
// This is used when invalidating cache due to settings changes (Smart Fit, etc.).
func (fm *FileManager) DeleteDerivatives(id string) error {
	filesToDelete := []string{}

	// Recursive walk in fitted directory
	fittedRoot := filepath.Join(fm.rootDir, FittedRootDir)
	err := filepath.Walk(fittedRoot, func(path string, info os.FileInfo, err error) error {
		if fm.closing.Load() {
			return filepath.SkipAll
		}
		if err != nil {
			return nil // Skip access errors
		}
		if !info.IsDir() {
			name := info.Name()
			trimmed := strings.TrimSuffix(name, ".spice_tmp")
			ext := filepath.Ext(trimmed)
			fileID := strings.TrimSuffix(trimmed, ext)
			if fileID == id {
				filesToDelete = append(filesToDelete, path)
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("DeleteDerivatives: Error walking fitted dir: %v", err)
	}

	for _, f := range filesToDelete {
		if fm.closing.Load() {
			return nil
		}
		if err := os.Remove(f); err != nil {
			// Suppress benign errors
			if strings.Contains(err.Error(), "used by another process") || strings.Contains(err.Error(), "access is denied") {
				log.Debugf("DeleteDerivatives: Skipped locked file %s: %v", f, err)
			} else {
				log.Printf("DeleteDerivatives: Failed to delete %s: %v", f, err)
			}
		}
	}

	return nil
}

// AsyncDeepDelete launches DeepDelete in a tracked background goroutine.
func (fm *FileManager) AsyncDeepDelete(id string) {
	if fm.closing.Load() {
		return
	}
	fm.wg.Add(1)
	go func() {
		defer fm.wg.Done()
		_ = fm.DeepDelete(id)
	}()
}

// AsyncDeepDeleteBatch launches DeepDeleteBatch in a tracked background goroutine.
func (fm *FileManager) AsyncDeepDeleteBatch(ids []string) {
	if fm.closing.Load() || len(ids) == 0 {
		return
	}
	fm.wg.Add(1)
	go func() {
		defer fm.wg.Done()
		_ = fm.DeepDeleteBatch(ids)
	}()
}

// AsyncCleanupOrphans launches CleanupOrphans in a tracked background goroutine.
func (fm *FileManager) AsyncCleanupOrphans(knownIDs map[string]bool) {
	if fm.closing.Load() {
		return
	}
	fm.wg.Add(1)
	go func() {
		defer fm.wg.Done()
		fm.CleanupOrphans(knownIDs)
	}()
}

// AsyncDeleteDerivatives launches DeleteDerivatives in a tracked background goroutine.
func (fm *FileManager) AsyncDeleteDerivatives(id string) {
	if fm.closing.Load() {
		return
	}
	fm.wg.Add(1)
	go func() {
		defer fm.wg.Done()
		_ = fm.DeleteDerivatives(id)
	}()
}

// AsyncDeleteDerivativesBatch launches DeleteDerivatives for a slice of IDs.
func (fm *FileManager) AsyncDeleteDerivativesBatch(ids []string) {
	if fm.closing.Load() || len(ids) == 0 {
		return
	}
	fm.wg.Add(1)
	go func() {
		defer fm.wg.Done()
		for _, id := range ids {
			if fm.closing.Load() {
				return
			}
			_ = fm.DeleteDerivatives(id)
		}
	}()
}

// WaitTimeout waits for all in-flight asynchronous operations to finish up to timeout.
// Returns true if all finished cleanly, false if timed out.
func (fm *FileManager) WaitTimeout(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		fm.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		log.Printf("FileManager: in-flight operations timed out after %v during shutdown", timeout)
		return false
	}
}

// Close marks the FileManager as closing and drains in-flight operations with a bounded timeout.
func (fm *FileManager) Close() {
	fm.closing.Store(true)
	fm.WaitTimeout(500 * time.Millisecond)
}

// RenameAllAssets renames the Master image and ALL its derivatives to a new ID.
// This is used for namespacing migrations.
func (fm *FileManager) RenameAllAssets(oldID, newID string) error {
	log.Printf("[FileManager] Renaming assets: %s -> %s", oldID, newID)

	// 1. Rename Master (Root)
	entries, err := os.ReadDir(fm.rootDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			ext := filepath.Ext(name)
			fileID := strings.TrimSuffix(name, ext)
			if fileID == oldID {
				oldPath := filepath.Join(fm.rootDir, name)
				newPath := filepath.Join(fm.rootDir, newID+ext)
				log.Debugf("FileManager: Renaming Master %s -> %s", oldPath, newPath)
				if err := os.Rename(oldPath, newPath); err != nil {
					log.Printf("FileManager: Failed to rename Master %s: %v", oldPath, err)
				}
			}
		}
	}

	// 2. Rename Derivatives (Recursive in FittedRoot)
	fittedRoot := filepath.Join(fm.rootDir, FittedRootDir)
	err = filepath.Walk(fittedRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			name := info.Name()
			ext := filepath.Ext(name)
			fileID := strings.TrimSuffix(name, ext)
			if fileID == oldID {
				newPath := filepath.Join(filepath.Dir(path), newID+ext)
				log.Debugf("FileManager: Renaming Derivative %s -> %s", path, newPath)
				if err := os.Rename(path, newPath); err != nil {
					log.Printf("FileManager: Failed to rename Derivative %s: %v", path, err)
				}
			}
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		log.Printf("FileManager: Error walking fitted dir during rename: %v", err)
	}

	return nil
}

// GetDimensions returns the width and height of an image file on disk.
func (fm *FileManager) GetDimensions(path string) (int, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()

	img, _, err := image.DecodeConfig(file)
	if err != nil {
		return 0, 0, err
	}
	return img.Width, img.Height, nil
}
