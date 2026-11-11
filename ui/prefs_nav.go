package ui

import (
	"bytes"
	"image/color"
	"runtime"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/dixieflatline76/Spice/v2/pkg/i18n"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
)

// Preferences window navigation (Fyne adapter for schema.NavSchema): grouped sidebar, page
// headers (icon tile or current-wallpaper banner), Museums drill-in and the floating save pill.

const (
	sidebarWidth      float32 = 210
	headerTileSize    float32 = 44
	sidebarTileSize   float32 = 26
	bannerHeight      float32 = 118
	pillBottomMargin  float32 = 18
	pageBottomForPill float32 = 88
)

// navStyleWindows selects the Windows sidebar selection style (neutral fill + accent bar) instead
// of the macOS/Linux filled-accent style. Tests may override it.
var navStyleWindows = runtime.GOOS == "windows"

// prefsNavView is the rendered navigation: sidebar, page stack and save pill.
type prefsNavView struct {
	sidebarBox fyne.CanvasObject // the sidebar column inside its scroll (sizing tests)
	sm      *SettingsManager
	nav     *schema.NavSchema
	root    fyne.CanvasObject
	content *fyne.Container

	pages     map[string]fyne.CanvasObject
	sidebar   map[string]*navItemWidget
	parentOf  map[string]string // child page -> parent page
	titleOf   map[string]string // page -> sidebar title
	pageOrder []string          // top-level page IDs in sidebar order
	current   string

	pill *savePill

	refreshers []func() // dynamic titles and badges
}

// newPrefsNavView renders every page up front (so the registry is complete for dirty tracking
// and the save pill) and selects initial (or the first page).
func newPrefsNavView(sm *SettingsManager, nav *schema.NavSchema, initial string) *prefsNavView {
	v := &prefsNavView{
		sm:       sm,
		nav:      nav,
		pages:    map[string]fyne.CanvasObject{},
		sidebar:  map[string]*navItemWidget{},
		parentOf: map[string]string{},
		titleOf:  map[string]string{},
	}

	sm.pagePadX = pagePadX
	sm.pageTopInset = 4
	sm.pageBottomInset = pageBottomForPill
	sm.pageMaxWidth = pageMaxWidth

	sidebarObjs := []fyne.CanvasObject{v.buildSearchField()}
	for _, g := range nav.Groups {
		if len(g.Items) == 0 {
			continue
		}
		sidebarObjs = append(sidebarObjs, newSidebarGroupLabel(g.Title))
		for i := range g.Items {
			item := g.Items[i]
			v.pageOrder = append(v.pageOrder, item.ID)
			v.titleOf[item.ID] = item.Title
			v.pages[item.ID] = v.renderPage(item, nil)
			for ci := range item.Children {
				child := item.Children[ci]
				v.parentOf[child.ID] = item.ID
				v.titleOf[child.ID] = child.Title
				v.pages[child.ID] = v.renderPage(child, &item)
			}
			navItem := newNavItemWidget(item.Title, sm.navIconResource(item.IconName, item.IconBytes), item.Badge, func() {
				v.Select(item.ID)
			})
			v.sidebar[item.ID] = navItem
			sidebarObjs = append(sidebarObjs, navItem)
		}
	}
	sm.currentPage = ""

	sidebarBox := container.New(&tightVBoxLayout{overlap: 0}, sidebarObjs...)
	sidebarScroll := container.NewVScroll(container.New(&paddedLayout{x: 10, y: 12}, sidebarBox))
	sidebarBG := newThemedRect(colorNameSidebar, "", 0)
	edge := newThemedRect(colorNameBorder, "", 0)
	edge.minSize = fyne.NewSize(1, 0)
	sidebar := container.New(&fixedWidthLayout{width: sidebarWidth},
		container.NewStack(sidebarBG, container.NewBorder(nil, nil, nil, edge, sidebarScroll)))

	v.content = container.NewStack()
	v.pill = newSavePill(sm)
	overlay := container.New(&pillOverlayLayout{}, v.pill.Container)
	v.pill.overlay = overlay
	main := container.NewStack(v.content, overlay)

	v.root = container.NewBorder(nil, nil, sidebar, nil, main)
	v.sidebarBox = sidebarBox

	sm.AddDirtyObserver(v.syncDirty)

	if _, ok := v.pages[initial]; !ok {
		initial = nav.FirstItemID()
	}
	v.Select(initial)
	v.syncDirty()
	return v
}

// Root returns the window content.
func (v *prefsNavView) Root() fyne.CanvasObject { return v.root }

// CurrentPage returns the selected page ID (a child ID while drilled into a museum).
func (v *prefsNavView) CurrentPage() string { return v.current }

// Select shows a page by ID (top-level or child).
func (v *prefsNavView) Select(id string) {
	page, ok := v.pages[id]
	if !ok {
		return
	}
	v.current = id
	v.content.Objects = []fyne.CanvasObject{page}
	v.content.Refresh()
	top := id
	if p, ok := v.parentOf[id]; ok {
		top = p
	}
	for pid, it := range v.sidebar {
		it.setSelected(pid == top)
	}
}

// syncDirty updates sidebar dots, dynamic titles/badges and the save pill.
func (v *prefsNavView) syncDirty() {
	names := v.sm.DirtyNames()
	dirtyPages := map[string]bool{}
	for _, n := range names {
		pid := v.sm.PageOf(n)
		if p, ok := v.parentOf[pid]; ok {
			pid = p
		}
		if pid != "" {
			dirtyPages[pid] = true
		}
	}
	for pid, it := range v.sidebar {
		it.setDirty(dirtyPages[pid])
		it.refreshBadge()
	}
	for _, fn := range v.refreshers {
		fn()
	}

	var titles []string
	for _, pid := range v.pageOrder {
		if dirtyPages[pid] {
			titles = append(titles, v.titleOf[pid])
		}
	}
	v.pill.update(len(names), titles)
}

// pendingSummary formats "N unsaved changes on <Page> and <Page> +k".
func pendingSummary(count int, pages []string) string {
	if count <= 0 {
		return ""
	}
	if len(pages) == 0 {
		if count == 1 {
			return i18n.T("1 unsaved change")
		}
		return i18n.Tf("{{.Count}} unsaved changes", map[string]any{"Count": count})
	}
	var where string
	switch len(pages) {
	case 1:
		where = pages[0]
	default:
		where = i18n.Tf("{{.First}} and {{.Second}}", map[string]any{"First": pages[0], "Second": pages[1]})
		if extra := len(pages) - 2; extra > 0 {
			where += " +" + strconv.Itoa(extra)
		}
	}
	if count == 1 {
		return i18n.Tf("1 unsaved change on {{.Pages}}", map[string]any{"Pages": where})
	}
	return i18n.Tf("{{.Count}} unsaved changes on {{.Pages}}", map[string]any{"Count": count, "Pages": where})
}

func (v *prefsNavView) buildSearchField() fyne.CanvasObject {
	// Placeholder until settings search lands (P6).
	search := widget.NewEntry()
	search.SetPlaceHolder(i18n.T("Search settings"))
	search.ActionItem = widget.NewIcon(theme.SearchIcon())
	return container.New(&paddedLayout{x: 0, y: 2}, search)
}

// --- Pages -------------------------------------------------------------------------------------

func (v *prefsNavView) renderPage(item schema.NavItem, parent *schema.NavItem) fyne.CanvasObject {
	v.sm.currentPage = item.ID

	var headerObjs []fyne.CanvasObject
	if parent != nil {
		parentID := parent.ID
		back := widget.NewButtonWithIcon(parent.Title, theme.NavigateBackIcon(), func() { v.Select(parentID) })
		back.Importance = widget.LowImportance
		headerObjs = append(headerObjs, container.NewHBox(back))
	}
	headerObjs = append(headerObjs, v.renderHeader(item))
	header := container.New(&pageColumnLayout{top: 20, bottom: 14, padX: pagePadX, gap: 6, maxWidth: pageMaxWidth}, headerObjs...)

	var body fyne.CanvasObject
	if item.Panel != nil && len(item.Panel.Sections) > 0 {
		body = v.sm.RenderSchema(*item.Panel)
	}
	if len(item.Children) > 0 {
		list := v.renderChildList(item)
		column := container.New(&pageColumnLayout{top: 4, bottom: pageBottomForPill, padX: pagePadX, gap: groupGap, maxWidth: pageMaxWidth}, list)
		if body == nil {
			body = container.NewVScroll(column)
		} else {
			body = container.NewBorder(nil, column, nil, nil, body)
		}
	}
	if body == nil {
		body = layout.NewSpacer()
	}
	return container.NewBorder(header, nil, nil, nil, body)
}

// renderHeader draws the icon-tile header, or the current-wallpaper banner when requested and
// available (data comes from the inner ring's BannerData snapshot only).
func (v *prefsNavView) renderHeader(item schema.NavItem) fyne.CanvasObject {
	if item.Header == schema.HeaderBanner && item.Banner != nil && item.Banner.ImagePath != "" {
		if b := newPageBanner(item.Title, item.Description, *item.Banner); b != nil {
			return b
		}
	}

	tileBG := newThemedRect(theme.ColorNamePrimary, "", 10)
	tileBG.minSize = fyne.NewSize(headerTileSize, headerTileSize)
	var tile fyne.CanvasObject = tileBG
	if res := v.sm.navIconResource(item.IconName, item.IconBytes); res != nil {
		if !isSVGResource(res) {
			// Bitmap provider logos keep their own colors on the soft tile color.
			tileBG.fill = colorNameIconTile
			tileBG.stroke = colorNameBorder
			tileBG.strokeWidth = 1
		}
		icon := widget.NewIcon(colorizeIcon(res, theme.ColorNameForegroundOnPrimary))
		tile = container.NewStack(tileBG, container.New(&paddedLayout{x: 10, y: 10}, icon))
	}
	tile = container.New(&fixedWidthLayout{width: headerTileSize}, tile)

	title := widget.NewLabel(item.Title)
	title.TextStyle = fyne.TextStyle{Bold: true}
	title.SizeName = theme.SizeNameHeadingText
	title.Truncation = fyne.TextTruncateEllipsis
	if item.TitleFunc != nil {
		fn := item.TitleFunc
		title.SetText(fn())
		v.refreshers = append(v.refreshers, func() {
			if t := fn(); t != title.Text {
				title.SetText(t)
			}
		})
	}
	text := []fyne.CanvasObject{title}
	if item.Description != "" {
		desc := widget.NewRichTextWithText(item.Description)
		desc.Wrapping = fyne.TextWrapWord
		if seg, ok := desc.Segments[0].(*widget.TextSegment); ok {
			seg.Style.ColorName = theme.ColorNamePlaceHolder
		}
		text = append(text, desc)
	}
	textBox := container.New(&tightVBoxLayout{overlap: rowInnerPad() + 2}, text...)
	return container.New(&headerLayout{}, container.NewCenter(tile), textBox)
}

// headerLayout: [tile, text] with the tile vertically centered against the text block.
type headerLayout struct{}

func (headerLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	t, x := objects[0].MinSize(), objects[1].MinSize()
	h := t.Height
	if x.Height > h {
		h = x.Height
	}
	return fyne.NewSize(t.Width+12+x.Width, h)
}

func (headerLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	t := objects[0].MinSize()
	objects[0].Resize(fyne.NewSize(t.Width, size.Height))
	objects[0].Move(fyne.NewPos(0, 0))
	xw := size.Width - t.Width - 12 + rowInnerPad()
	objects[1].Resize(fyne.NewSize(xw, objects[1].MinSize().Height))
	xh := objects[1].MinSize().Height
	objects[1].Move(fyne.NewPos(t.Width+12-rowInnerPad(), (size.Height-xh)/2))
}

// renderChildList renders the drill-in list (Museums): chevron rows with "N of M on" badges.
func (v *prefsNavView) renderChildList(item schema.NavItem) fyne.CanvasObject {
	var rows []fyne.CanvasObject
	for i := range item.Children {
		child := item.Children[i]
		badge := widget.NewLabel("")
		badge.SizeName = theme.SizeNameCaptionText
		badge.Importance = widget.LowImportance
		if child.Badge != nil {
			fn := child.Badge
			badge.SetText(fn())
			v.refreshers = append(v.refreshers, func() {
				if t := fn(); t != badge.Text {
					badge.SetText(t)
				}
			})
		}
		right := container.NewHBox(badge, newChevron())

		var left fyne.CanvasObject = newRowLabel(child.Title)
		if res := v.sm.navIconResource(child.IconName, child.IconBytes); res != nil {
			left = container.NewHBox(newIconTile(res, sidebarTileSize), left)
		}
		row := newInsetRowWithLeft(left, nil, right)
		id := child.ID
		rows = append(rows, newTapRow(row.Container, func() { v.Select(id) }))
	}
	return newCard(newDividedBox(rows...))
}

// newChevron returns the muted "›" drawn at the end of drill-in rows.
func newChevron() fyne.CanvasObject {
	l := widget.NewLabel("›")
	l.Importance = widget.LowImportance
	l.TextStyle = fyne.TextStyle{Bold: true}
	return l
}

// --- Banner ------------------------------------------------------------------------------------

// newPageBanner renders the current wallpaper (cropped to fill) with the page title, description
// and a "Now showing · provider" chip. Returns nil if the image cannot be loaded.
func newPageBanner(title, description string, data schema.BannerData) fyne.CanvasObject {
	img := canvas.NewImageFromFile(data.ImagePath)
	if img == nil {
		return nil
	}
	img.FillMode = canvas.ImageFillCover
	img.ScaleMode = canvas.ImageScaleSmooth
	img.CornerRadius = 10

	scrim := canvas.NewRectangle(color.NRGBA{A: 0x5C})
	scrim.CornerRadius = 10

	white := color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	t := canvas.NewText(title, white)
	t.TextStyle = fyne.TextStyle{Bold: true}
	t.TextSize = themeSize(theme.SizeNameHeadingText)
	d := canvas.NewText(description, color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xE6})
	d.TextSize = themeSize(theme.SizeNameText)

	var chip fyne.CanvasObject
	if data.ProviderName != "" {
		chipBG := canvas.NewRectangle(color.NRGBA{A: 0x73})
		chipText := canvas.NewText(i18n.Tf("Now showing · {{.Provider}}", map[string]any{"Provider": data.ProviderName}), white)
		chipText.TextSize = themeSize(theme.SizeNameCaptionText)
		chip = container.New(&chipLayout{bg: chipBG, text: chipText}, chipBG, chipText)
	}

	objs := []fyne.CanvasObject{img, scrim, t, d}
	if chip != nil {
		objs = append(objs, chip)
	}
	return container.New(&bannerLayout{title: t, desc: d, chip: chip}, objs...)
}

type bannerLayout struct {
	title, desc *canvas.Text
	chip        fyne.CanvasObject
}

func (b *bannerLayout) MinSize([]fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(200, bannerHeight)
}

func (b *bannerLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	objects[0].Resize(size)
	objects[0].Move(fyne.NewPos(0, 0))
	objects[1].Resize(size)
	objects[1].Move(fyne.NewPos(0, 0))
	const pad = 18
	dh := b.desc.MinSize().Height
	th := b.title.MinSize().Height
	b.desc.Move(fyne.NewPos(pad, size.Height-pad-dh))
	b.desc.Resize(b.desc.MinSize())
	b.title.Move(fyne.NewPos(pad, size.Height-pad-dh-th+2))
	b.title.Resize(b.title.MinSize())
	if b.chip != nil {
		cs := b.chip.MinSize()
		b.chip.Resize(cs)
		b.chip.Move(fyne.NewPos(size.Width-14-cs.Width, size.Height-14-cs.Height))
	}
}

type chipLayout struct {
	bg   *canvas.Rectangle
	text *canvas.Text
}

func (c *chipLayout) MinSize([]fyne.CanvasObject) fyne.Size {
	ts := c.text.MinSize()
	return fyne.NewSize(ts.Width+20, ts.Height+8)
}

func (c *chipLayout) Layout(_ []fyne.CanvasObject, size fyne.Size) {
	c.bg.Resize(size)
	c.bg.CornerRadius = size.Height / 2
	ts := c.text.MinSize()
	c.text.Resize(ts)
	c.text.Move(fyne.NewPos((size.Width-ts.Width)/2, (size.Height-ts.Height)/2))
}

// --- Icons -------------------------------------------------------------------------------------

// navIconResource resolves a page icon from raw bytes or a name.
func (sm *SettingsManager) navIconResource(name string, data []byte) fyne.Resource {
	if len(data) > 0 {
		return fyne.NewStaticResource(name, data)
	}
	switch name {
	case "image":
		return theme.MediaPhotoIcon()
	case "museum":
		return theme.FileImageIcon()
	case "keyboard":
		return theme.MediaSkipNextIcon()
	case "advanced":
		return theme.MoreHorizontalIcon()
	}
	return sm.resolveIcon(name)
}

// colorizeIcon recolors SVG icons with a theme color; bitmap provider icons are kept as-is.
func colorizeIcon(res fyne.Resource, name fyne.ThemeColorName) fyne.Resource {
	if res == nil {
		return nil
	}
	if !isSVGResource(res) {
		return res
	}
	return theme.NewColoredResource(res, name)
}

func isSVGResource(res fyne.Resource) bool {
	return res != nil && bytes.Contains(res.Content(), []byte("<svg"))
}

// newIconTile returns a rounded tile (tile color) with an accent-colored icon.
func newIconTile(res fyne.Resource, size float32) fyne.CanvasObject {
	bg := newThemedRect(colorNameIconTile, "", 6)
	bg.minSize = fyne.NewSize(size, size)
	icon := widget.NewIcon(colorizeIcon(res, theme.ColorNamePrimary))
	return container.New(&fixedWidthLayout{width: size}, container.NewStack(bg, container.New(&paddedLayout{x: 4, y: 4}, icon)))
}

// newSidebarGroupLabel is the accent group title in the sidebar (General / Sources / Library).
func newSidebarGroupLabel(text string) fyne.CanvasObject {
	l := newGroupLabel(text)
	return container.New(&paddedLayout{x: 0, y: 0}, container.New(layout.NewCustomPaddedLayout(6, 0, 0, 0), l))
}

// --- Tappable rows -----------------------------------------------------------------------------

// tapRow makes a row tappable with a hover highlight (chevron/drill-in rows).
type tapRow struct {
	widget.BaseWidget
	content fyne.CanvasObject
	hover   *themedRect
	onTap   func()
}

func newTapRow(content fyne.CanvasObject, onTap func()) *tapRow {
	r := &tapRow{content: content, onTap: onTap, hover: newThemedRect("", "", 0)}
	r.ExtendBaseWidget(r)
	return r
}

func (r *tapRow) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.NewStack(r.hover, r.content))
}

func (r *tapRow) Tapped(*fyne.PointEvent) {
	if r.onTap != nil {
		r.onTap()
	}
}

func (r *tapRow) Cursor() desktop.Cursor { return desktop.PointerCursor }

func (r *tapRow) MouseIn(*desktop.MouseEvent) { r.hover.setFill(theme.ColorNameHover) }

func (r *tapRow) MouseMoved(*desktop.MouseEvent) {}

func (r *tapRow) MouseOut() { r.hover.setFill("") }

// --- Sidebar item ------------------------------------------------------------------------------

// navItemWidget is one sidebar entry: icon tile, title, optional badge and the unsaved-changes dot.
// Selected style: filled accent with white text/icon on macOS/Linux; neutral fill plus a 3px accent
// bar on the left edge on Windows.
type navItemWidget struct {
	widget.BaseWidget
	title     string
	icon      fyne.Resource
	badgeFunc func() string
	badge     string
	onTap     func()
	selected  bool
	dirty     bool
	hovered   bool
}

func newNavItemWidget(title string, icon fyne.Resource, badge func() string, onTap func()) *navItemWidget {
	n := &navItemWidget{title: title, icon: icon, badgeFunc: badge, onTap: onTap}
	if badge != nil {
		n.badge = badge()
	}
	n.ExtendBaseWidget(n)
	return n
}

func (n *navItemWidget) setSelected(b bool) {
	if n.selected != b {
		n.selected = b
		n.Refresh()
	}
}

func (n *navItemWidget) setDirty(b bool) {
	if n.dirty != b {
		n.dirty = b
		n.Refresh()
	}
}

func (n *navItemWidget) refreshBadge() {
	if n.badgeFunc == nil {
		return
	}
	if b := n.badgeFunc(); b != n.badge {
		n.badge = b
		n.Refresh()
	}
}

func (n *navItemWidget) Tapped(*fyne.PointEvent) {
	if n.onTap != nil {
		n.onTap()
	}
}

func (n *navItemWidget) Cursor() desktop.Cursor { return desktop.PointerCursor }

func (n *navItemWidget) MouseIn(*desktop.MouseEvent) { n.hovered = true; n.Refresh() }

func (n *navItemWidget) MouseMoved(*desktop.MouseEvent) {}

func (n *navItemWidget) MouseOut() { n.hovered = false; n.Refresh() }

func (n *navItemWidget) CreateRenderer() fyne.WidgetRenderer {
	r := &navItemRenderer{
		n:      n,
		bg:     canvas.NewRectangle(color.Transparent),
		bar:    canvas.NewRectangle(color.Transparent),
		tile:   canvas.NewRectangle(color.Transparent),
		icon:   canvas.NewImageFromResource(nil),
		label:  canvas.NewText(n.title, color.Black),
		badge:  canvas.NewText("", color.Black),
		dot:    canvas.NewCircle(color.Transparent),
		height: 31,
	}
	r.icon.FillMode = canvas.ImageFillContain
	r.Refresh()
	return r
}

type navItemRenderer struct {
	n      *navItemWidget
	bg     *canvas.Rectangle
	bar    *canvas.Rectangle
	tile   *canvas.Rectangle
	icon   *canvas.Image
	label  *canvas.Text
	badge  *canvas.Text
	dot    *canvas.Circle
	height float32
}

func (r *navItemRenderer) MinSize() fyne.Size {
	return fyne.NewSize(sidebarWidth-24, r.height)
}

func (r *navItemRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	r.bg.CornerRadius = 7
	r.bar.Resize(fyne.NewSize(3, size.Height-14))
	r.bar.Move(fyne.NewPos(0, 7))
	r.bar.CornerRadius = 1.5

	x := float32(6)
	ty := (size.Height - sidebarTileSize) / 2
	r.tile.Move(fyne.NewPos(x, ty))
	r.tile.Resize(fyne.NewSize(sidebarTileSize, sidebarTileSize))
	r.tile.CornerRadius = 6
	r.icon.Move(fyne.NewPos(x+5, ty+5))
	r.icon.Resize(fyne.NewSize(sidebarTileSize-10, sidebarTileSize-10))

	right := size.Width - 8
	if r.n.dirty {
		r.dot.Resize(fyne.NewSize(rowDotSize, rowDotSize))
		r.dot.Move(fyne.NewPos(right-rowDotSize, (size.Height-rowDotSize)/2))
		right -= rowDotSize + 6
	}
	bs := r.badge.MinSize()
	if r.badge.Text != "" {
		r.badge.Resize(bs)
		r.badge.Move(fyne.NewPos(right-bs.Width, (size.Height-bs.Height)/2))
		right -= bs.Width + 6
	}
	lx := x + sidebarTileSize + 10
	ls := r.label.MinSize()
	r.label.Move(fyne.NewPos(lx, (size.Height-ls.Height)/2))
	r.label.Resize(fyne.NewSize(right-lx, ls.Height))
}

func (r *navItemRenderer) Refresh() {
	n := r.n
	fg := themeColor(theme.ColorNameForeground)
	accent := themeColor(theme.ColorNamePrimary)
	onAccent := themeColor(theme.ColorNameForegroundOnPrimary)
	muted := themeColor(theme.ColorNamePlaceHolder)
	tile := themeColor(colorNameIconTile)
	iconColor := theme.ColorNamePrimary

	r.bar.FillColor = color.Transparent
	switch {
	case n.selected && !navStyleWindows:
		r.bg.FillColor = accent
		r.label.Color = onAccent
		r.badge.Color = fadeColor(onAccent, 0xCC)
		r.tile.FillColor = fadeColor(onAccent, 0x33)
		iconColor = theme.ColorNameForegroundOnPrimary
		r.dot.FillColor = onAccent
	case n.selected && navStyleWindows:
		r.bg.FillColor = themeColor(colorNameWinSelection)
		r.bar.FillColor = accent
		r.label.Color = fg
		r.badge.Color = muted
		r.tile.FillColor = tile
		r.dot.FillColor = accent
	case n.hovered:
		r.bg.FillColor = themeColor(theme.ColorNameHover)
		r.label.Color = fg
		r.badge.Color = muted
		r.tile.FillColor = tile
		r.dot.FillColor = accent
	default:
		r.bg.FillColor = color.Transparent
		r.label.Color = fg
		r.badge.Color = muted
		r.tile.FillColor = tile
		r.dot.FillColor = accent
	}
	r.label.Text = n.title
	r.label.TextSize = themeSize(theme.SizeNameText)
	r.label.TextStyle = fyne.TextStyle{Bold: n.selected && !navStyleWindows}
	r.badge.Text = n.badge
	r.badge.TextSize = themeSize(theme.SizeNameCaptionText)
	r.icon.Resource = colorizeIcon(n.icon, iconColor)
	if n.dirty {
		r.dot.Show()
	} else {
		r.dot.Hide()
	}
	r.Layout(n.Size())
	for _, o := range r.Objects() {
		canvas.Refresh(o)
	}
}

func (r *navItemRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.bar, r.tile, r.icon, r.label, r.badge, r.dot}
}

func (r *navItemRenderer) Destroy() {}

// --- Save pill ---------------------------------------------------------------------------------

// savePill floats centered at the bottom of the content area while anything is staged:
// "N unsaved changes on <Page>…", a ghost Revert button and the accent Save changes button
// (the SettingsManager's existing apply button, so Save runs the unchanged apply path).
type savePill struct {
	*fyne.Container
	overlay *fyne.Container // parent overlay, re-laid out when the pill's text changes
	label   *widget.Label
	revert  *widget.Button
	save    *widget.Button
}

func newSavePill(sm *SettingsManager) *savePill {
	label := widget.NewLabel("")
	label.TextStyle = fyne.TextStyle{Bold: true}
	revert := widget.NewButton(i18n.T("Revert"), func() { sm.RevertPending() })
	revert.Importance = widget.LowImportance
	save := sm.applyButton
	save.Importance = widget.HighImportance

	dot := newDot()
	row := container.NewHBox(container.NewCenter(dot), label, revert, save)
	bg := newThemedRect(colorNameCard, colorNameStrongBorder, -1)
	p := &savePill{
		Container: container.NewStack(bg, container.New(&paddedLayout{x: 14, y: 6}, row)),
		label:     label,
		revert:    revert,
		save:      save,
	}
	p.Hide()
	return p
}

func (p *savePill) update(count int, pages []string) {
	if count == 0 {
		p.Hide()
		return
	}
	text := pendingSummary(count, pages)
	if p.label.Text != text {
		p.label.SetText(text)
	}
	p.Show()
	if p.overlay != nil {
		p.overlay.Refresh()
	} else {
		p.Refresh()
	}
}

// pillOverlayLayout centers the pill at the bottom of the content area.
type pillOverlayLayout struct{}

func (pillOverlayLayout) MinSize([]fyne.CanvasObject) fyne.Size { return fyne.NewSize(0, 0) }

func (pillOverlayLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		ms := o.MinSize()
		w := ms.Width
		if max := size.Width - 32; w > max {
			w = max
		}
		o.Resize(fyne.NewSize(w, ms.Height))
		o.Move(fyne.NewPos((size.Width-w)/2, size.Height-pillBottomMargin-ms.Height))
	}
}
