package schema

import "sort"

// Well-known preferences page and group IDs. Plugins contribute pages to these IDs and the
// adapter merges contributions that share an ID (see MergeNav).
const (
	NavGroupGeneral = "general"
	NavGroupSources = "sources"
	NavGroupLibrary = "library"

	NavPageApp       = "app"
	NavPageShortcuts = "shortcuts"
	NavPageWallpaper = "wallpaper"
	NavPageSmartFit  = "smartfit"
	NavPageFraming   = "framing"
	NavPagePause     = "pause"
	NavPageDisplays  = "displays"
	NavPageAdvanced  = "advanced"
	NavPageMuseums   = "museums"
)

// HeaderKind selects how a page header is drawn.
type HeaderKind string

const (
	// HeaderIcon is the default header: accent icon tile, title and one-line description.
	HeaderIcon HeaderKind = ""
	// HeaderBanner draws a wide image banner (falls back to HeaderIcon when Banner is nil or empty).
	HeaderBanner HeaderKind = "banner"
)

// BannerData is a snapshot of what a banner header shows. The inner ring fills it under its own
// locks when it builds the schema; the UI adapter only reads it (one-way port data flow).
type BannerData struct {
	ImagePath    string // Local file path of the image to show (cropped to fill)
	ProviderName string // Display name of the image's source provider
}

// NavSchema is the declarative definition of the preferences window navigation: a grouped
// sidebar of pages. It replaces the old outer tabs + inner tabs/accordions.
type NavSchema struct {
	Groups []NavGroup
	// Selected is the page ID to show first (e.g. a provider page the tray or browser extension
	// asked to focus). Empty means "keep the caller's default".
	Selected string
}

// NavGroup is one titled block of sidebar items (General / Sources / Library).
type NavGroup struct {
	ID    string
	Title string
	Order int // Lower sorts first
	Items []NavItem
}

// NavItem is one sidebar page. Items with Children render a drill-in list of child pages.
type NavItem struct {
	ID          string
	Title       string
	TitleFunc   func() string // Optional dynamic header title (e.g. "wallhaven (2 active)")
	Description string
	IconName    string
	IconBytes   []byte
	Order       int           // Lower sorts first within the group
	Badge       func() string // Optional short badge (e.g. active count, "3 of 9 on")
	Panel       *PanelSchema
	Children    []NavItem
	Header      HeaderKind
	Banner      *BannerData
}

// MergeNav merges several navigation contributions into one. Groups merge by ID and items merge
// by ID within their group: the first non-empty Title/Description/Icon/Badge wins, panel sections
// and children are appended in contribution order. Groups and items are then sorted by Order
// (stable, so equal orders keep contribution order). The last non-empty Selected wins.
func MergeNav(navs ...*NavSchema) *NavSchema {
	out := &NavSchema{}
	groupIdx := map[string]int{}
	itemIdx := map[string]map[string]int{}

	for _, n := range navs {
		if n == nil {
			continue
		}
		if n.Selected != "" {
			out.Selected = n.Selected
		}
		for _, g := range n.Groups {
			gi, ok := groupIdx[g.ID]
			if !ok {
				gi = len(out.Groups)
				groupIdx[g.ID] = gi
				itemIdx[g.ID] = map[string]int{}
				out.Groups = append(out.Groups, NavGroup{ID: g.ID, Title: g.Title, Order: g.Order})
			}
			dst := &out.Groups[gi]
			if dst.Title == "" {
				dst.Title = g.Title
			}
			for _, it := range g.Items {
				ii, ok := itemIdx[g.ID][it.ID]
				if !ok {
					itemIdx[g.ID][it.ID] = len(dst.Items)
					cp := it
					if it.Panel != nil {
						p := *it.Panel
						p.Sections = append([]SectionSchema{}, it.Panel.Sections...)
						cp.Panel = &p
					}
					cp.Children = append([]NavItem{}, it.Children...)
					dst.Items = append(dst.Items, cp)
					continue
				}
				mergeNavItem(&dst.Items[ii], it)
			}
		}
	}

	sort.SliceStable(out.Groups, func(i, j int) bool { return out.Groups[i].Order < out.Groups[j].Order })
	for gi := range out.Groups {
		items := out.Groups[gi].Items
		sort.SliceStable(items, func(i, j int) bool { return items[i].Order < items[j].Order })
	}
	return out
}

func mergeNavItem(dst *NavItem, src NavItem) {
	if dst.Title == "" {
		dst.Title = src.Title
	}
	if dst.TitleFunc == nil {
		dst.TitleFunc = src.TitleFunc
	}
	if dst.Description == "" {
		dst.Description = src.Description
	}
	if dst.IconName == "" && len(dst.IconBytes) == 0 {
		dst.IconName = src.IconName
		dst.IconBytes = src.IconBytes
	}
	if dst.Badge == nil {
		dst.Badge = src.Badge
	}
	if dst.Header == HeaderIcon {
		dst.Header = src.Header
	}
	if dst.Banner == nil {
		dst.Banner = src.Banner
	}
	if src.Panel != nil {
		if dst.Panel == nil {
			p := *src.Panel
			p.Sections = append([]SectionSchema{}, src.Panel.Sections...)
			dst.Panel = &p
		} else {
			dst.Panel.Sections = append(dst.Panel.Sections, src.Panel.Sections...)
			if dst.Panel.Description == "" {
				dst.Panel.Description = src.Panel.Description
			}
		}
	}
	dst.Children = append(dst.Children, src.Children...)
}

// FindItem returns the page with the given ID (searching children too) and its parent, if any.
func (n *NavSchema) FindItem(id string) (item *NavItem, parent *NavItem) {
	if n == nil || id == "" {
		return nil, nil
	}
	for gi := range n.Groups {
		for ii := range n.Groups[gi].Items {
			it := &n.Groups[gi].Items[ii]
			if it.ID == id {
				return it, nil
			}
			for ci := range it.Children {
				if it.Children[ci].ID == id {
					return &it.Children[ci], it
				}
			}
		}
	}
	return nil, nil
}

// FirstItemID returns the ID of the first page in the first group, or "" if there are none.
func (n *NavSchema) FirstItemID() string {
	if n == nil {
		return ""
	}
	for _, g := range n.Groups {
		if len(g.Items) > 0 {
			return g.Items[0].ID
		}
	}
	return ""
}
