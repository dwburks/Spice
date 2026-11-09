package wallpaper

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/dixieflatline76/Spice/v2/pkg/curation"
	"github.com/dixieflatline76/Spice/v2/pkg/i18n"
	"github.com/dixieflatline76/Spice/v2/pkg/provider"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/setting"
)

// PrefsPanelBuilder constructs the preferences UI.
type PrefsPanelBuilder struct {
	plugin *Plugin
	sm     setting.SettingsManager
}

func NewPrefsPanelBuilder(p *Plugin, sm setting.SettingsManager) *PrefsPanelBuilder {
	if sm != nil {
		sm.RegisterOnSettingsSaved(func() {
			p.cfg.save()
		})
	}
	return &PrefsPanelBuilder{
		plugin: p,
		sm:     sm,
	}
}

// generalSection is a group of General settings that lands on one page.
type generalSection struct {
	page    string
	section schema.SectionSchema
}

// buildGeneralSections returns every General setting grouped by page (settings map in plans/ui-redesign.md).
// Item names, ApplyFuncs and behavior are unchanged; only their grouping moved.
func (b *PrefsPanelBuilder) buildGeneralSections() []generalSection {
	return []generalSection{
		{page: schema.NavPageWallpaper, section: schema.SectionSchema{
			ID:    "rotation",
			Title: i18n.T("Rotation"),
			Items: []schema.ItemSchema{
				schema.TextItem{
					Name:         "changeFrequency",
					Label:        i18n.T("Change frequency"),
					Help:         i18n.T("How often each display gets a new wallpaper."),
					InitialValue: fmt.Sprintf("%d", b.plugin.cfg.GetWallpaperChangeFrequency()),
					IsNumeric:    true,
					Presets: []schema.TextPreset{
						{Label: i18n.T("Every 15 minutes"), Value: "15"},
						{Label: i18n.T("Every 30 minutes"), Value: "30"},
						{Label: i18n.T("Every hour"), Value: "60"},
						{Label: i18n.T("Every 3 hours"), Value: "180"},
						{Label: i18n.T("Every 6 hours"), Value: "360"},
						{Label: i18n.T("Every 12 hours"), Value: "720"},
						{Label: i18n.T("Daily"), Value: "1440"},
						{Label: i18n.T("Never"), Value: "0"},
					},
					CustomLabel: i18n.T("Custom…"),
					Suffix:      i18n.T("minutes"),
					Validator: func(s string) error {
						val, err := strconv.Atoi(s)
						if err != nil || val < 0 {
							return errors.New(i18n.T("Enter a whole number of minutes, or 0."))
						}
						return nil
					},
					ApplyFunc: func(val string) {
						freq, _ := strconv.Atoi(val)
						b.plugin.ChangeWallpaperFrequency(Frequency(freq), true)
					},
					NeedsRefresh: true,
				},
				schema.BoolItem{
					Name:         "staggerChanges",
					Label:        i18n.T("Stagger display changes"),
					Help:         i18n.T("Change displays a few seconds apart instead of all at once."),
					InitialValue: b.plugin.cfg.GetStaggerMonitorChanges(),
					ApplyFunc: func(val bool) {
						b.plugin.cfg.SetStaggerMonitorChanges(val)
					},
				},
				schema.BoolItem{
					Name:         "chgImgOnStart",
					Label:        i18n.T("Change wallpaper at startup"),
					Help:         i18n.T("Turn off to change wallpapers only on the timer or when you ask."),
					InitialValue: b.plugin.cfg.GetChgImgOnStart(),
					ApplyFunc: func(val bool) {
						b.plugin.cfg.SetChgImgOnStart(val)
					},
				},
				schema.BoolItem{
					Name:         "nightlyRefresh",
					Label:        i18n.T("Refresh wallpapers nightly"),
					Help:         i18n.T("Download new wallpapers each night; cache cleanup runs either way."),
					InitialValue: b.plugin.cfg.GetNightlyRefresh(),
					ApplyFunc: func(val bool) {
						b.plugin.cfg.SetNightlyRefresh(val)
					},
				},
			},
		}},
		{page: schema.NavPageWallpaper, section: schema.SectionSchema{
			ID:    "storage",
			Title: i18n.T("Storage"),
			Items: []schema.ItemSchema{
				schema.SelectItem{
					Name:         "cacheSize",
					Label:        i18n.T("Cache size"),
					Help:         i18n.T("How many images stay in rotation; more means more variety and more disk space."),
					Options:      setting.StringOptions(GetCacheSizes()),
					InitialValue: b.plugin.cfg.GetCacheSizeIndex(),
					ApplyFunc: func(val interface{}) {
						b.plugin.cfg.SetCacheSizeIndex(val.(int))
					},
				},
				schema.SelectItem{
					Name:         "maxDownloadSize",
					Label:        i18n.T("Max download size"),
					Help:         i18n.T("Larger images are skipped to save bandwidth and disk space."),
					Options:      MaxDownloadSizeOptionLabels(),
					InitialValue: b.plugin.cfg.GetMaxDownloadSizeIndex(),
					ApplyFunc: func(val interface{}) {
						b.plugin.ApplyMaxDownloadSizeIndex(val.(int))
					},
				},
				schema.SelectItem{
					Name:         "cacheDiskSpace",
					Label:        i18n.T("Cache disk space"),
					Help:         i18n.T("Images in rotation are always kept; older reusable images are removed first when the cache is full."),
					Options:      CacheDiskBudgetOptionLabels(),
					InitialValue: b.plugin.cfg.GetCacheDiskBudgetIndex(),
					ApplyFunc: func(val interface{}) {
						b.plugin.ApplyCacheDiskBudgetIndex(val.(int))
					},
				},
				schema.LabelItem{
					ID:         "cacheDiskUsage",
					Text:       b.plugin.CacheUsageSummary(),
					Importance: schema.ImportanceLow,
				},
				schema.ConfirmButtonItem{
					Name:           "clearCache",
					Label:          i18n.T("Clear wallpaper cache"),
					Help:           i18n.T("Delete every downloaded wallpaper and its resized copies."),
					ButtonText:     i18n.T("Clear cache"),
					ConfirmTitle:   i18n.T("Clear wallpaper cache?"),
					ConfirmMessage: i18n.T("All downloaded images will be deleted, and new wallpapers will need an internet connection."),
					Importance:     schema.ImportanceDanger,
					OnPressed:      b.plugin.ClearCache,
				},
				schema.ConfirmButtonItem{
					Name:           "resetAvoidSet",
					Label:          i18n.T("Blocked images"),
					Help:           i18n.T("Clear the list so blocked images can download again on the next refresh."),
					ButtonText:     i18n.T("Reset"),
					ConfirmTitle:   i18n.T("Reset blocked images?"),
					ConfirmMessage: i18n.T("This can't be undone."),
					Importance:     schema.ImportanceMedium,
					OnPressed:      b.plugin.cfg.ResetAvoidSet,
				},
			},
		}},
		{page: schema.NavPageSmartFit, section: schema.SectionSchema{
			ID:    "fit",
			Title: i18n.T("Fit"),
			Items: []schema.ItemSchema{
				schema.SelectItem{
					Name:         "smartFitMode",
					Label:        i18n.T("Smart fit mode"),
					Help:         i18n.T("Disabled keeps the original image, Quality skips images that don't match your screen's shape, and Flexibility lets high-resolution images crop more."),
					Options:      GetSmartFitModes(),
					InitialValue: int(b.plugin.cfg.GetSmartFitMode()),
					ApplyFunc: func(val interface{}) {
						mode := SmartFitMode(val.(int))
						b.plugin.cfg.SetSmartFitMode(mode)
					},
					NeedsRefresh: true,
				},
			},
		}},
		{page: schema.NavPageSmartFit, section: schema.SectionSchema{
			ID:    "faces",
			Title: i18n.T("Faces"),
			Items: []schema.ItemSchema{
				schema.BoolItem{
					Name:         "faceCrop",
					Label:        i18n.T("Face crop"),
					Help:         i18n.T("Crops tightly around the largest face, which suits portraits."),
					InitialValue: b.plugin.cfg.GetFaceCropEnabled(),
					NeedsRefresh: true,
					EnabledIf: func() bool {
						val := b.sm.GetValue("smartFitMode")
						if val == nil {
							return true
						}
						return SmartFitMode(val.(int)) != SmartFitOff
					},
					OnChanged: func(val bool) {
						if val {
							b.sm.SetValue("faceBoost", false)
						}
					},
					ApplyFunc: func(val bool) {
						b.plugin.cfg.SetFaceCropEnabled(val)
						if val {
							b.plugin.cfg.SetFaceBoostEnabled(false)
						}
					},
				},
				schema.BoolItem{
					Name:         "faceBoost",
					Label:        i18n.T("Face boost"),
					Help:         i18n.T("Keeps faces in frame while balancing the rest of the image."),
					InitialValue: b.plugin.cfg.GetFaceBoostEnabled(),
					NeedsRefresh: true,
					EnabledIf: func() bool {
						val := b.sm.GetValue("smartFitMode")
						if val == nil {
							return true
						}
						return SmartFitMode(val.(int)) != SmartFitOff
					},
					OnChanged: func(val bool) {
						if val {
							b.sm.SetValue("faceCrop", false)
						}
					},
					ApplyFunc: func(val bool) {
						b.plugin.cfg.SetFaceBoostEnabled(val)
						if val {
							b.plugin.cfg.SetFaceCropEnabled(false)
						}
					},
				},
			},
		}},
		{page: schema.NavPageFraming, section: schema.SectionSchema{
			ID:    "framing",
			Title: i18n.T("Framing"),
			Items: []schema.ItemSchema{
				schema.BoolItem{
					Name:         "virtualFramingFallback",
					Label:        i18n.T("Frame mismatched images"),
					Help:         i18n.T("Show images that don't fit your screen in a gallery frame instead of skipping them."),
					InitialValue: b.plugin.cfg.VirtualFramingFallback,
					ApplyFunc: func(val bool) {
						b.plugin.cfg.VirtualFramingFallback = val
					},
					NeedsRefresh: false,
				},
			},
		}},
		{page: schema.NavPageFraming, section: schema.SectionSchema{
			ID:    "look",
			Title: i18n.T("Look"),
			Items: []schema.ItemSchema{
				schema.SelectItem{
					Name:         "virtualWallColor",
					Label:        i18n.T("Wall color"),
					Help:         i18n.T("Sets the wall behind the frame."),
					Options:      []string{i18n.T("Dynamic"), i18n.T("Neutral gallery")},
					InitialValue: int(b.plugin.cfg.VirtualWallColor),
					ApplyFunc: func(val interface{}) {
						b.plugin.cfg.VirtualWallColor = VirtualWallMode(val.(int))
					},
					NeedsRefresh: true,
				},
				schema.BoolItem{
					Name:         "virtualPaperMatting",
					Label:        i18n.T("Paper mat"),
					Help:         i18n.T("Adds a white paper mat between the frame and the artwork."),
					InitialValue: b.plugin.cfg.VirtualPaperMatting,
					ApplyFunc: func(val bool) {
						b.plugin.cfg.VirtualPaperMatting = val
					},
				},
				schema.SliderItem{
					Name:         "virtualFrameSize",
					Label:        i18n.T("Frame size"),
					Help:         i18n.T("Height of the framed artwork as a share of the screen."),
					Min:          50,
					Max:          100,
					Step:         1,
					Format:       "%.0f%%",
					InitialValue: b.plugin.cfg.VirtualFrameSize * 100.0,
					ApplyFunc: func(val float64) {
						b.plugin.cfg.VirtualFrameSize = val / 100.0
					},
				},
			},
		}},
		{page: schema.NavPagePause, section: schema.SectionSchema{
			ID:    "activity",
			Title: i18n.T("Activity"),
			Items: []schema.ItemSchema{
				schema.BoolItem{
					Name:         "pauseWhenBusy",
					Label:        i18n.T("Pause when busy"),
					Help:         i18n.T("Pauses rotation and downloads during fullscreen games, videos, or presentations."),
					InitialValue: b.plugin.cfg.GetPauseWhenBusy(),
					ApplyFunc: func(val bool) {
						b.plugin.cfg.SetPauseWhenBusy(val)
						if b.plugin.gate != nil {
							b.plugin.gate.Wakeup()
						}
					},
				},
				schema.AppPickerItem{
					Name:         "gateGameProcesses",
					Label:        i18n.T("Pause for apps and games"),
					Help:         i18n.T("Pauses wallpaper changes while any of these apps is in the foreground."),
					ButtonText:   i18n.T("Add app or game..."),
					InitialValue: b.plugin.cfg.GetGameProcesses(),
					ApplyFunc: func(val []string) {
						b.plugin.cfg.SetGameProcesses(val)
						if b.plugin.gate != nil {
							b.plugin.gate.Wakeup()
						}
					},
				},
				schema.TextItem{
					Name:         "gateGraceMinutes",
					Label:        i18n.T("Resume grace period"),
					Help:         i18n.T("Minutes rotation stays on after you resume it manually while busy."),
					InitialValue: strconv.Itoa(b.plugin.cfg.GetGateGraceMinutes()),
					IsNumeric:    true,
					Validator: func(s string) error {
						val, err := strconv.Atoi(strings.TrimSpace(s))
						if err != nil || val < 0 {
							return errors.New(i18n.T("Enter a whole number of minutes, or 0."))
						}
						return nil
					},
					ApplyFunc: func(val string) {
						if mins, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && mins >= 0 {
							b.plugin.cfg.SetGateGraceMinutes(mins)
							if b.plugin.gate != nil {
								b.plugin.gate.Wakeup()
							}
						}
					},
				},
			},
		}},
		{page: schema.NavPagePause, section: schema.SectionSchema{
			ID:    "battery",
			Title: i18n.T("Battery"),
			Items: []schema.ItemSchema{
				schema.BoolItem{
					Name:         "batterySaver",
					Label:        i18n.T("Battery saver"),
					Help:         i18n.T("Pauses rotation and background activity on battery below the threshold."),
					InitialValue: b.plugin.cfg.GetBatterySaver(),
					ApplyFunc: func(val bool) {
						b.plugin.cfg.SetBatterySaver(val)
						if b.plugin.gate != nil {
							b.plugin.gate.Wakeup()
						}
					},
				},
				schema.SelectItem{
					Name:         "batterySaverLevel",
					Label:        i18n.T("Battery threshold"),
					Help:         i18n.T("Battery saver starts at or below this charge."),
					Options:      []string{"10%", "15%", "20%", "25%"},
					InitialValue: b.plugin.cfg.GetBatterySaverLevel(),
					ApplyFunc: func(val interface{}) {
						b.plugin.cfg.SetBatterySaverLevel(val.(int))
						if b.plugin.gate != nil {
							b.plugin.gate.Wakeup()
						}
					},
				},
			},
		}},
		{page: schema.NavPageDisplays, section: schema.SectionSchema{
			ID:    "displays",
			Title: i18n.T("Displays"),
			Items: []schema.ItemSchema{
				schema.ButtonItem{
					Name:       "refreshDisplays",
					Label:      i18n.T("Display configuration"),
					Help:       i18n.T("Use after you connect or disconnect a display while Spice is running."),
					ButtonText: i18n.T("Refresh displays"),
					OnPressed: func() {
						b.plugin.SyncMonitors(true)
					},
				},
			},
		}},
		{page: schema.NavPageAdvanced, section: schema.SectionSchema{
			ID:    "updates",
			Title: i18n.T("Updates"),
			Items: []schema.ItemSchema{
				schema.BoolItem{
					Name:         "museumCollectionOTA",
					Label:        i18n.T("Museum collection updates"),
					Help:         i18n.T("Occasionally downloads new curated museum collections without an app update."),
					InitialValue: b.plugin.cfg.GetMuseumCollectionOTA(),
					ApplyFunc: func(val bool) {
						b.plugin.cfg.SetMuseumCollectionOTA(val)
					},
				},
			},
		}},
	}
}

// BuildGeneralTabSchema returns every General setting section in page order (flattened view of
// the General pages, used by tests and anything that needs to look up an item by name).
func (b *PrefsPanelBuilder) BuildGeneralTabSchema() *schema.PanelSchema {
	var sections []schema.SectionSchema
	for _, gs := range b.buildGeneralSections() {
		sections = append(sections, gs.section)
	}
	return &schema.PanelSchema{Sections: sections}
}

// generalPageMeta declares the wallpaper plugin's General pages.
func (b *PrefsPanelBuilder) generalPageMeta() []schema.NavItem {
	return []schema.NavItem{
		{
			ID:          schema.NavPageWallpaper,
			Title:       i18n.T("Wallpaper"),
			Description: i18n.T("How often wallpapers change and how much is kept on disk."),
			IconName:    "image",
			Order:       30,
			Header:      schema.HeaderBanner,
			Banner:      b.plugin.PrimaryWallpaperBanner(),
		},
		{
			ID:          schema.NavPageSmartFit,
			Title:       i18n.T("Smart fit"),
			Description: i18n.T("How images are fitted and cropped to your screens."),
			IconName:    "fullscreen",
			Order:       40,
		},
		{
			ID:          schema.NavPageFraming,
			Title:       i18n.T("Framing"),
			Description: i18n.T("Museum-style frames for images that don't fit your screen."),
			IconName:    "color_palette",
			Order:       50,
		},
		{
			ID:          schema.NavPagePause,
			Title:       i18n.T("Pause and power"),
			Description: i18n.T("Pause rotation during games, presentations or on low battery."),
			IconName:    "pause",
			Order:       60,
		},
		{
			ID:          schema.NavPageDisplays,
			Title:       i18n.T("Displays"),
			Description: i18n.T("Keep Spice in sync with your connected displays."),
			IconName:    "computer",
			Order:       70,
		},
		{
			ID:          schema.NavPageAdvanced,
			Title:       i18n.T("Advanced"),
			Description: i18n.T("Diagnostics and over-the-air updates."),
			IconName:    "settings",
			Order:       80,
		},
	}
}

// BuildGeneralPages returns the wallpaper plugin's pages for the General sidebar group.
func (b *PrefsPanelBuilder) BuildGeneralPages() []schema.NavItem {
	pages := b.generalPageMeta()
	sections := b.buildGeneralSections()
	for i := range pages {
		for _, gs := range sections {
			if gs.page == pages[i].ID {
				if pages[i].Panel == nil {
					pages[i].Panel = &schema.PanelSchema{}
				}
				pages[i].Panel.Sections = append(pages[i].Panel.Sections, gs.section)
			}
		}
	}
	return pages
}

// BuildNavSchema constructs the wallpaper plugin's preferences pages: General pages plus one
// page per source (Sources) and per personal library provider (Library). Museums is a single
// page that drills into one child page per museum.
func (b *PrefsPanelBuilder) BuildNavSchema() *schema.NavSchema {
	sources, library, selected := b.BuildProviderPages()
	return &schema.NavSchema{
		Selected: selected,
		Groups: []schema.NavGroup{
			{ID: schema.NavGroupGeneral, Title: i18n.T("General"), Order: 10, Items: b.BuildGeneralPages()},
			{ID: schema.NavGroupSources, Title: i18n.T("Sources"), Order: 20, Items: sources},
			{ID: schema.NavGroupLibrary, Title: i18n.T("Library"), Order: 30, Items: library},
		},
	}
}

// providerPageOrder fixes the sidebar order of known providers; others follow alphabetically.
var providerPageOrder = map[string]int{
	"Wallhaven":    10,
	"Pexels":       20,
	"Wikimedia":    30,
	"LocalFolder":  10,
	"GooglePhotos": 20,
	"Favorites":    30,
}

const museumsPageOrder = 40

// BuildProviderPages creates one page per provider: community sources and a Museums drill-in page
// for Sources, personal providers for Library. selected is the page the pending URL / focused
// provider resolved to ("" if none).
func (b *PrefsPanelBuilder) BuildProviderPages() (sources []schema.NavItem, library []schema.NavItem, selected string) {
	var museums []schema.NavItem
	var museumProviders []provider.ImageProvider
	for _, name := range b.getSortedProviderIDs() {
		p := b.plugin.providers[name]
		if p == nil {
			continue
		}
		item, focused := b.createProviderPage(p)
		if focused {
			selected = p.ID()
		}
		if item == nil {
			continue
		}
		switch p.Type() {
		case provider.TypePersonal:
			library = append(library, *item)
		case provider.TypeMuseum:
			museums = append(museums, *item)
			museumProviders = append(museumProviders, p)
		default:
			sources = append(sources, *item)
		}
	}

	if len(museums) > 0 {
		sources = append(sources, schema.NavItem{
			ID:          schema.NavPageMuseums,
			Title:       i18n.T("Museums"),
			Description: i18n.T("Open-access collections from the world's great museums."),
			IconName:    "museum",
			Order:       museumsPageOrder,
			Children:    museums,
			Badge:       b.createMuseumsBadgeFunc(museumProviders),
		})
	}

	// Clear any unconsumed pending state so stale values never linger across rebuilds.
	b.plugin.pendingAddUrl = ""
	b.plugin.focusProviderName = ""
	return sources, library, selected
}

func (b *PrefsPanelBuilder) getSortedProviderIDs() []string {
	var names []string
	for n := range b.plugin.providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// createProviderPage builds the sidebar page for one provider. focused reports whether the
// pending URL (browser extension) or the focused provider (tray "Source" item) resolved to it.
func (b *PrefsPanelBuilder) createProviderPage(p provider.ImageProvider) (*schema.NavItem, bool) {
	isPending := false
	pendingURL := ""

	// Check pending state: only providers that support user queries (and match focusProviderName if set)
	// may consume pendingAddUrl. This prevents non-query providers (e.g. Getty, NPM) from stealing
	// pendingAddUrl when BuildProviderPages iterates alphabetically.
	if b.plugin.pendingAddUrl != "" && p.SupportsUserQueries() && (b.plugin.focusProviderName == "" || b.plugin.focusProviderName == p.ID()) {
		if _, err := p.ParseURL(b.plugin.pendingAddUrl); err == nil {
			pendingURL = b.plugin.pendingAddUrl
			isPending = true
			b.plugin.pendingAddUrl = ""
		}
	}
	if b.plugin.focusProviderName == p.ID() {
		isPending = true
		b.plugin.focusProviderName = ""
	}

	content := b.buildProviderContent(p, pendingURL)
	if content == nil {
		return nil, isPending
	}

	iconName := p.ID()
	var iconBytes []byte
	if iconStr, ok := p.GetProviderIcon().(string); ok {
		iconName = iconStr
	} else if data, ok := p.GetProviderIcon().([]byte); ok {
		iconBytes = data
	}

	title := p.Title()
	if title == "" {
		title = p.Name()
	}
	order, ok := providerPageOrder[p.ID()]
	if !ok {
		order = 100
	}

	item := &schema.NavItem{
		ID:          p.ID(),
		Title:       title,
		Description: content.Description,
		IconName:    iconName,
		IconBytes:   iconBytes,
		Order:       order,
		Panel:       content,
	}
	if p.Type() == provider.TypeMuseum {
		item.Badge = b.createMuseumBadgeFunc(p)
	} else {
		item.TitleFunc = b.createTitleFunc(p)
		item.Badge = b.createActiveBadgeFunc(p)
	}
	return item, isPending
}

func (b *PrefsPanelBuilder) buildProviderContent(p provider.ImageProvider, pendingURL string) *schema.PanelSchema {
	settingsPanel := p.CreateSettingsPanel(b.sm)
	queryPanel := p.CreateQueryPanel(b.sm, pendingURL)

	if settingsPanel != nil && queryPanel != nil {
		desc := settingsPanel.Description
		if desc == "" {
			desc = queryPanel.Description
		}
		return &schema.PanelSchema{
			Description: desc,
			Sections:    append(append([]schema.SectionSchema{}, settingsPanel.Sections...), queryPanel.Sections...),
		}
	} else if settingsPanel != nil {
		return settingsPanel
	} else if queryPanel != nil {
		return queryPanel
	}
	return nil
}

// activeQueryCount counts a provider's active queries (curated entries for museums), treating a
// staged toggle as already applied so counts update live while the user edits.
func (b *PrefsPanelBuilder) activeQueryCount(p provider.ImageProvider) (active, total int) {
	col := curation.GetManager().GetCollection(p.ID())
	if col != nil {
		// Curated museum provider: count active curated collection entries
		queries := b.plugin.cfg.GetQueries()
		for _, entry := range col.Entries {
			total++
			settingKey := p.ID() + "_" + entry.Key
			isActive := false
			for _, q := range queries {
				if q.Provider == p.ID() && q.URL == entry.Key {
					if q.Active {
						isActive = true
						break
					}
				}
			}
			if b.sm != nil && b.sm.HasPendingChange(settingKey) {
				isActive = !isActive
			}
			if isActive {
				active++
			}
		}
		return active, total
	}
	// General query list provider (Wallhaven, Pexels, etc.)
	for _, q := range b.plugin.cfg.GetQueries() {
		if q.Provider == p.ID() {
			total++
			isActive := q.Active
			if b.sm != nil && b.sm.HasPendingChange(q.ID) {
				isActive = !q.Active
			}
			if isActive {
				active++
			}
		}
	}
	return active, total
}

// createTitleFunc returns the page header title with today's active-count suffix.
func (b *PrefsPanelBuilder) createTitleFunc(p provider.ImageProvider) func() string {
	return func() string {
		title := p.Title()
		if title == "" {
			title = i18n.Tf("Image sources ({{.Name}})", map[string]any{"Name": p.Name()}) + "..."
		}
		activeCount, _ := b.activeQueryCount(p)
		if activeCount > 0 {
			if activeCount == 1 {
				return i18n.Tf("{{.Title}} (1 active)", map[string]any{"Title": title})
			}
			return i18n.Tf("{{.Title}} ({{.Count}} active)", map[string]any{"Title": title, "Count": activeCount})
		}
		return title
	}
}

// createActiveBadgeFunc returns the sidebar badge: the number of active queries ("" when none).
func (b *PrefsPanelBuilder) createActiveBadgeFunc(p provider.ImageProvider) func() string {
	return func() string {
		if active, _ := b.activeQueryCount(p); active > 0 {
			return strconv.Itoa(active)
		}
		return ""
	}
}

// createMuseumBadgeFunc returns a museum's "N of M on" collection summary.
func (b *PrefsPanelBuilder) createMuseumBadgeFunc(p provider.ImageProvider) func() string {
	return func() string {
		active, total := b.activeQueryCount(p)
		if total == 0 {
			return ""
		}
		return i18n.Tf("{{.On}} of {{.Total}} on", map[string]any{"On": active, "Total": total})
	}
}

// createMuseumsBadgeFunc returns the Museums page badge: active collections across all museums.
func (b *PrefsPanelBuilder) createMuseumsBadgeFunc(museums []provider.ImageProvider) func() string {
	return func() string {
		sum := 0
		for _, p := range museums {
			active, _ := b.activeQueryCount(p)
			sum += active
		}
		if sum == 0 {
			return ""
		}
		return strconv.Itoa(sum)
	}
}
