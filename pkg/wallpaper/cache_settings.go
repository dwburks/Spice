package wallpaper

import (
	"strconv"

	"github.com/dixieflatline76/Spice/v2/pkg/i18n"
)

// Storage settings (General tab, "Wallpaper Cycle & Cache"): option labels, usage summary
// and the apply hooks. Pure domain code; the UI builder only maps these onto schema items.

// formatSizeMB renders a size option in megabytes ("500 MB", "2 GB"; 0 = "No limit").
func formatSizeMB(mb int) string {
	switch {
	case mb == 0:
		return i18n.T("No limit")
	case mb >= 1024 && mb%1024 == 0:
		return i18n.Tf("{{.Size}} GB", map[string]any{"Size": strconv.Itoa(mb / 1024)})
	default:
		return i18n.Tf("{{.Size}} MB", map[string]any{"Size": strconv.Itoa(mb)})
	}
}

// formatBytes renders a byte count as whole megabytes below 1 GB, otherwise as gigabytes
// with one decimal ("840 MB", "1.2 GB").
func formatBytes(b int64) string {
	const gb = 1024 * bytesPerMB
	if b < gb {
		return i18n.Tf("{{.Size}} MB", map[string]any{"Size": strconv.FormatInt((b+bytesPerMB/2)/bytesPerMB, 10)})
	}
	tenths := (b*10 + gb/2) / gb
	size := strconv.FormatInt(tenths/10, 10)
	if tenths%10 != 0 {
		size += "." + strconv.FormatInt(tenths%10, 10)
	}
	return i18n.Tf("{{.Size}} GB", map[string]any{"Size": size})
}

// MaxDownloadSizeOptionLabels returns the labels for MaxDownloadSizeOptionsMB.
func MaxDownloadSizeOptionLabels() []string {
	out := make([]string, len(MaxDownloadSizeOptionsMB))
	for i, mb := range MaxDownloadSizeOptionsMB {
		out[i] = formatSizeMB(mb)
	}
	return out
}

// CacheDiskBudgetOptionLabels returns the labels for CacheDiskBudgetOptionsMB.
func CacheDiskBudgetOptionLabels() []string {
	out := make([]string, len(CacheDiskBudgetOptionsMB))
	for i, mb := range CacheDiskBudgetOptionsMB {
		out[i] = formatSizeMB(mb)
	}
	return out
}

// CacheUsage returns the bytes of master files on disk that belong to images in rotation
// and to retained images (kept for reuse).
func (s *ImageStore) CacheUsage() (active, retained int64) {
	if s.fm == nil {
		return 0, 0
	}
	masters, err := s.fm.ScanMasters() // File I/O outside s.mu
	if err != nil {
		return 0, 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, mf := range masters {
		if s.idSet[id] {
			active += mf.Size
		} else if _, ok := s.retained[id]; ok {
			retained += mf.Size
		}
	}
	return active, retained
}

// CacheUsageSummary describes current cache disk use for the settings panel.
func (wp *Plugin) CacheUsageSummary() string {
	store, ok := wp.store.(*ImageStore)
	if !ok {
		return ""
	}
	active, retained := store.CacheUsage()
	return i18n.Tf("Using {{.Total}} ({{.Active}} in rotation, {{.Kept}} kept)", map[string]any{
		"Total":  formatBytes(active + retained),
		"Active": formatBytes(active),
		"Kept":   formatBytes(retained),
	})
}

// ApplyMaxDownloadSizeIndex stores the download cap option and forgets this session's
// "too large" verdicts, which may no longer hold under the new cap.
func (wp *Plugin) ApplyMaxDownloadSizeIndex(i int) {
	wp.cfg.SetMaxDownloadSizeIndex(i)
	wp.oversizedIDs.Range(func(k, _ any) bool {
		wp.oversizedIDs.Delete(k)
		return true
	})
}

// ApplyCacheDiskBudgetIndex stores the disk budget option and trims retained files to it
// right away (images in rotation are never touched).
func (wp *Plugin) ApplyCacheDiskBudgetIndex(i int) {
	wp.cfg.SetCacheDiskBudgetIndex(i)
	wp.scheduleCacheMaintenance(false)
}
