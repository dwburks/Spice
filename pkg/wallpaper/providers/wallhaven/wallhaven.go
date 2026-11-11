package wallhaven

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	_ "embed"

	"github.com/dixieflatline76/Spice/v2/pkg/i18n"
	"github.com/dixieflatline76/Spice/v2/pkg/provider"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/setting"
	"github.com/dixieflatline76/Spice/v2/pkg/wallpaper"
	"github.com/dixieflatline76/Spice/v2/util/log"
)

//go:embed wallhaven.png
var iconData []byte

// Provider implements ImageProvider for Wallhaven.
type Provider struct {
	cfg               *wallpaper.Config
	httpClient        *http.Client
	testAPIKey        string
	validatedUsername string // Successfully verified username via API
}

// SetAPIKeyForTesting sets an API key for testing purposes.
func (p *Provider) SetAPIKeyForTesting(key string) {
	p.testAPIKey = key
}

func init() {
	wallpaper.RegisterProvider("Wallhaven", func(cfg *wallpaper.Config, client *http.Client) provider.ImageProvider {
		return NewProvider(cfg, client)
	})
}

// NewProvider creates a new Provider.
func NewProvider(cfg *wallpaper.Config, client *http.Client) *Provider {
	return &Provider{
		cfg:        cfg,
		httpClient: client,
	}
}

func (p *Provider) ID() string {
	return "Wallhaven"
}

func (p *Provider) Name() string {
	return i18n.T("wallhaven")
}

func (p *Provider) Type() provider.ProviderType {
	return provider.TypeCommunity
}

func (p *Provider) GetAttributionType() provider.AttributionType {
	return provider.AttributionBy
}

func (p *Provider) SupportsUserQueries() bool {
	return true
}

func (p *Provider) HomeURL() string {
	return "https://wallhaven.cc"
}

func (p *Provider) GetProviderIcon() interface{} {
	return iconData
}

// ParseURL converts a web URL to an API URL.
func (p *Provider) ParseURL(webURL string) (string, error) {
	log.Debugf("Wallhaven Parsing URL: %s", webURL)
	trimmedURL := strings.TrimSpace(webURL)

	baseURL, err := determineBaseAPIURL(trimmedURL)
	if err != nil {
		return "", err
	}

	return cleanQueryParams(baseURL)
}

func determineBaseAPIURL(trimmedURL string) (string, error) {
	switch {
	case UserFavoritesRegex.MatchString(trimmedURL):
		matches := UserFavoritesRegex.FindStringSubmatch(trimmedURL)
		return fmt.Sprintf(WallhavenAPICollectionURL, matches[1], matches[2]), nil

	case MainCategoryRegex.MatchString(trimmedURL):
		matches := MainCategoryRegex.FindStringSubmatch(trimmedURL)
		category := matches[1] // "latest", "toplist", etc.
		baseURL := WallhavenAPISearchURL + "?" + mapCategoryToParams(category)
		if len(matches) > 2 && matches[2] != "" {
			baseURL += "&" + strings.TrimPrefix(matches[2], "?")
		}
		return baseURL, nil

	case SearchRegex.MatchString(trimmedURL):
		matches := SearchRegex.FindStringSubmatch(trimmedURL)
		apiSearchBase := WallhavenAPISearchURL
		if len(matches) == 3 && matches[2] != "" {
			return apiSearchBase + matches[2], nil
		}
		return apiSearchBase, nil

	case APICollectionRegex.MatchString(trimmedURL), APISearchRegex.MatchString(trimmedURL):
		return trimmedURL, nil

	default:
		return "", fmt.Errorf("entered URL is currently not supported: %s", trimmedURL)
	}
}

func cleanQueryParams(baseURL string) (string, error) {
	parsedURL, parseErr := url.Parse(baseURL)
	if parseErr != nil {
		return "", fmt.Errorf("internal error parsing transformed URL '%s': %w", baseURL, parseErr)
	}
	q := parsedURL.Query()
	paramsChanged := false

	if q.Has("apikey") {
		q.Del("apikey")
		paramsChanged = true
	}
	if q.Has("page") {
		q.Del("page")
		paramsChanged = true
	}
	if q.Get("sorting") == "random" && q.Has("seed") {
		q.Del("seed")
		paramsChanged = true
	}

	if paramsChanged {
		parsedURL.RawQuery = q.Encode()
	}
	log.Debugf("Wallhaven Parsed URL: %s", parsedURL.String())
	return parsedURL.String(), nil
}

// WithResolution adds an `atleast` size floor to the API URL unless the query already sets
// `atleast` or `resolutions` (`ratios` alone does not stop it: it only fixes the shape). With
// the Minimum image size setting on "Any size" nothing is added.
func (p *Provider) WithResolution(apiURL string, width, height int) string {
	if p.cfg != nil && p.cfg.Preferences != nil && p.cfg.GetMinImageSize().MaxUpscale() == 0 {
		return apiURL
	}
	u, err := url.Parse(apiURL)
	if err != nil {
		return apiURL // Return original if parsing fails
	}

	q := u.Query()
	if addSizeFloor(q, width, height) {
		u.RawQuery = q.Encode()
		return u.String()
	}

	return apiURL
}

// addSizeFloor sets `atleast` on q unless the query already has `atleast` or `resolutions`.
// It reports whether it changed q.
func addSizeFloor(q url.Values, width, height int) bool {
	if width <= 0 || height <= 0 || q.Has("atleast") || q.Has("resolutions") {
		return false
	}
	q.Set("atleast", fmt.Sprintf("%dx%d", width, height))
	return true
}

// FiltersByMinResolution marks Wallhaven as a provider that applies provider.MinResolutionKey.
func (p *Provider) FiltersByMinResolution() bool { return true }

// DiscoverCollections fetches all public collections for a given username.
func (p *Provider) DiscoverCollections(ctx context.Context, username string) ([]wallpaper.ImageQuery, error) {
	if username == "" {
		log.Printf("[ERROR] Wallhaven: DiscoverCollections called with empty username")
		return nil, errors.New("username cannot be empty")
	}

	apiKey := p.cfg.GetWallhavenAPIKey()
	if p.testAPIKey != "" {
		apiKey = p.testAPIKey
	}

	log.Debugf("Wallhaven: Discovering collections for user: %s (using API Key: %v)", username, apiKey != "")

	url := fmt.Sprintf(WallhavenAPICollectionsRootURL, username)
	if apiKey != "" {
		url += "?apikey=" + apiKey
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		log.Printf("[ERROR] Wallhaven: HTTP request failed: %v", err)
		return nil, err
	}
	defer resp.Body.Close()

	log.Debugf("Wallhaven: API response status: %d", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden {
			return nil, errors.New("access denied: account might be private or API key invalid")
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, errors.New("user not found on Wallhaven")
		}
		return nil, fmt.Errorf("wallhaven API error: status %d", resp.StatusCode)
	}

	var result CollectionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("[ERROR] Wallhaven: Failed to decode collection JSON: %v", err)
		return nil, err
	}

	log.Debugf("Wallhaven: Found %d remote collections", len(result.Data))

	queries := make([]wallpaper.ImageQuery, 0, len(result.Data))
	for _, col := range result.Data {
		apiURL := fmt.Sprintf(WallhavenAPICollectionURL, username, fmt.Sprint(col.ID))
		privacy := "Private"
		if col.Public == 1 {
			privacy = "Public"
		}
		queries = append(queries, wallpaper.ImageQuery{
			ID:          wallpaper.GenerateQueryID(p.ID() + ":" + apiURL),
			Description: fmt.Sprintf("❤ Collection: %s - %s - %d images", col.Label, privacy, col.Count),
			URL:         apiURL,
			Active:      false, // Default to inactive for new synced collections
			Provider:    "Wallhaven",
			Managed:     true,
		})
	}

	return queries, nil
}

// Sync performs the actual sync of collections into the config.
func (p *Provider) Sync(ctx context.Context) error {
	syncEnabled := p.cfg.GetWallhavenSyncEnabled()
	log.Debugf("Wallhaven: Sync starting. Enabled: %v", syncEnabled)

	if !syncEnabled {
		log.Debugf("Wallhaven: Sync disabled, clearing managed queries.")
		p.cfg.SyncManagedQueries("Wallhaven", nil)
		return nil
	}

	username := p.cfg.GetWallhavenUsername()
	log.Debugf("Wallhaven: Sync using username: '%s'", username)

	if username == "" {
		log.Debugf("Wallhaven: Sync skipped due to empty username.")
		return nil
	}
	remoteQueries, err := p.DiscoverCollections(ctx, username)
	if err != nil {
		log.Printf("[ERROR] Wallhaven: Sync discovery failed: %v", err)
		return err
	}

	p.cfg.SyncManagedQueries("Wallhaven", remoteQueries)
	return nil
}

// SyncCollections is a legacy wrapper (optional cleanup)
func (p *Provider) SyncCollections(ctx context.Context, username string) error {
	return p.Sync(ctx)
}

// mapCategoryToParams maps a Wallhaven Main Category to API query parameters reference in alphabetical order.
func mapCategoryToParams(category string) string {
	switch category {
	case "latest":
		return "order=desc&sorting=date_added"
	case "toplist":
		return "order=desc&sorting=toplist&topRange=1M"
	case "hot":
		return "order=desc&sorting=hot"
	case "random":
		// Random sorting needs a seed to stay consistent during paging, but for a "wallpaper changer"
		// usually we want freshness. Here we set sorting=random.
		// If page=2 is requested later, the API handles it if using same seed, but here we are generating the *base* query.
		return "order=desc&sorting=random"
	default:
		return ""
	}
}

// FetchImages fetches images from Wallhaven.
func (p *Provider) FetchImages(ctx context.Context, apiURL string, page int) ([]provider.Image, error) {
	u, err := url.Parse(apiURL)
	if err != nil {
		return nil, fmt.Errorf("invalid API URL: %w", err)
	}

	q := u.Query()
	if p.testAPIKey != "" {
		q.Set("apikey", p.testAPIKey)
	} else {
		q.Set("apikey", p.cfg.GetWallhavenAPIKey())
	}
	q.Set("page", fmt.Sprint(page))

	// Size floor for this request only (the stored query URL, and so its ID, is not touched):
	// the smallest image usable on a connected display, from the Minimum image size setting.
	if mr, ok := ctx.Value(provider.MinResolutionKey).(provider.MinResolution); ok {
		addSizeFloor(q, mr.Width, mr.Height)
	}

	// We need desktop dimensions for default query if no resolutions/atleast specified.
	// Provider doesn't have direct access to OS anymore.
	// We can skip this optimization or add it back if critical.
	// For now, I'll skip it to keep it simple and consistent with interface.
	// If needed, I can add `os` to `Provider` struct.

	// Actually, let's try to keep it if possible.
	// But I don't have access to `wp.os` here.
	// I'll skip it for now. Users can specify resolutions in query if they want.
	// Or I can add `os` to `NewProvider` later.

	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch from Wallhaven: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wallhaven API returned status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var response imgSrvcResponse
	if err = json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	log.Debugf("Wallhaven API returned %d images", len(response.Data))

	var images []provider.Image
	for _, item := range response.Data {
		log.Debugf("Wallhaven Image ID: %s, Uploader: '%s'", item.ID, item.Uploader.Username)
		images = append(images, provider.Image{
			ID:          item.ID,
			Path:        item.Path,
			ViewURL:     item.ShortURL,
			Attribution: item.Uploader.Username,
			Provider:    p.ID(),
			FileType:    item.FileType,
			Width:       item.DimensionX,
			Height:      item.DimensionY,
			FileSize:    item.FileSize,
		})
	}

	return images, nil
}

// EnrichImage fetches additional details for the image (e.g. attribution) if missing.
func (p *Provider) EnrichImage(ctx context.Context, img provider.Image) (provider.Image, error) {
	if img.Attribution != "" {
		return img, nil // Already has attribution
	}

	// Wallhaven ID is usually the last part of the path or available in the struct
	// We use the ID from the image struct
	apiURL := fmt.Sprintf("https://wallhaven.cc/api/v1/w/%s", img.ID)

	apiKey := p.cfg.GetWallhavenAPIKey()
	if p.testAPIKey != "" {
		apiKey = p.testAPIKey
	}
	if apiKey != "" {
		apiURL += "?apikey=" + apiKey
	}

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return img, fmt.Errorf("failed to create enrichment request: %w", err)
	}
	req.Header.Set("User-Agent", "SpiceWallpaperManager/v0.1.0")

	start := time.Now()
	resp, err := p.httpClient.Do(req)
	log.Debugf("EnrichImage HTTP Request took %v", time.Since(start))
	if err != nil {
		return img, fmt.Errorf("failed to fetch enrichment data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Log but don't fail hard, just return original image
		log.Debugf("Wallhaven enrichment failed for %s: status %d", img.ID, resp.StatusCode)
		return img, nil
	}

	var response struct {
		Data struct {
			Uploader struct {
				Username string `json:"username"`
			} `json:"uploader"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return img, fmt.Errorf("failed to decode enrichment response: %w", err)
	}

	if response.Data.Uploader.Username != "" {
		img.Attribution = response.Data.Uploader.Username
		log.Debugf("Enriched Wallhaven image %s with uploader: %s", img.ID, img.Attribution)
	}

	return img, nil
}

// --- Wallhaven JSON Structs ---

// imgSrvcResponse is the response from the Wallhaven API
type imgSrvcResponse struct {
	Data []ImgSrvcImage `json:"data"`
	Meta struct {
		LastPage int `json:"meta"`
	} `json:"meta"`
}

// ImgSrvcImage represents an image from the image service.
type ImgSrvcImage struct {
	Path       string   `json:"path"`
	ID         string   `json:"id"`
	ShortURL   string   `json:"short_url"`
	FileType   string   `json:"file_type"`
	Ratio      string   `json:"ratio"`
	DimensionX int      `json:"dimension_x"`
	DimensionY int      `json:"dimension_y"`
	FileSize   int64    `json:"file_size"`
	Thumbs     Thumbs   `json:"thumbs"`
	Uploader   Uploader `json:"uploader"`
}

// Uploader represents the user who uploaded the image.
type Uploader struct {
	Username string `json:"username"`
}

// Thumbs represents the different sizes of the image.
type Thumbs struct {
	Large    string `json:"large"`
	Original string `json:"original"`
	Small    string `json:"small"`
}

// CollectionResponse is the response from the Wallhaven API
type CollectionResponse struct {
	Data []Collection `json:"data"`
}

// Collection is the wallpaper collection
type Collection struct {
	ID     int    `json:"id"`
	Label  string `json:"label"`
	Views  int    `json:"views"`
	Public int    `json:"public"`
	Count  int    `json:"count"`
}

// --- UI Integration ---

// --- UI Implementation (Pure Go) ---

func (p *Provider) Title() string {
	return "wallhaven"
}

// CreateSettingsPanel returns the declarative UI for Wallhaven settings.
func (p *Provider) CreateSettingsPanel(sm setting.SettingsManager) *schema.PanelSchema {
	const apiKeyIdent = "wallhavenAPIKey"
	const userIdent = "Wallhaven Username"
	const syncIdent = "WallhavenSyncEnabled"

	// Self-healing: if sync is enabled but prerequisites are missing, fix the config.
	if p.cfg.GetWallhavenSyncEnabled() && (p.cfg.GetWallhavenUsername() == "" || p.cfg.GetWallhavenAPIKey() == "") {
		p.cfg.SetWallhavenSyncEnabled(false)
	}

	return &schema.PanelSchema{
		Description: i18n.T("wallhaven is a repository for high-quality, high-resolution wallpapers."),
		Sections: []schema.SectionSchema{
			{
				Title: i18n.T("Account"),
				Items: []schema.ItemSchema{
					schema.SecretItem{
						Name:         apiKeyIdent,
						Label:        i18n.T("wallhaven API key"),
						InitialValue: p.cfg.GetWallhavenAPIKey(),
						Placeholder:  i18n.T("Enter your wallhaven API key"),
						OnVerify: func(key string) error {
							ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
							defer cancel()
							return CheckWallhavenAPIKeyWithContext(ctx, key)
						},
						ApplyFunc: func(key string) {
							p.cfg.SetWallhavenAPIKey(key)
						},
						OnClear: func() {
							p.validatedUsername = ""
							// Clear ALL native state directly to avoid triggering the sync
							// BoolItem's ApplyFunc (which spawns a goroutine).
							p.cfg.SetWallhavenAPIKey("")
							p.cfg.SetWallhavenUsername("")
							p.cfg.SetWallhavenSyncEnabled(false)
							p.cfg.SyncManagedQueries("Wallhaven", nil)
							// Now safely update the UI baselines
							sm.ResetSettings(
								setting.SettingReset{Name: apiKeyIdent, Value: ""},
								setting.SettingReset{Name: userIdent, Value: ""},
								setting.SettingReset{Name: syncIdent, Value: false},
							)
						},
					},
					schema.HyperlinkItem{
						Text: i18n.T("Get an API key for restricted content"),
						URL:  "https://wallhaven.cc/settings/account",
					},
					schema.TextItem{
						Name:          userIdent,
						Label:         i18n.T("wallhaven username"),
						Help:          i18n.T("Needed to sync your wallhaven collections."),
						InitialValue:  p.cfg.GetWallhavenUsername(),
						PlaceHolder:   i18n.T("Your wallhaven.cc username"),
						DisplayStatus: true,
						SkipApply:     true,
						EnabledIf: func() bool {
							// Require a saved API key before allowing username entry
							apiKey, _ := sm.GetValue(apiKeyIdent).(string)
							return apiKey != ""
						},
						ApplyFunc: func(s string) {
							p.cfg.SetWallhavenUsername(s)
						},
					},
					schema.AsyncButtonItem{
						Name:            "VerifyUsername",
						ButtonText:      i18n.T("Verify username"),
						LoadingText:     i18n.T("Verifying..."),
						Style:           schema.ButtonStylePrimary,
						TargetStatusKey: userIdent,
						IconName:        "search",
						VisibleIf: func() bool {
							curr := sm.GetValue(userIdent)
							base := sm.GetBaseline(userIdent)
							if curr == nil || base == nil {
								return false
							}
							currStr := curr.(string)
							baseStr := base.(string)
							return currStr != "" && currStr != p.validatedUsername && currStr != baseStr
						},
						OnPressed: func() error {
							currUser, _ := sm.GetValue(userIdent).(string)
							apiKeyVal, _ := sm.GetValue(apiKeyIdent).(string)
							if apiKeyVal == "" {
								return errors.New(i18n.T("Save an API key before verifying your username."))
							}
							ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
							defer cancel()
							return CheckWallhavenUsername(ctx, currUser, apiKeyVal)
						},
						OnCompleted: func(err error) {
							if err == nil {
								p.validatedUsername, _ = sm.GetValue(userIdent).(string)
								sm.CommitSetting(userIdent)
							} else {
								// Show explicit error feedback before clearing
								sm.ShowError(err)
								// Clear the invalid username and reset baseline
								sm.SetValue(userIdent, "")
								sm.SeedBaseline(userIdent, "")
								sm.RefreshUI()
							}
						},
					},
				},
			},
			{
				Title: i18n.T("Sync"),
				Items: []schema.ItemSchema{
					schema.BoolItem{
						Name:         syncIdent,
						Label:        i18n.T("Sync collections"),
						Help:         i18n.T("Keeps your wallhaven collections in Spice; new ones are added switched off."),
						InitialValue: p.cfg.GetWallhavenSyncEnabled(),
						NeedsRefresh: true,
						EnabledIf: func() bool {
							curr := sm.GetValue(userIdent)
							base := sm.GetBaseline(userIdent)
							if curr == nil || base == nil {
								return false
							}
							currStr := curr.(string)
							baseStr := base.(string)
							return currStr != "" && (currStr == p.validatedUsername || currStr == baseStr)
						},
						ApplyFunc: func(b bool) {
							p.cfg.SetWallhavenSyncEnabled(b)
							go func() {
								_ = p.Sync(context.Background())
								sm.SetSettingStatus(syncIdent, i18n.T("Collections synced"), schema.ImportanceSuccess)
								sm.RefreshUI()
							}()
						},
					},
				},
			},
		},
	}
}

// CreateQueryPanel creates the image query management panel.
func (p *Provider) CreateQueryPanel(sm setting.SettingsManager, pendingUrl string) *schema.PanelSchema {
	addCfg := schema.AddQueryConfig{
		Title:           i18n.T("Add wallhaven collection"),
		URLPlaceholder:  "https://wallhaven.cc/search?q=...",
		URLValidator:    WallhavenURLRegexp,
		URLErrorMsg:     i18n.T("Enter a valid wallhaven URL."),
		DescPlaceholder: i18n.T("Description, e.g. Landscapes"),
		AddHandler: func(desc, url string, active bool) (string, error) {
			apiURL, err := p.ParseURL(url)
			if err != nil {
				return "", err
			}
			return p.cfg.AddImageQuery(desc, apiURL, active)
		},
	}

	editCfg := addCfg
	editCfg.Title = i18n.T("Edit wallhaven collection")
	editCfg.AddHandler = nil
	editCfg.UpdateHandler = func(id, desc, url string) (string, error) {
		apiURL, err := p.ParseURL(url)
		if err != nil {
			return "", err
		}
		return p.cfg.UpdateQuery(id, desc, apiURL)
	}

	if pendingUrl != "" {
		sm.ShowAddQueryDialog(addCfg, pendingUrl, "", sm.RefreshUI)
	}

	return &schema.PanelSchema{
		Sections: []schema.SectionSchema{
			{
				Title:       i18n.T("Collections"),
				Description: i18n.T("Paste a wallhaven search or collection URL to add it."),
				Items: []schema.ItemSchema{
					schema.QueryListItem{
						GetQueries: func() []schema.Query {
							queries := p.cfg.GetQueries()
							var abstracts []schema.Query
							for _, q := range queries {
								if q.Provider == p.ID() {
									abstracts = append(abstracts, schema.Query{
										ID:          q.ID,
										URL:         q.URL,
										Description: q.Description,
										Active:      q.Active,
										Managed:     q.Managed,
									})
								}
							}
							return abstracts
						},
						EnableQuery:  p.cfg.EnableImageQuery,
						DisableQuery: p.cfg.DisableImageQuery,
						RemoveQuery:  p.cfg.RemoveImageQuery,
						GetDisplayURL: func(q schema.Query) *url.URL {
							return p.getDisplayURL(q)
						},
						EditConfig: &editCfg,
					},
					schema.ButtonItem{
						Name:       "wallhaven_add",
						ButtonText: i18n.T("Add collection"),
						IconName:   "add",
						OnPressed: func() {
							sm.ShowAddQueryDialog(addCfg, "", "", sm.RefreshUI)
						},
					},
				},
			},
		},
	}
}

// GetAPIPacing implements the PacedProvider interface to space out API calls.
func (p *Provider) GetAPIPacing() time.Duration {
	return 1500 * time.Millisecond // 1.5 seconds per API call (40 RPM max)
}

// GetProcessPacing implements the PacedProvider interface to space out image downloads.
func (p *Provider) GetProcessPacing() time.Duration {
	return 1500 * time.Millisecond // 1.5 seconds per image download
}

func (p *Provider) getDisplayURL(q schema.Query) *url.URL {
	apiVal := q.URL
	if APICollectionIDRegex.MatchString(apiVal) {
		matches := APICollectionIDRegex.FindStringSubmatch(apiVal)
		if len(matches) >= 3 {
			displayURL := fmt.Sprintf("https://wallhaven.cc/user/%s/favorites/%s", matches[1], matches[2])
			res, _ := url.Parse(displayURL)
			return res
		}
	}
	if APISearchRegex.MatchString(apiVal) {
		// Convert API search to web search
		displayURL := strings.Replace(apiVal, "api/v1/search", "search", 1)
		res, _ := url.Parse(displayURL)
		return res
	}
	res, _ := url.Parse(apiVal)
	return res
}
