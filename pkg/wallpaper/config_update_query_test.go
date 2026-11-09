package wallpaper

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newUpdateQueryConfig(t *testing.T) *Config {
	t.Helper()
	ResetConfig()
	cfg := GetConfig(NewMockPreferences())
	cfg.Queries = []ImageQuery{}
	return cfg
}

func TestUpdateQuery_DescriptionOnly_NoCallback(t *testing.T) {
	cfg := newUpdateQueryConfig(t)
	var fired atomic.Int32
	cfg.SetQueryURLChangedCallback(func(string, string) { fired.Add(1) })

	id, err := cfg.AddImageQuery("Old", "https://wallhaven.cc/api/v1/search?q=a", true)
	require.NoError(t, err)

	sameID, err := cfg.UpdateQuery(id, "  New desc  ", "https://wallhaven.cc/api/v1/search?q=a")
	require.NoError(t, err)
	assert.Equal(t, id, sameID, "description-only edit keeps the ID")

	q, ok := cfg.GetQuery(id)
	require.True(t, ok)
	assert.Equal(t, "New desc", q.Description)
	assert.Equal(t, "https://wallhaven.cc/api/v1/search?q=a", q.URL)
	assert.True(t, q.Active)

	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, fired.Load(), "description-only edit must not fire the URL-changed callback")
}

func TestUpdateQuery_URLChange_NewIDAndCallbackGetsBoth(t *testing.T) {
	cfg := newUpdateQueryConfig(t)
	type ids struct{ old, new string }
	got := make(chan ids, 1)
	cfg.SetQueryURLChangedCallback(func(oldID, newID string) { got <- ids{oldID, newID} })

	id, err := cfg.AddImageQuery("Old", "https://wallhaven.cc/api/v1/search?q=a", true)
	require.NoError(t, err)
	require.NoError(t, cfg.DisableImageQuery(id))
	require.NoError(t, cfg.EnableImageQuery(id))

	newID, err := cfg.UpdateQuery(id, "Old", "https://wallhaven.cc/api/v1/search?q=b")
	require.NoError(t, err)
	assert.Equal(t, GenerateQueryID("Wallhaven:https://wallhaven.cc/api/v1/search?q=b"), newID,
		"the ID the migration (and AddProviderQuery) would compute")
	assert.NotEqual(t, id, newID)

	select {
	case fired := <-got:
		assert.Equal(t, ids{id, newID}, fired)
	case <-time.After(2 * time.Second):
		t.Fatal("URL-changed callback was not fired")
	}

	_, oldExists := cfg.GetQuery(id)
	assert.False(t, oldExists)
	q, ok := cfg.GetQuery(newID)
	require.True(t, ok)
	assert.Equal(t, "https://wallhaven.cc/api/v1/search?q=b", q.URL)
	assert.True(t, q.Active)
	assert.Equal(t, "Wallhaven", q.Provider)
	assert.False(t, q.Managed)
	assert.Equal(t, "Old", q.Description)
}

func TestUpdateQuery_ManagedRejected(t *testing.T) {
	cfg := newUpdateQueryConfig(t)
	id, err := cfg.AddProviderQuery("Synced", "https://wallhaven.cc/api/v1/collections/u/1", "Wallhaven", false, true)
	require.NoError(t, err)

	_, err = cfg.UpdateQuery(id, "Changed", "https://wallhaven.cc/api/v1/collections/u/2")
	assert.Error(t, err)

	q, _ := cfg.GetQuery(id)
	assert.Equal(t, "Synced", q.Description)
	assert.Equal(t, "https://wallhaven.cc/api/v1/collections/u/1", q.URL)
}

func TestUpdateQuery_DuplicateURLRejected(t *testing.T) {
	cfg := newUpdateQueryConfig(t)
	var fired atomic.Int32
	cfg.SetQueryURLChangedCallback(func(string, string) { fired.Add(1) })

	_, err := cfg.AddImageQuery("A", "https://wallhaven.cc/api/v1/search?q=a", true)
	require.NoError(t, err)
	idB, err := cfg.AddImageQuery("B", "https://wallhaven.cc/api/v1/search?q=b", true)
	require.NoError(t, err)

	_, err = cfg.UpdateQuery(idB, "B", "https://wallhaven.cc/api/v1/search?q=a")
	assert.Error(t, err, "editing to another query's URL is the duplicate case")

	q, _ := cfg.GetQuery(idB)
	assert.Equal(t, "https://wallhaven.cc/api/v1/search?q=b", q.URL)
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, fired.Load())

	// The same URL under a different provider is not a duplicate.
	_, err = cfg.AddPexelsQuery("P", "https://www.pexels.com/search/x/", true)
	require.NoError(t, err)
	_, err = cfg.UpdateQuery(idB, "B", "https://www.pexels.com/search/x/")
	assert.NoError(t, err)
}

func TestUpdateQuery_EmptyRejected(t *testing.T) {
	cfg := newUpdateQueryConfig(t)
	id, err := cfg.AddImageQuery("A", "https://wallhaven.cc/api/v1/search?q=a", true)
	require.NoError(t, err)

	for _, c := range [][2]string{{"   ", "https://wallhaven.cc/api/v1/search?q=a"}, {"A", ""}, {"A", "  "}} {
		_, err := cfg.UpdateQuery(id, c[0], c[1])
		assert.Error(t, err)
	}

	q, _ := cfg.GetQuery(id)
	assert.Equal(t, "A", q.Description)
	assert.Equal(t, "https://wallhaven.cc/api/v1/search?q=a", q.URL)
}

func TestUpdateQuery_UnknownID(t *testing.T) {
	cfg := newUpdateQueryConfig(t)
	_, err := cfg.UpdateQuery("nope", "A", "https://wallhaven.cc/api/v1/search?q=a")
	assert.Error(t, err)
}

func TestAddProviderQuery_AfterEdit_DuplicateChecks(t *testing.T) {
	cfg := newUpdateQueryConfig(t)
	const urlX = "https://wallhaven.cc/api/v1/search?q=x"
	const urlY = "https://wallhaven.cc/api/v1/search?q=y"

	idA, err := cfg.AddImageQuery("A", urlX, true)
	require.NoError(t, err)
	idA, err = cfg.UpdateQuery(idA, "A", urlY)
	require.NoError(t, err)

	// New URL Y is now owned by A: adding it again must be rejected.
	_, err = cfg.AddImageQuery("dup of Y", urlY, true)
	assert.Error(t, err)

	// Old URL X is free again: the edited query no longer carries X's ID.
	idX, err := cfg.AddImageQuery("re-add X", urlX, true)
	require.NoError(t, err)
	assert.NotEqual(t, idA, idX)
}
