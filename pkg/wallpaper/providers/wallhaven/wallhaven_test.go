package wallhaven

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	fynetest "fyne.io/fyne/v2/test"
	"github.com/dixieflatline76/Spice/v2/pkg/provider"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
	"github.com/dixieflatline76/Spice/v2/pkg/wallpaper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProvider_EnrichImage(t *testing.T) {
	// Setup mock server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check URL
		if r.URL.Path != "/api/v1/w/test_id" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Return mock response
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"uploader": {
					"username": "TestUser"
				}
			}
		}`))
	}))
	defer ts.Close()

	client := &http.Client{
		Transport: &mockTransport{
			RoundTripFunc: func(req *http.Request) (*http.Response, error) {
				// Redirect to mock server
				u, _ := req.URL.Parse(ts.URL)
				req.URL.Scheme = u.Scheme
				req.URL.Host = u.Host
				return http.DefaultTransport.RoundTrip(req)
			},
		},
	}

	proc := &Provider{
		httpClient: client,
		cfg:        &wallpaper.Config{},
	}

	// Test case
	img := provider.Image{ID: "test_id", Attribution: ""}
	enriched, err := proc.EnrichImage(context.Background(), img)

	assert.NoError(t, err)
	assert.Equal(t, "TestUser", enriched.Attribution)

	// Test case: Already has attribution
	img2 := provider.Image{ID: "test_id_2", Attribution: "Existing"}
	enriched2, err := proc.EnrichImage(context.Background(), img2)
	assert.NoError(t, err)
	assert.Equal(t, "Existing", enriched2.Attribution)

	// Test case: API Error (Non-200)
	tsError := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer tsError.Close()

	clientError := &http.Client{
		Transport: &mockTransport{
			RoundTripFunc: func(req *http.Request) (*http.Response, error) {
				u, _ := req.URL.Parse(tsError.URL)
				req.URL.Scheme = u.Scheme
				req.URL.Host = u.Host
				return http.DefaultTransport.RoundTrip(req)
			},
		},
	}
	providerError := &Provider{httpClient: clientError, cfg: &wallpaper.Config{}}

	img3 := provider.Image{ID: "error_id", Attribution: ""}
	enriched3, err := providerError.EnrichImage(context.Background(), img3)
	assert.NoError(t, err) // Should not error, just return original
	assert.Equal(t, "", enriched3.Attribution)

	// Test case: Malformed JSON
	tsBadJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{invalid_json`))
	}))
	defer tsBadJSON.Close()

	clientBadJSON := &http.Client{
		Transport: &mockTransport{
			RoundTripFunc: func(req *http.Request) (*http.Response, error) {
				u, _ := req.URL.Parse(tsBadJSON.URL)
				req.URL.Scheme = u.Scheme
				req.URL.Host = u.Host
				return http.DefaultTransport.RoundTrip(req)
			},
		},
	}
	providerBadJSON := &Provider{httpClient: clientBadJSON, cfg: &wallpaper.Config{}}

	img4 := provider.Image{ID: "bad_json_id", Attribution: ""}
	enriched4, err := providerBadJSON.EnrichImage(context.Background(), img4)
	assert.Error(t, err) // Should error on decode failure
	assert.Contains(t, err.Error(), "failed to decode")
	assert.Equal(t, "", enriched4.Attribution)
}

func TestProvider_WithResolution(t *testing.T) {
	p := &Provider{}
	width, height := 1920, 1080

	tests := []struct {
		name     string
		inputURL string
		expected string
	}{
		{
			name:     "No existing resolution params",
			inputURL: "https://wallhaven.cc/api/v1/search?q=anime",
			expected: "https://wallhaven.cc/api/v1/search?atleast=1920x1080&q=anime",
		},
		{
			name:     "Existing atleast param",
			inputURL: "https://wallhaven.cc/api/v1/search?q=anime&atleast=2560x1440",
			expected: "https://wallhaven.cc/api/v1/search?q=anime&atleast=2560x1440",
		},
		{
			name:     "Existing resolutions param",
			inputURL: "https://wallhaven.cc/api/v1/search?q=anime&resolutions=1920x1080",
			expected: "https://wallhaven.cc/api/v1/search?q=anime&resolutions=1920x1080",
		},
		{
			name:     "Existing ratios param only fixes the shape, so the floor is still added",
			inputURL: "https://wallhaven.cc/api/v1/search?q=anime&ratios=16x9",
			expected: "https://wallhaven.cc/api/v1/search?atleast=1920x1080&q=anime&ratios=16x9",
		},
		{
			name:     "Ratios with an explicit atleast keeps the user's atleast",
			inputURL: "https://wallhaven.cc/api/v1/search?q=anime&ratios=16x9&atleast=2560x1440",
			expected: "https://wallhaven.cc/api/v1/search?q=anime&ratios=16x9&atleast=2560x1440",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.WithResolution(tt.inputURL, width, height)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestProvider_ParseURL(t *testing.T) {
	p := &Provider{}

	tests := []struct {
		name      string
		input     string
		want      string
		wantErr   bool
		errSubstr string
	}{
		{
			name:  "User Favorites URL",
			input: "https://wallhaven.cc/user/TestUser/favorites/123",
			want:  "https://wallhaven.cc/api/v1/collections/TestUser/123",
		},
		{
			name:  "Search URL Simple",
			input: "https://wallhaven.cc/search",
			want:  "https://wallhaven.cc/api/v1/search",
		},

		// Main Categories
		{
			name:  "Main Category - Latest",
			input: "https://wallhaven.cc/latest",
			want:  "https://wallhaven.cc/api/v1/search?order=desc&sorting=date_added",
		},
		{
			name:  "Main Category - Toplist",
			input: "https://wallhaven.cc/toplist",
			want:  "https://wallhaven.cc/api/v1/search?order=desc&sorting=toplist&topRange=1M",
		},
		{
			name:  "Main Category - Hot",
			input: "https://wallhaven.cc/hot",
			want:  "https://wallhaven.cc/api/v1/search?order=desc&sorting=hot",
		},
		{
			name:  "Main Category - Random",
			input: "https://wallhaven.cc/random?seed=123",
			want:  "https://wallhaven.cc/api/v1/search?order=desc&sorting=random",
		},
		{
			name:  "Main Category - Random (No Seed)",
			input: "https://wallhaven.cc/random",
			want:  "https://wallhaven.cc/api/v1/search?order=desc&sorting=random",
		},

		// Standard Search
		{
			name:  "Search URL With Query",
			input: "https://wallhaven.cc/search?q=anime",
			want:  "https://wallhaven.cc/api/v1/search?q=anime",
		},
		{
			name:  "API Collection URL (Pass-through)",
			input: "https://wallhaven.cc/api/v1/collections/TestUser/123",
			want:  "https://wallhaven.cc/api/v1/collections/TestUser/123",
		},
		{
			name:  "API Search URL (Pass-through)",
			input: "https://wallhaven.cc/api/v1/search?q=nature",
			want:  "https://wallhaven.cc/api/v1/search?q=nature",
		},
		{
			name:  "URL with API Key (Should be stripped)",
			input: "https://wallhaven.cc/api/v1/search?q=cats&apikey=secret123",
			want:  "https://wallhaven.cc/api/v1/search?q=cats",
		},
		{
			name:  "URL with Page (Should be stripped)",
			input: "https://wallhaven.cc/api/v1/search?q=cats&page=2",
			want:  "https://wallhaven.cc/api/v1/search?q=cats",
		},
		{
			name:      "Unsupported URL",
			input:     "https://google.com",
			wantErr:   true,
			errSubstr: "not supported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := p.ParseURL(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errSubstr != "" {
					assert.Contains(t, err.Error(), tt.errSubstr)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestProvider_FetchImages(t *testing.T) {
	// Setup mock server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Basic validation of injected params
		query := r.URL.Query()
		if query.Get("apikey") != "test-api-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if query.Get("page") != "1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{
					"id": "img1",
					"path": "https://w.wallhaven.cc/full/img1.jpg",
					"short_url": "https://wh.cc/img1",
					"file_type": "image/jpeg",
					"file_size": 31457280,
					"uploader": { "username": "User1" }
				},
				{
					"id": "img2",
					"path": "https://w.wallhaven.cc/full/img2.png",
					"short_url": "https://wh.cc/img2",
					"file_type": "image/png",
					"uploader": { "username": "User2" }
				}
			]
		}`))
	}))
	defer ts.Close()

	client := &http.Client{
		Transport: &mockTransport{
			RoundTripFunc: func(req *http.Request) (*http.Response, error) {
				// Redirect to mock server
				u, _ := req.URL.Parse(ts.URL)
				req.URL.Scheme = u.Scheme
				req.URL.Host = u.Host
				return http.DefaultTransport.RoundTrip(req)
			},
		},
	}

	// Create Provider
	p := NewProvider(&wallpaper.Config{}, client)
	p.SetAPIKeyForTesting("test-api-key")

	// Test Success
	images, err := p.FetchImages(context.Background(), "https://wallhaven.cc/api/v1/search", 1)
	assert.NoError(t, err)
	assert.Len(t, images, 2)
	assert.Equal(t, "img1", images[0].ID)
	assert.Equal(t, "User1", images[0].Attribution)
	assert.Equal(t, int64(31457280), images[0].FileSize, "file_size from search results must reach provider.Image for the pre-request size cap")
	assert.Equal(t, int64(0), images[1].FileSize, "missing file_size means unknown")

	// Test Error Case: Bad API Key (401)
	pBad := NewProvider(&wallpaper.Config{}, client)
	pBad.SetAPIKeyForTesting("wrong-key")
	_, err = pBad.FetchImages(context.Background(), "https://wallhaven.cc/api/v1/search", 1)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "status: 401")
}

// mockTransport allows mocking HTTP responses
type mockTransport struct {
	RoundTripFunc func(req *http.Request) (*http.Response, error)
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.RoundTripFunc(req)
}

func TestProvider_CreateQueryPanel_EditConfig(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	cfg := wallpaper.GetConfig(app.Preferences())
	p := NewProvider(cfg, &http.Client{})

	panel := p.CreateQueryPanel(nil, "")
	var list *schema.QueryListItem
	for _, sec := range panel.Sections {
		for _, it := range sec.Items {
			if ql, ok := it.(schema.QueryListItem); ok {
				list = &ql
			}
		}
	}
	require.NotNil(t, list)
	require.NotNil(t, list.EditConfig)
	require.NotNil(t, list.EditConfig.UpdateHandler)
	assert.Equal(t, WallhavenURLRegexp, list.EditConfig.URLValidator)

	id, err := cfg.AddImageQuery("Edit me", "https://wallhaven.cc/api/v1/search?q=edit-before", false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cfg.RemoveImageQuery(id) }) // re-registered below after the re-key

	// Valid user-facing URL is parsed to the API form and stored; the URL edit re-keys the query.
	id, err = list.EditConfig.UpdateHandler(id, "Edited", "https://wallhaven.cc/search?q=edit-after")
	require.NoError(t, err)
	t.Cleanup(func() { _ = cfg.RemoveImageQuery(id) })
	q, ok := cfg.GetQuery(id)
	require.True(t, ok)
	assert.Equal(t, "Edited", q.Description)
	assert.Equal(t, "https://wallhaven.cc/api/v1/search?q=edit-after", q.URL)

	// The display URL shown in the list round-trips to the stored URL (no spurious "URL changed").
	disp := list.GetDisplayURL(schema.Query{ID: id, URL: q.URL})
	require.NotNil(t, disp)
	assert.Regexp(t, WallhavenURLRegexp, disp.String())
	sameID, err := list.EditConfig.UpdateHandler(id, "Edited", disp.String())
	require.NoError(t, err)
	assert.Equal(t, id, sameID, "no URL change: the ID is kept")
	q2, _ := cfg.GetQuery(id)
	assert.Equal(t, q.URL, q2.URL)

	// Invalid URL is rejected and nothing changes.
	_, err = list.EditConfig.UpdateHandler(id, "Edited", "https://example.com/not-wallhaven")
	assert.Error(t, err)
	q3, _ := cfg.GetQuery(id)
	assert.Equal(t, q.URL, q3.URL)
}

func TestProvider_WithResolution_AnySizeAddsNothing(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	cfg := wallpaper.GetConfig(app.Preferences())
	cfg.SetMinImageSize(wallpaper.MinImageSizeAny)
	p := NewProvider(cfg, &http.Client{})

	in := "https://wallhaven.cc/api/v1/search?q=anime&ratios=16x9"
	assert.Equal(t, in, p.WithResolution(in, 1920, 1080), "Any size must not add a size floor")

	cfg.SetMinImageSize(wallpaper.MinImageSizeDisplay)
	assert.Contains(t, p.WithResolution(in, 1920, 1080), "atleast=1920x1080")
}

// The size floor is applied to the request only: the stored query URL, and so the query ID derived
// from it, never changes.
func TestProvider_FetchImages_AppliesSizeFloorAtRequestTimeWithoutChangingTheQueryID(t *testing.T) {
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"a","path":"https://w.wallhaven.cc/full/a.jpg","dimension_x":3840,"dimension_y":2160}]}`))
	}))
	defer ts.Close()
	client := &http.Client{Transport: &mockTransport{RoundTripFunc: func(req *http.Request) (*http.Response, error) {
		u, _ := req.URL.Parse(ts.URL)
		req.URL.Scheme, req.URL.Host = u.Scheme, u.Host
		return http.DefaultTransport.RoundTrip(req)
	}}}
	p := NewProvider(&wallpaper.Config{}, client)
	p.SetAPIKeyForTesting("k")

	stored := "https://wallhaven.cc/api/v1/search?q=anime&ratios=16x9"
	idBefore := wallpaper.GenerateQueryID(p.ID() + ":" + stored)

	ctx := context.WithValue(context.Background(), provider.MinResolutionKey, provider.MinResolution{Width: 3440, Height: 1440})
	images, err := p.FetchImages(ctx, stored, 1)
	require.NoError(t, err)
	require.Len(t, images, 1)
	assert.Equal(t, 3840, images[0].Width, "search results carry the size the pre-download check uses")
	assert.Equal(t, 2160, images[0].Height)

	_, err = p.FetchImages(context.Background(), stored, 1)
	require.NoError(t, err)
	require.Len(t, seen, 2)
	assert.Contains(t, seen[0], "atleast=3440x1440", "a ratios-only query gets the floor")
	assert.NotContains(t, seen[1], "atleast", "no floor in the context, none on the request")

	assert.Equal(t, idBefore, wallpaper.GenerateQueryID(p.ID()+":"+stored), "the stored URL, and so the query ID, is unchanged")

	// A query that sets its own atleast or resolutions keeps it.
	seen = nil
	for _, own := range []string{"atleast=1000x1000", "resolutions=1920x1080"} {
		_, err = p.FetchImages(ctx, "https://wallhaven.cc/api/v1/search?q=x&"+own, 1)
		require.NoError(t, err)
	}
	assert.Contains(t, seen[0], "atleast=1000x1000")
	assert.NotContains(t, seen[0], "3440x1440")
	assert.NotContains(t, seen[1], "atleast")
}

func TestProvider_IsASizeFilteringProvider(t *testing.T) {
	var _ provider.SizeFilteringProvider = &Provider{}
	assert.True(t, (&Provider{}).FiltersByMinResolution())
}
