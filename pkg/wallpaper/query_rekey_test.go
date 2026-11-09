package wallpaper

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// restartStore reloads config (running migrations) and the cache, then applies the
// migration remap and runs the startup Sync, like Activate does.
func restartStore(t *testing.T, prefs Preferences, fm *FileManager, dir string) (*Config, *ImageStore, *Plugin) {
	t.Helper()
	ResetConfig()
	cfg := GetConfig(prefs)
	store := NewImageStore()
	store.SetAsyncSave(false)
	store.SetFileManager(fm, filepath.Join(dir, "cache.json"))
	require.NoError(t, store.LoadCache())
	wp := &Plugin{cfg: cfg, store: store, fm: fm}
	wp.applyQueryIDRemap()
	wp.syncStoreWithConfig()
	return cfg, store, wp
}

// Root cause of the empty rotation after a URL edit: the edit kept the old ID, the next
// startup's migration renamed it, and every image fetched after the edit (tagged with the
// old ID) was retained as belonging to an unknown query.
func TestQueryURLEdit_SurvivesRestartWithoutOrphans(t *testing.T) {
	prefs := NewMockPreferences()
	ResetConfig()
	cfg := GetConfig(prefs)
	id, err := cfg.AddImageQuery("Q", "https://wallhaven.cc/api/v1/search?q=before", true)
	require.NoError(t, err)
	newID, err := cfg.UpdateQuery(id, "Q", "https://wallhaven.cc/api/v1/search?q=after")
	require.NoError(t, err)
	// Fetches tag images with the query's ID as stored in the config after the edit.
	var fetchID string
	for _, q := range cfg.GetQueries() {
		if q.URL == "https://wallhaven.cc/api/v1/search?q=after" {
			fetchID = q.ID
		}
	}
	require.Equal(t, newID, fetchID)

	store, fm, dir := newRetentionStore(t)
	for _, imgID := range []string{"Wallhaven_a1", "Wallhaven_a2", "Wallhaven_a3"} {
		addWithMaster(t, store, fm, imgID, fetchID, 10) // downloaded after the edit
	}
	require.NoError(t, store.SaveCache())

	cfg2, store2, _ := restartStore(t, prefs, fm, dir)
	q, ok := cfg2.GetQuery(newID)
	require.True(t, ok, "no migration rename: the edit already assigned the final ID")
	assert.Equal(t, queryIDFor("Wallhaven", "https://wallhaven.cc/api/v1/search?q=after"), q.ID)
	assert.True(t, q.Active, "Active preserved")
	assert.Empty(t, cfg2.QueryIDRemap())
	assert.Equal(t, 3, store2.Count(), "nothing left rotation")
	assert.Equal(t, 0, store2.RetainedCount(), "nothing retained as orphaned")
	for _, img := range store2.List() {
		assert.Equal(t, newID, img.SourceQueryID)
	}
}

// Repair of data written by the old build: the query kept its pre-edit ID, images carry it,
// and the migration renames the query on load. The recorded rename re-tags the images.
func TestQueryIDMigration_RetagsImagesBeforeFirstSync(t *testing.T) {
	prefs := NewMockPreferences()
	ResetConfig()
	cfg := GetConfig(prefs)
	staleID, err := cfg.AddImageQuery("Q", "https://wallhaven.cc/api/v1/search?q=before", true)
	require.NoError(t, err)
	// What the old UpdateQuery did: change the URL but keep the ID.
	cfg.mu.Lock()
	idx, _ := cfg.findQueryIndex(staleID)
	cfg.Queries[idx].URL = "https://wallhaven.cc/api/v1/search?q=after"
	cfg.save()
	cfg.mu.Unlock()

	store, fm, dir := newRetentionStore(t)
	addWithMaster(t, store, fm, "Wallhaven_b1", staleID, 10)
	addWithMaster(t, store, fm, "Wallhaven_b2", staleID, 10)
	require.NoError(t, store.SaveCache())

	cfg2, store2, _ := restartStore(t, prefs, fm, dir)
	wantID := queryIDFor("Wallhaven", "https://wallhaven.cc/api/v1/search?q=after")
	assert.Equal(t, map[string]string{staleID: wantID}, cfg2.QueryIDRemap(), "the migration records the rename")
	assert.Equal(t, 2, store2.Count(), "re-tagged images stay in rotation")
	assert.Equal(t, 0, store2.RetainedCount())
	for _, img := range store2.List() {
		assert.Equal(t, wantID, img.SourceQueryID)
	}
}

// A fresh default config no longer needs (or logs) the ID rename on every load.
func TestFreshConfig_NoQueryIDRename(t *testing.T) {
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	assert.Empty(t, cfg.QueryIDRemap(), "backfill assigns the final provider-scoped IDs directly")
	for _, q := range cfg.Queries {
		if q.Provider != "Favorites" {
			assert.Equal(t, queryIDFor(q.Provider, q.URL), q.ID, q.Description)
		}
	}
}
