package ui

import (
	"image/color"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/dixieflatline76/Spice/v2/pkg/i18n"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
)

// Settings search (Fyne adapter): the sidebar search field swaps the page area for a results
// list built from the pure-Go index in pkg/ui/schema. Clicking a result opens its page, scrolls
// the matching row into view and flashes it; clearing the field restores the page that was
// showing. The index and ranking live in the schema package; nothing here touches domain state.

const (
	searchTrailSep       = " › "
	searchRevealMargin   = 56 // space kept above a revealed row, so its section heading stays in view
	searchFlashPeak      = 0x4D
	searchFlashDuration  = 1500 * time.Millisecond
	searchFlashHoldUntil = 0.3 // fraction of the flash spent at full tint before it fades
)

// Timings are variables so tests can run search synchronously.
var (
	searchDebounce    = 120 * time.Millisecond // typing pause before results update
	searchRevealDelay = 60 * time.Millisecond  // lets a freshly shown page lay out before scrolling
)

// searchEntry is the sidebar search field: Escape clears it.
type searchEntry struct {
	widget.Entry
	onEscape func()
}

func newSearchEntry() *searchEntry {
	e := &searchEntry{}
	e.ExtendBaseWidget(e)
	return e
}

func (e *searchEntry) TypedKey(ev *fyne.KeyEvent) {
	if ev.Name == fyne.KeyEscape {
		if e.onEscape != nil {
			e.onEscape()
		}
		return
	}
	e.Entry.TypedKey(ev)
}

// prefsSearch owns the search field and the results page of one preferences window.
type prefsSearch struct {
	v      *prefsNavView
	entry  *searchEntry
	action *widget.Button
	index  []schema.SearchEntry

	gen     int // bumped on every change so a late debounce timer can tell it is stale
	timer   *time.Timer
	showing bool // the results page is in v.content
	quiet   bool // the field is being cleared programmatically: ignore OnChanged
	results []schema.SearchResult
}

func newPrefsSearch(v *prefsNavView) *prefsSearch {
	s := &prefsSearch{v: v, entry: newSearchEntry()}
	s.entry.SetPlaceHolder(i18n.T("Search settings"))
	s.action = widget.NewButtonWithIcon("", theme.SearchIcon(), s.onAction)
	s.action.Importance = widget.LowImportance
	s.entry.ActionItem = s.action
	s.entry.OnChanged = s.onChanged
	s.entry.OnSubmitted = func(string) { s.openFirst() }
	s.entry.onEscape = s.clear
	return s
}

// onAction is the field's trailing button: a clear button while there is text.
func (s *prefsSearch) onAction() {
	if s.entry.Text != "" {
		s.clear()
	}
}

func (s *prefsSearch) syncAction() {
	icon := theme.SearchIcon()
	if s.entry.Text != "" {
		icon = theme.CancelIcon()
	}
	s.action.SetIcon(icon)
}

func (s *prefsSearch) stopTimer() {
	s.gen++
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

func (s *prefsSearch) onChanged(text string) {
	if s.quiet {
		return
	}
	s.syncAction()
	s.stopTimer()
	q := strings.TrimSpace(text)
	if q == "" {
		s.hide()
		return
	}
	if searchDebounce <= 0 {
		s.show(q)
		return
	}
	gen := s.gen
	s.timer = time.AfterFunc(searchDebounce, func() {
		fyne.Do(func() {
			if s.gen == gen {
				s.show(q)
			}
		})
	})
}

// clear empties the field and brings back the page that was showing.
func (s *prefsSearch) clear() {
	if s.entry.Text == "" && !s.showing {
		return
	}
	s.entry.SetText("") // OnChanged restores the page
	if s.showing {
		s.hide()
	}
}

// reset returns to the normal state without a fuss (a page was chosen some other way).
func (s *prefsSearch) reset() {
	s.stopTimer()
	if s.entry.Text != "" {
		s.quiet = true
		s.entry.SetText("")
		s.quiet = false
		s.syncAction()
	}
	s.hide()
}

// hide swaps the results out for the selected page.
func (s *prefsSearch) hide() {
	if !s.showing {
		return
	}
	s.showing = false
	s.results = nil
	if page, ok := s.v.pages[s.v.current]; ok {
		s.v.content.Objects = []fyne.CanvasObject{page}
		s.v.content.Refresh()
	}
}

// show runs the query and puts the results page in the content area. The sidebar keeps showing
// the page that was selected before searching.
func (s *prefsSearch) show(q string) {
	s.results = s.run(q)
	s.showing = true
	s.v.content.Objects = []fyne.CanvasObject{s.buildPage(q, s.results)}
	s.v.content.Refresh()
}

// openFirst is Enter in the field: open the best match.
func (s *prefsSearch) openFirst() {
	q := strings.TrimSpace(s.entry.Text)
	if q == "" {
		return
	}
	if res := s.run(q); len(res) > 0 {
		s.open(res[0])
	}
}

// run searches the index and drops settings whose row is currently hidden (an OS-specific row,
// or one whose VisibleIf is false), so a result always leads to something on screen.
func (s *prefsSearch) run(q string) []schema.SearchResult {
	res := schema.Search(s.index, q)
	out := res[:0]
	for _, r := range res {
		if r.SettingName != "" {
			if row := s.v.sm.rowOf[r.SettingName]; row != nil && !row.Visible() {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// open leaves search mode and goes to a result: its page, then its row.
func (s *prefsSearch) open(r schema.SearchResult) {
	s.reset()
	if _, ok := s.v.pages[r.PageID]; !ok {
		return
	}
	s.v.Select(r.PageID)
	if r.SettingName != "" {
		s.v.revealSetting(r.PageID, r.SettingName)
	}
}

// --- Results page ------------------------------------------------------------------------------

func (s *prefsSearch) buildPage(q string, res []schema.SearchResult) fyne.CanvasObject {
	var desc string
	switch len(res) {
	case 0:
	case 1:
		desc = i18n.Tf("1 result for “{{.Query}}”", map[string]any{"Query": q})
	default:
		desc = i18n.Tf("{{.Count}} results for “{{.Query}}”", map[string]any{"Count": len(res), "Query": q})
	}
	head := s.v.renderHeader(schema.NavItem{
		Title:       i18n.T("Search"),
		Description: desc,
		IconName:    "search",
		IconBytes:   theme.SearchIcon().Content(),
	})
	header := container.New(&pageColumnLayout{top: 20, bottom: 14, padX: pagePadX, gap: 6, maxWidth: pageMaxWidth}, head)

	var body fyne.CanvasObject
	if len(res) == 0 {
		body = newSearchEmptyState(q)
	} else {
		rows := make([]fyne.CanvasObject, 0, len(res))
		for _, r := range res {
			rows = append(rows, s.newResultRow(r))
		}
		body = newCard(newDividedBox(rows...))
	}
	column := container.New(&pageColumnLayout{top: 4, bottom: pageBottomForPill, padX: pagePadX, gap: groupGap, maxWidth: pageMaxWidth}, body)
	return container.NewBorder(header, nil, nil, nil, container.NewVScroll(column))
}

// searchTrail is the muted line under a result: "Wallpaper › Rotation" for a setting, the parent
// pages for a nested page, or a page's own description when it has no parents.
func searchTrail(r schema.SearchResult) string {
	trail := r.PageTrail
	if r.IsPage {
		if len(trail) > 1 {
			return strings.Join(trail[:len(trail)-1], searchTrailSep)
		}
		return r.Help
	}
	parts := append([]string{}, trail...)
	if r.SectionTitle != "" {
		parts = append(parts, cleanLabel(r.SectionTitle))
	}
	return strings.Join(parts, searchTrailSep)
}

func (s *prefsSearch) newResultRow(r schema.SearchResult) fyne.CanvasObject {
	lbl := newRowLabel(r.Label)
	objs := []fyne.CanvasObject{lbl}
	if trail := searchTrail(r); trail != "" {
		objs = append(objs, newHelpText(trail))
	}
	left := container.New(&tightVBoxLayout{overlap: rowInnerPad() + 1}, objs...)
	row := newInsetRowWithLeft(left, lbl, newChevron())
	return newTapRow(row.Container, func() { s.open(r) })
}

// newSearchEmptyState is the "nothing found" block: a bold line and a muted hint.
func newSearchEmptyState(q string) fyne.CanvasObject {
	icon := widget.NewIcon(colorizeIcon(theme.SearchIcon(), theme.ColorNamePlaceHolder))
	title := widget.NewLabel(i18n.Tf("No settings match “{{.Query}}”", map[string]any{"Query": q}))
	title.TextStyle = fyne.TextStyle{Bold: true}
	title.Alignment = fyne.TextAlignCenter
	title.Wrapping = fyne.TextWrapWord
	hint := widget.NewRichTextWithText(i18n.T("Try a different word, like “cache” or “faces”."))
	hint.Wrapping = fyne.TextWrapWord
	if seg, ok := hint.Segments[0].(*widget.TextSegment); ok {
		seg.Style.ColorName = theme.ColorNamePlaceHolder
		seg.Style.Alignment = fyne.TextAlignCenter
	}
	iconBox := container.New(&fixedWidthLayout{width: 36}, container.New(&paddedLayout{x: 2, y: 2}, icon))
	block := container.New(&tightVBoxLayout{overlap: 0}, container.NewCenter(iconBox), title, hint)
	return container.New(&paddedLayout{x: 0, y: 36}, block)
}

// --- Revealing a result ------------------------------------------------------------------------

// revealSetting scrolls the page's row for a setting into view and flashes it. It runs a moment
// after the page is shown so the page has been laid out.
func (v *prefsNavView) revealSetting(pageID, name string) {
	row := v.sm.rowOf[name]
	if row == nil {
		return
	}
	reveal := func() {
		if v.current != pageID || v.search.showing {
			return // the user moved on
		}
		v.scrollToRow(pageID, row)
		flashRow(row)
	}
	if searchRevealDelay <= 0 {
		reveal()
		return
	}
	time.AfterFunc(searchRevealDelay, func() { fyne.Do(reveal) })
}

// scrollToRow scrolls the page's scroll container so the row sits near the top.
func (v *prefsNavView) scrollToRow(pageID string, row fyne.CanvasObject) {
	page, ok := v.pages[pageID]
	if !ok || !row.Visible() {
		return
	}
	sc, pos, ok := locateInScroll(page, row)
	if !ok || sc == nil {
		return
	}
	y := pos.Y - searchRevealMargin
	if maxY := sc.Content.MinSize().Height - sc.Size().Height; y > maxY {
		y = maxY
	}
	if y < 0 {
		y = 0
	}
	sc.ScrollToOffset(fyne.NewPos(0, y))
}

// locateInScroll finds target under root and returns its innermost scroll container and its
// position within that scroll's content.
func locateInScroll(root, target fyne.CanvasObject) (*container.Scroll, fyne.Position, bool) {
	var (
		found *container.Scroll
		pos   fyne.Position
	)
	var walk func(o fyne.CanvasObject, cur *container.Scroll, acc fyne.Position) bool
	walk = func(o fyne.CanvasObject, cur *container.Scroll, acc fyne.Position) bool {
		switch n := o.(type) {
		case *container.Scroll:
			return walk(n.Content, n, fyne.Position{}) // content coordinates restart at the scroll
		case *fyne.Container:
			for _, c := range n.Objects {
				p := acc.Add(c.Position())
				if c == target {
					found, pos = cur, p
					return true
				}
				if walk(c, cur, p) {
					return true
				}
			}
		}
		return false
	}
	ok := walk(root, nil, fyne.Position{})
	return found, pos, ok
}

// flashRow briefly tints an inset row with the accent color, then fades it out.
func flashRow(row fyne.CanvasObject) {
	c, ok := row.(*fyne.Container)
	if !ok || len(c.Objects) <= rowObjDot {
		return
	}
	if _, isInset := c.Layout.(*insetRowLayout); !isInset {
		return
	}
	rect, ok := c.Objects[rowObjFlash].(*canvas.Rectangle)
	if !ok {
		return
	}
	base := themeColor(theme.ColorNamePrimary)
	set := func(a uint8) {
		if a == 0 {
			rect.FillColor = color.Transparent
		} else {
			rect.FillColor = fadeColor(base, a)
		}
		rect.Refresh()
	}
	set(searchFlashPeak)
	anim := fyne.NewAnimation(searchFlashDuration, func(p float32) { set(flashAlpha(p)) })
	anim.Curve = fyne.AnimationLinear
	anim.Start()
}

// flashAlpha is the tint strength at animation progress p (0..1): full for the first part, then
// a linear fade to nothing.
func flashAlpha(p float32) uint8 {
	switch {
	case p >= 1:
		return 0
	case p <= searchFlashHoldUntil:
		return searchFlashPeak
	}
	return uint8(float32(searchFlashPeak) * (1 - (p-searchFlashHoldUntil)/(1-searchFlashHoldUntil)))
}
