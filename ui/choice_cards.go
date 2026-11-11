package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/dixieflatline76/Spice/v2/pkg/i18n"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
)

// Choice cards: the adapter for schema.ChoiceCardsItem. A row of side-by-side cards, one
// selected (accent ring plus a "Selected" pill). It takes part in the SettingsManager registry
// exactly like a SelectItem: the selected index is staged, never written to Config until the
// item's ApplyFunc runs from "Save changes".

const (
	cardPad           float32 = 12
	cardGap           float32 = 10
	cardMinWidth      float32 = 150
	cardIllustrationH float32 = 64
	cardRingWidth     float32 = 2
)

// --- Illustration ---------------------------------------------------------------------------

// ultrawideAspect is the display shape the illustrations crop to (3440x1440).
const ultrawideAspect = 3440.0 / 1440.0

// cropIllustration draws a gray "photo" and, for the fit modes, the orange rectangle that the
// display would show of it. Off draws the photo alone; Flexibility uses a taller photo, so its
// crop trims visibly more than Quality's.
type cropIllustration struct {
	widget.BaseWidget
	kind string
}

func newCropIllustration(kind string) *cropIllustration {
	c := &cropIllustration{kind: kind}
	c.ExtendBaseWidget(c)
	return c
}

func (c *cropIllustration) CreateRenderer() fyne.WidgetRenderer {
	r := &cropIllustrationRenderer{
		c:     c,
		photo: canvas.NewRectangle(color.Transparent),
		crop:  canvas.NewRectangle(color.Transparent),
	}
	r.Refresh()
	return r
}

type cropIllustrationRenderer struct {
	c     *cropIllustration
	photo *canvas.Rectangle // the whole image
	crop  *canvas.Rectangle // what the display shows
}

func (r *cropIllustrationRenderer) MinSize() fyne.Size {
	return fyne.NewSize(60, cardIllustrationH)
}

// photoAspect is the photo's width / height for the illustration kind.
func (r *cropIllustrationRenderer) photoAspect() float32 {
	switch r.c.kind {
	case schema.IllustrationQuality:
		return 1.6 // 16:10
	case schema.IllustrationFlexibility:
		return 4.0 / 3.0
	}
	return 1.5 // 3:2, plain image
}

func (r *cropIllustrationRenderer) Layout(size fyne.Size) {
	h := size.Height
	w := h * r.photoAspect()
	if w > size.Width {
		w = size.Width
		h = w / r.photoAspect()
	}
	x, y := (size.Width-w)/2, (size.Height-h)/2
	r.photo.Move(fyne.NewPos(x, y))
	r.photo.Resize(fyne.NewSize(w, h))
	r.photo.CornerRadius = 4

	if r.c.kind == schema.IllustrationNone {
		r.crop.Resize(fyne.NewSize(0, 0))
		return
	}
	ch := w / ultrawideAspect
	r.crop.Move(fyne.NewPos(x, y+(h-ch)/2))
	r.crop.Resize(fyne.NewSize(w, ch))
	r.crop.CornerRadius = 2
}

func (r *cropIllustrationRenderer) Refresh() {
	r.photo.FillColor = themeColor(colorNameSwitchTrack)
	r.photo.StrokeColor = themeColor(colorNameStrongBorder)
	r.photo.StrokeWidth = 1
	accent := color.NRGBAModel.Convert(themeColor(theme.ColorNamePrimary)).(color.NRGBA)
	r.crop.FillColor = withAlpha(accent, 0x55)
	r.crop.StrokeColor = accent
	r.crop.StrokeWidth = 2
	r.Layout(r.c.Size())
	r.photo.Refresh()
	r.crop.Refresh()
}

func (r *cropIllustrationRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.photo, r.crop}
}
func (r *cropIllustrationRenderer) Destroy() {}

// --- Card -----------------------------------------------------------------------------------

// choiceCard is one selectable card.
type choiceCard struct {
	widget.DisableableWidget
	index    int
	selected bool
	hovered  bool
	focused  bool
	onSelect func(index int)
	onArrow  func(index, delta int)

	illus *cropIllustration
	title *widget.Label
	desc  *widget.RichText
	pill  *canvas.Text
}

func newChoiceCard(index int, c schema.ChoiceCard, onSelect func(int), onArrow func(index, delta int)) *choiceCard {
	card := &choiceCard{
		index:    index,
		onSelect: onSelect,
		onArrow:  onArrow,
		illus:    newCropIllustration(c.Illustration),
		title:    widget.NewLabel(c.Title),
		desc:     newHelpText(c.Description),
		pill:     canvas.NewText(i18n.T("Selected"), color.White),
	}
	card.title.TextStyle = fyne.TextStyle{Bold: true}
	card.title.Wrapping = fyne.TextWrapWord
	card.pill.TextSize = themeSize(theme.SizeNameCaptionText)
	card.pill.TextStyle = fyne.TextStyle{Bold: true}
	card.ExtendBaseWidget(card)
	return card
}

func (c *choiceCard) setSelected(sel bool) {
	if c.selected == sel {
		return
	}
	c.selected = sel
	c.Refresh()
}

// Tapped implements fyne.Tappable.
func (c *choiceCard) Tapped(*fyne.PointEvent) {
	if c.Disabled() {
		return
	}
	if cv := fyne.CurrentApp().Driver().CanvasForObject(c); cv != nil {
		cv.Focus(c)
	}
	if c.onSelect != nil {
		c.onSelect(c.index)
	}
}

// Cursor implements desktop.Cursorable.
func (c *choiceCard) Cursor() desktop.Cursor {
	if c.Disabled() {
		return desktop.DefaultCursor
	}
	return desktop.PointerCursor
}

// MouseIn implements desktop.Hoverable.
func (c *choiceCard) MouseIn(*desktop.MouseEvent) { c.hovered = true; c.Refresh() }

// MouseMoved implements desktop.Hoverable.
func (c *choiceCard) MouseMoved(*desktop.MouseEvent) {}

// MouseOut implements desktop.Hoverable.
func (c *choiceCard) MouseOut() { c.hovered = false; c.Refresh() }

// FocusGained implements fyne.Focusable.
func (c *choiceCard) FocusGained() { c.focused = true; c.Refresh() }

// FocusLost implements fyne.Focusable.
func (c *choiceCard) FocusLost() { c.focused = false; c.Refresh() }

// TypedRune implements fyne.Focusable: space selects.
func (c *choiceCard) TypedRune(r rune) {
	if r == ' ' && !c.Disabled() && c.onSelect != nil {
		c.onSelect(c.index)
	}
}

// TypedKey implements fyne.Focusable: arrows move the selection, enter selects.
func (c *choiceCard) TypedKey(e *fyne.KeyEvent) {
	if c.Disabled() {
		return
	}
	switch e.Name {
	case fyne.KeyLeft, fyne.KeyUp:
		if c.onArrow != nil {
			c.onArrow(c.index, -1)
		}
	case fyne.KeyRight, fyne.KeyDown:
		if c.onArrow != nil {
			c.onArrow(c.index, 1)
		}
	case fyne.KeyReturn, fyne.KeyEnter:
		if c.onSelect != nil {
			c.onSelect(c.index)
		}
	}
}

// MinSize implements fyne.Widget.
func (c *choiceCard) MinSize() fyne.Size {
	c.ExtendBaseWidget(c)
	return c.BaseWidget.MinSize()
}

// CreateRenderer implements fyne.Widget.
func (c *choiceCard) CreateRenderer() fyne.WidgetRenderer {
	c.ExtendBaseWidget(c)
	r := &choiceCardRenderer{
		c:        c,
		bg:       canvas.NewRectangle(color.Transparent),
		ring:     canvas.NewRectangle(color.Transparent),
		pillBG:   canvas.NewRectangle(color.Transparent),
		pillSize: fyne.NewSize(0, 0),
	}
	r.Refresh()
	return r
}

type choiceCardRenderer struct {
	c        *choiceCard
	bg       *canvas.Rectangle
	ring     *canvas.Rectangle
	pillBG   *canvas.Rectangle
	pillSize fyne.Size
}

const (
	pillPadX float32 = 8
	pillPadY float32 = 2
)

func (r *choiceCardRenderer) textBlockHeights(innerW float32) (titleH, descH float32) {
	r.c.title.Resize(fyne.NewSize(innerW, r.c.title.MinSize().Height))
	titleH = r.c.title.MinSize().Height
	r.c.desc.Resize(fyne.NewSize(innerW, r.c.desc.MinSize().Height))
	descH = r.c.desc.MinSize().Height
	return titleH, descH
}

func (r *choiceCardRenderer) MinSize() fyne.Size {
	innerW := r.c.Size().Width - 2*cardPad
	if innerW < cardMinWidth-2*cardPad {
		innerW = cardMinWidth - 2*cardPad
	}
	titleH, descH := r.textBlockHeights(innerW)
	h := cardPad + cardIllustrationH + 4 + titleH + descH + cardPad
	return fyne.NewSize(cardMinWidth, h)
}

func (r *choiceCardRenderer) Layout(size fyne.Size) {
	r.bg.Move(fyne.NewPos(0, 0))
	r.bg.Resize(size)
	r.ring.Move(fyne.NewPos(0, 0))
	r.ring.Resize(size)

	innerW := size.Width - 2*cardPad
	if innerW < 0 {
		innerW = 0
	}
	y := cardPad
	r.c.illus.Move(fyne.NewPos(cardPad, y))
	r.c.illus.Resize(fyne.NewSize(innerW, cardIllustrationH))
	y += cardIllustrationH + 4

	titleH, descH := r.textBlockHeights(innerW)
	r.c.title.Move(fyne.NewPos(cardPad-rowInnerPad(), y))
	r.c.title.Resize(fyne.NewSize(innerW+2*rowInnerPad(), titleH))
	y += titleH
	r.c.desc.Move(fyne.NewPos(cardPad-rowInnerPad(), y))
	r.c.desc.Resize(fyne.NewSize(innerW+2*rowInnerPad(), descH))

	ts := fyne.MeasureText(r.c.pill.Text, r.c.pill.TextSize, r.c.pill.TextStyle)
	r.pillSize = fyne.NewSize(ts.Width+2*pillPadX, ts.Height+2*pillPadY)
	px, py := size.Width-r.pillSize.Width-8, float32(8)
	r.pillBG.Move(fyne.NewPos(px, py))
	r.pillBG.Resize(r.pillSize)
	r.pillBG.CornerRadius = r.pillSize.Height / 2
	r.c.pill.Move(fyne.NewPos(px+pillPadX, py+pillPadY))
	r.c.pill.Resize(ts)
}

func (r *choiceCardRenderer) Refresh() {
	r.bg.CornerRadius = themeSize(sizeNameCardRadius)
	r.ring.CornerRadius = themeSize(sizeNameCardRadius)
	r.bg.FillColor = themeColor(colorNameCard)
	r.bg.StrokeColor = themeColor(colorNameBorder)
	r.bg.StrokeWidth = 1
	switch {
	case r.c.selected:
		r.bg.StrokeColor = color.Transparent
		r.ring.StrokeColor = themeColor(theme.ColorNamePrimary)
		r.ring.StrokeWidth = cardRingWidth
	case r.c.focused:
		r.ring.StrokeColor = themeColor(theme.ColorNameFocus)
		r.ring.StrokeWidth = cardRingWidth
	case r.c.hovered && !r.c.Disabled():
		r.bg.StrokeColor = themeColor(colorNameStrongBorder)
		r.ring.StrokeWidth = 0
	default:
		r.ring.StrokeWidth = 0
	}
	if r.c.selected {
		r.pillBG.FillColor = themeColor(theme.ColorNamePrimary)
		r.c.pill.Color = themeColor(theme.ColorNameForegroundOnPrimary)
		r.pillBG.Show()
		r.c.pill.Show()
	} else {
		r.pillBG.Hide()
		r.c.pill.Hide()
	}
	if r.c.Disabled() {
		r.bg.FillColor = themeColor(theme.ColorNameDisabledButton)
	}
	r.c.title.Refresh()
	r.c.desc.Refresh()
	r.c.illus.Refresh()
	r.Layout(r.c.Size())
	r.bg.Refresh()
	r.ring.Refresh()
	r.pillBG.Refresh()
	r.c.pill.Refresh()
}

func (r *choiceCardRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.c.illus, r.c.title, r.c.desc, r.ring, r.pillBG, r.c.pill}
}
func (r *choiceCardRenderer) Destroy() {}

// --- Row of cards ---------------------------------------------------------------------------

// cardRowLayout gives every card the same width and the height of the tallest one.
type cardRowLayout struct{ gap float32 }

func (l *cardRowLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var w, h float32
	for i, o := range objects {
		ms := o.MinSize()
		w += ms.Width
		if i > 0 {
			w += l.gap
		}
		if ms.Height > h {
			h = ms.Height
		}
	}
	return fyne.NewSize(w, h)
}

func (l *cardRowLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	n := float32(len(objects))
	if n == 0 {
		return
	}
	w := (size.Width - l.gap*(n-1)) / n
	// First pass sizes every card, so each card's text wraps at its final width; the second
	// reads the tallest MinSize.
	for i, o := range objects {
		o.Resize(fyne.NewSize(w, size.Height))
		o.Move(fyne.NewPos(float32(i)*(w+l.gap), 0))
	}
	var h float32
	for _, o := range objects {
		if ms := o.MinSize(); ms.Height > h {
			h = ms.Height
		}
	}
	if h < size.Height {
		h = size.Height
	}
	for _, o := range objects {
		o.Resize(fyne.NewSize(w, h))
	}
}

// renderChoiceCardsRows renders a ChoiceCardsItem as one inset row holding the card strip
// (with the item's label and help above it when given).
func (sm *SettingsManager) renderChoiceCardsRows(v schema.ChoiceCardsItem) []fyne.CanvasObject {
	initial := v.InitialValue
	if initial < 0 || initial >= len(v.Cards) {
		initial = 0
	}
	selected := initial
	sm.registry[v.Name] = initial
	if v.ApplyFunc != nil {
		sm.applyFuncs[v.Name] = func(val interface{}) {
			if i, ok := val.(int); ok {
				v.ApplyFunc(i)
			}
		}
	}

	cards := make([]*choiceCard, len(v.Cards))
	objs := make([]fyne.CanvasObject, len(v.Cards))

	// stage compares the selection with the saved baseline: a difference is a pending change that
	// "Save changes" applies; selecting the baseline again cancels it. Nothing is written here.
	stage := func(idx int) {
		if base, ok := sm.registry[v.Name].(int); ok && idx == base {
			delete(sm.chgPrefsCallbacks, v.Name)
			return
		}
		sm.chgPrefsCallbacks[v.Name] = func() {
			if v.ApplyFunc != nil {
				v.ApplyFunc(idx)
			}
			if v.NeedsRefresh {
				sm.refreshFlags[v.Name] = true
			}
		}
	}
	show := func(idx int) {
		selected = idx
		for i, c := range cards {
			c.setSelected(i == idx)
		}
	}
	choose := func(idx int, userInitiated bool) {
		if idx < 0 || idx >= len(cards) {
			return
		}
		show(idx)
		stage(idx)
		if userInitiated && v.OnChanged != nil {
			v.OnChanged(idx)
		}
		if sm.checkAndEnableApply != nil {
			sm.checkAndEnableApply()
		}
	}
	arrow := func(from, delta int) {
		next := from + delta
		if next < 0 || next >= len(cards) {
			return
		}
		choose(next, true)
		if cv := fyne.CurrentApp().Driver().CanvasForObject(cards[next]); cv != nil {
			cv.Focus(cards[next])
		}
	}
	for i, c := range v.Cards {
		cards[i] = newChoiceCard(i, c, func(idx int) { choose(idx, true) }, arrow)
		objs[i] = cards[i]
	}
	show(initial)

	sm.valueGetters[v.Name] = func() interface{} { return selected }
	// Programmatic SetValue (and RevertPending) re-stages against the baseline but never fires the
	// item's OnChanged hook.
	sm.valueSetters[v.Name] = func(val interface{}) {
		if i, ok := val.(int); ok {
			choose(i, false)
		}
	}
	strip := container.New(&cardRowLayout{gap: cardGap}, objs...)
	sm.allWidgets[v.Name] = strip

	var rows []fyne.CanvasObject
	if v.Label != "" {
		rows = append(rows, newInsetRow(v.Label, v.Help, nil, nil).Container)
	}
	row := newInsetRowWithLeft(container.New(&paddedLayout{x: 0, y: 4}, strip), nil, nil)
	// No changed-dot beside the strip (it would sit on the first card's corner): the selected
	// card, the save pill and the sidebar dot already show the pending change.
	sm.notePage(v.Name)
	for _, c := range cards {
		sm.manage(c, nil, v.EnabledIf, nil)
	}
	sm.manage(strip, row.Container, nil, v.VisibleIf)
	return append(rows, row.Container)
}
