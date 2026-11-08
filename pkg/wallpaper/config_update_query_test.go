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
	cfg.SetQueryURLChangedCallback(func(string) { fired.Add(1) })

	id, err := cfg.AddImageQuery("Old", "https://wallhaven.cc/api/v1/search?q=a", true)
	require.NoError(t, err)

	require.NoError(t, cfg.UpdateQuery(id, "  New desc  ", "https://wallhaven.cc/api/v1/search?q=a"))

	q, ok := cfg.GetQuery(id)
	require.True(t, ok)
	assert.Equal(t, "New desc", q.Description)
	assert.Equal(t, "https://wallhaven.cc/api/v1/search?q=a", q.URL)
	assert.True(t, q.Active)

	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, fired.Load(), "description-only edit must not fire the URL-changed callback")
}

func TestUpdateQuery_URLChange_FiresCallbackAndKeepsState(t *testing.T) {
	cfg := newUpdateQueryConfig(t)
	got := make(chan string, 1)
	cfg.SetQueryURLChangedCallback(func(id string) { got <- id })

	id, err := cfg.AddImageQuery("Old", "https://wallhaven.cc/api/v1/search?q=a", true)
	require.NoError(t, err)
	require.NoError(t, cfg.DisableImageQuery(id))
	require.NoError(t, cfg.EnableImageQuery(id))

	require.NoError(t, cfg.UpdateQuery(id, "Old", "https://wallhaven.cc/api/v1/search?q=b"))

	select {
	case fired := <-got:
		assert.Equal(t, id, fired)
	case <-time.After(2 * time.Second):
		t.Fatal("URL-changed callback was not fired")
	}

	q, ok := cfg.GetQuery(id)
	require.True(t, ok)
	assert.Equal(t, id, q.ID)
	assert.Equal(t, "https://wallhaven.cc/api/v1/search?q=b", q.URL)
	assert.True(t, q.Active)
	assert.Equal(t, "Wallhaven", q.Provider)
	assert.False(t, q.Managed)
}

func TestUpdateQuery_ManagedRejected(t *testing.T) {
	cfg := newUpdateQueryConfig(t)
	id, err := cfg.AddProviderQuery("Synced", "https://wallhaven.cc/api/v1/collections/u/1", "Wallhaven", false, true)
	require.NoError(t, err)

	err = cfg.UpdateQuery(id, "Changed", "https://wallhaven.cc/api/v1/collections/u/2")
	assert.Error(t, err)

	q, _ := cfg.GetQuery(id)
	assert.Equal(t, "Synced", q.Description)
	assert.Equal(t, "https://wallhaven.cc/api/v1/collections/u/1", q.URL)
}

func TestUpdateQuery_DuplicateURLRejected(t *testing.T) {
	cfg := newUpdateQueryConfig(t)
	var fired atomic.Int32
	cfg.SetQueryURLChangedCallback(func(string) { fired.Add(1) })

	_, err := cfg.AddImageQuery("A", "https://wallhaven.cc/api/v1/search?q=a", true)
	require.NoError(t, err)
	idB, err := cfg.AddImageQuery("B", "https://wallhaven.cc/api/v1/search?q=b", true)
	require.NoError(t, err)

	err = cfg.UpdateQuery(idB, "B", "https://wallhaven.cc/api/v1/search?q=a")
	assert.Error(t, err)

	q, _ := cfg.GetQuery(idB)
	assert.Equal(t, "https://wallhaven.cc/api/v1/search?q=b", q.URL)
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, fired.Load())

	// The same URL under a different provider is not a duplicate.
	_, err = cfg.AddPexelsQuery("P", "https://www.pexels.com/search/x/", true)
	require.NoError(t, err)
	assert.NoError(t, cfg.UpdateQuery(idB, "B", "https://www.pexels.com/search/x/"))
}

func TestUpdateQuery_EmptyRejected(t *testing.T) {
	cfg := newUpdateQueryConfig(t)
	id, err := cfg.AddImageQuery("A", "https://wallhaven.cc/api/v1/search?q=a", true)
	require.NoError(t, err)

	assert.Error(t, cfg.UpdateQuery(id, "   ", "https://wallhaven.cc/api/v1/search?q=a"))
	assert.Error(t, cfg.UpdateQuery(id, "A", ""))
	assert.Error(t, cfg.UpdateQuery(id, "A", "  "))

	q, _ := cfg.GetQuery(id)
	assert.Equal(t, "A", q.Description)
	assert.Equal(t, "https://wallhaven.cc/api/v1/search?q=a", q.URL)
}

func TestUpdateQuery_UnknownID(t *testing.T) {
	cfg := newUpdateQueryConfig(t)
	assert.Error(t, cfg.UpdateQuery("nope", "A", "https://wallhaven.cc/api/v1/search?q=a"))
}

func TestAddProviderQuery_AfterEdit_DuplicateChecks(t *testing.T) {
	cfg := newUpdateQueryConfig(t)
	const urlX = "https://wallhaven.cc/api/v1/search?q=x"
	const urlY = "https://wallhaven.cc/api/v1/search?q=y"

	idA, err := cfg.AddImageQuery("A", urlX, true)
	require.NoError(t, err)
	require.NoError(t, cfg.UpdateQuery(idA, "A", urlY))

	// New URL Y is now owned by A: adding it again must be rejected (stored-URL check).
	_, err = cfg.AddImageQuery("dup of Y", urlY, true)
	assert.Error(t, err)

	// Old URL X: A still carries the ID hashed from X, so the ID check still rejects it.
	_, err = cfg.AddImageQuery("re-add X", urlX, true)
	t.Logf("re-adding the old URL after edit: err=%v", err)
	assert.Error(t, err, "documented behavior: old-URL ID is retained by the edited query")
}
