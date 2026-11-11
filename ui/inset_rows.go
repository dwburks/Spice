package ui

import (
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Inset-list building blocks for the preferences window: themed rectangles, hairline-divided
// stacks, label/help/control rows, rounded cards and accent group labels.

const (
	rowPadX      float32 = 14
	rowPadY      float32 = 7
	rowGap       float32 = 16
	rowDotSize   float32 = 7
	groupGap     float32 = 22
	pagePadX     float32 = 28
	pageMaxWidth float32 = 820
)

// themedRect is a rectangle whose colors come from theme color names, so it follows theme changes.
// A negative radius means "pill" (half the height).
type themedRect struct {
	widget.BaseWidget
	fill        fyne.ThemeColorName
	stroke      fyne.ThemeColorName
	strokeWidth float32
	radius      float32
	minSize     fyne.Size
}

func newThemedRect(fill, stroke fyne.ThemeColorName, radius float32) *themedRect {
	r := &themedRect{fill: fill, stroke: stroke, radius: radius}
	if stroke != "" {
		r.strokeWidth = 1
	}
	r.ExtendBaseWidget(r)
	return r
}

func (r *themedRect) setFill(fill fyne.ThemeColorName) {
	if r.fill == fill {
		return
	}
	r.fill = fill
	r.Refresh()
}

func (r *themedRect) CreateRenderer() fyne.WidgetRenderer {
	rr := &themedRectRenderer{r: r, rect: canvas.NewRectangle(color.Transparent)}
	rr.Refresh()
	return rr
}

type themedRectRenderer struct {
	r    *themedRect
	rect *canvas.Rectangle
}

func (rr *themedRectRenderer) Layout(size fyne.Size) {
	rr.rect.Resize(size)
	if rr.r.radius < 0 {
		rr.rect.CornerRadius = size.Height / 2
	} else {
		rr.rect.CornerRadius = rr.r.radius
	}
}

func (rr *themedRectRenderer) MinSize() fyne.Size { return rr.r.minSize }

func (rr *themedRectRenderer) Refresh() {
	if rr.r.fill != "" {
		rr.rect.FillColor = themeColor(rr.r.fill)
	} else {
		rr.rect.FillColor = color.Transparent
	}
	if rr.r.stroke != "" {
		rr.rect.StrokeColor = themeColor(rr.r.stroke)
		rr.rect.StrokeWidth = rr.r.strokeWidth
	} else {
		rr.rect.StrokeWidth = 0
	}
	rr.Layout(rr.r.Size())
	rr.rect.Refresh()
}

func (rr *themedRectRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{rr.rect} }
func (rr *themedRectRenderer) Destroy()                     {}

// newDot returns the small accent "changed" dot.
func newDot() *themedRect {
	d := newThemedRect(theme.ColorNamePrimary, "", -1)
	d.minSize = fyne.NewSize(rowDotSize, rowDotSize)
	return d
}

// --- Hairline-divided vertical stack -------------------------------------------------------

// dividedLayout stacks rows (even indexes) and places a hairline (odd indexes) between every
// pair of visible, non-empty rows. Unused hairlines are collapsed to zero size.
type dividedLayout struct {
	inset float32 // left inset of the hairlines
}

func (l *dividedLayout) visibleRows(objects []fyne.CanvasObject) []int {
	var idx []int
	for i := 0; i < len(objects); i += 2 {
		if o := objects[i]; o.Visible() && o.MinSize().Height > 0 {
			idx = append(idx, i)
		}
	}
	return idx
}

func (l *dividedLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var w, h float32
	vis := l.visibleRows(objects)
	for k, i := range vis {
		ms := objects[i].MinSize()
		if ms.Width > w {
			w = ms.Width
		}
		h += ms.Height
		if k > 0 {
			h++
		}
	}
	return fyne.NewSize(w, h)
}

func (l *dividedLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for i := 1; i < len(objects); i += 2 {
		objects[i].Resize(fyne.NewSize(0, 0))
		objects[i].Move(fyne.NewPos(0, 0))
	}
	var y float32
	vis := l.visibleRows(objects)
	for k, i := range vis {
		if k > 0 {
			sep := objects[vis[k-1]+1]
			sep.Move(fyne.NewPos(l.inset, y))
			sep.Resize(fyne.NewSize(size.Width-l.inset, 1))
			y++
		}
		h := objects[i].MinSize().Height
		objects[i].Move(fyne.NewPos(0, y))
		objects[i].Resize(fyne.NewSize(size.Width, h))
		y += h
	}
}

func interleaveHairlines(rows []fyne.CanvasObject) []fyne.CanvasObject {
	objs := make([]fyne.CanvasObject, 0, len(rows)*2)
	for i, r := range rows {
		if i > 0 {
			sep := newThemedRect(colorNameBorder, "", 0)
			objs = append(objs, sep)
		}
		objs = append(objs, r)
	}
	return objs
}

// newDividedBox returns a container that stacks rows with hairline dividers between them.
func newDividedBox(rows ...fyne.CanvasObject) *fyne.Container {
	return container.New(&dividedLayout{inset: rowPadX}, interleaveHairlines(rows)...)
}

// setDividedRows replaces the rows of a divided box.
func setDividedRows(c *fyne.Container, rows []fyne.CanvasObject) {
	c.Objects = interleaveHairlines(rows)
	c.Refresh()
}

// --- Rows -----------------------------------------------------------------------------------

// tightVBoxLayout stacks objects vertically, overlapping each by `overlap` so the inner padding
// of consecutive labels does not double up (label directly above its help text).
type tightVBoxLayout struct{ overlap float32 }

func (l *tightVBoxLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var w, h float32
	n := 0
	for _, o := range objects {
		if !o.Visible() {
			continue
		}
		ms := o.MinSize()
		if ms.Height == 0 {
			continue
		}
		if ms.Width > w {
			w = ms.Width
		}
		h += ms.Height
		n++
	}
	if n > 1 {
		h -= float32(n-1) * l.overlap
	}
	return fyne.NewSize(w, h)
}

func (l *tightVBoxLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	var y float32
	first := true
	for _, o := range objects {
		if !o.Visible() {
			continue
		}
		h := o.MinSize().Height
		if h == 0 {
			o.Resize(fyne.NewSize(size.Width, 0))
			continue
		}
		if !first {
			y -= l.overlap
		}
		first = false
		o.Move(fyne.NewPos(0, y))
		o.Resize(fyne.NewSize(size.Width, h))
		y += h
	}
}

// collapseLayout gives its single label zero height while the label has no text.
type collapseLayout struct{}

func (collapseLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	if len(objects) == 0 {
		return fyne.NewSize(0, 0)
	}
	if l, ok := objects[0].(*widget.Label); ok && l.Text == "" {
		return fyne.NewSize(0, 0)
	}
	return objects[0].MinSize()
}

func (collapseLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		o.Move(fyne.NewPos(0, 0))
		o.Resize(size)
	}
}

// insetRowLayout lays out [dot, left, right?]: left text block, control right-aligned and
// vertically centered, changed-dot in the left margin next to the label's first line.
type insetRowLayout struct {
	label fyne.CanvasObject // first line of the left block (for dot alignment); may be nil
	flush bool              // left block has no inner text padding (buttons): align to rowPadX
}

// Child order of an inset row's container: search highlight, changed-dot, left block, control.
const (
	rowObjFlash = iota
	rowObjDot
	rowObjLeft
	rowObjControl
)

func (l *insetRowLayout) padLeft() float32 {
	if l.flush {
		return rowPadX
	}
	if p := rowPadX - rowInnerPad(); p > 0 {
		return p
	}
	return 0
}

func rowInnerPad() float32 { return theme.InnerPadding() }

func (l *insetRowLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	left := objects[rowObjLeft].MinSize()
	var right fyne.Size
	if len(objects) > rowObjControl && objects[rowObjControl].Visible() {
		right = objects[rowObjControl].MinSize()
	}
	padLeft := l.padLeft()
	w := padLeft + left.Width + rowPadX
	if right.Width > 0 {
		w += rowGap + right.Width
	}
	h := left.Height
	if right.Height > h {
		h = right.Height
	}
	h += 2 * rowPadY
	if min := themeSize(sizeNameRowHeight); h < min {
		h = min
	}
	return fyne.NewSize(w, h)
}

func (l *insetRowLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	flash, dot, left := objects[rowObjFlash], objects[rowObjDot], objects[rowObjLeft]
	flash.Move(fyne.NewPos(1, 1))
	flash.Resize(fyne.NewSize(size.Width-2, size.Height-2))
	padLeft := l.padLeft()
	rightEdge := size.Width - rowPadX
	leftW := rightEdge - padLeft
	if len(objects) > rowObjControl && objects[rowObjControl].Visible() {
		right := objects[rowObjControl]
		rs := right.MinSize()
		maxW := (size.Width - 2*rowPadX) * 0.62
		if rs.Width > maxW {
			rs.Width = maxW
		}
		right.Resize(rs)
		right.Move(fyne.NewPos(rightEdge-rs.Width, (size.Height-rs.Height)/2))
		leftW = rightEdge - rs.Width - rowGap - padLeft
	}
	if leftW < 0 {
		leftW = 0
	}
	left.Resize(fyne.NewSize(leftW, left.MinSize().Height))
	lh := left.MinSize().Height
	left.Resize(fyne.NewSize(leftW, lh))
	ly := (size.Height - lh) / 2
	left.Move(fyne.NewPos(padLeft, ly))

	lineH := float32(20)
	if l.label != nil {
		lineH = l.label.MinSize().Height
	}
	dot.Resize(fyne.NewSize(rowDotSize, rowDotSize))
	dot.Move(fyne.NewPos((rowPadX-rowDotSize)/2-1, ly+(lineH-rowDotSize)/2))
}

// cleanLabel strips a trailing colon from legacy label strings (P3 render-time cleanup; the
// strings themselves are rewritten in P5).
func cleanLabel(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ":")
	s = strings.TrimSuffix(s, "：")
	return strings.TrimSpace(s)
}

// newRowLabel returns the row's main label (13pt, wrapping).
func newRowLabel(text string) *widget.Label {
	l := widget.NewLabel(cleanLabel(text))
	l.Wrapping = fyne.TextWrapWord
	return l
}

// newHelpText returns muted, smaller, wrapping help text.
func newHelpText(text string) *widget.RichText {
	rich := widget.NewRichTextWithText(text)
	rich.Wrapping = fyne.TextWrapWord
	for i := range rich.Segments {
		if seg, ok := rich.Segments[i].(*widget.TextSegment); ok {
			seg.Style.SizeName = theme.SizeNameCaptionText
			seg.Style.ColorName = theme.ColorNamePlaceHolder
		}
	}
	return rich
}

// newGroupLabel returns the small accent semibold label above a card.
func newGroupLabel(text string) *widget.Label {
	l := widget.NewLabel(cleanLabel(text))
	l.Importance = widget.HighImportance
	l.TextStyle = fyne.TextStyle{Bold: true}
	l.SizeName = theme.SizeNameCaptionText
	l.Wrapping = fyne.TextWrapWord
	return l
}

// insetRow is one rendered settings row.
type insetRow struct {
	*fyne.Container
	dot   *themedRect
	label *widget.Label
	flash *canvas.Rectangle // search-result highlight; transparent except while flashing
}

// newInsetRow builds a row from a label, optional help, optional status label and optional control.
func newInsetRow(labelText, help string, status *widget.Label, control fyne.CanvasObject) *insetRow {
	var leftObjs []fyne.CanvasObject
	var lbl *widget.Label
	if labelText != "" {
		lbl = newRowLabel(labelText)
		leftObjs = append(leftObjs, lbl)
	}
	if help != "" {
		leftObjs = append(leftObjs, newHelpText(help))
	}
	if status != nil {
		leftObjs = append(leftObjs, container.New(collapseLayout{}, status))
	}
	left := container.New(&tightVBoxLayout{overlap: rowInnerPad() + 1}, leftObjs...)
	return newInsetRowWithLeft(left, lbl, control)
}

// newInsetRowWithLeft builds a row from an arbitrary left block.
func newInsetRowWithLeft(left fyne.CanvasObject, firstLine *widget.Label, control fyne.CanvasObject) *insetRow {
	dot := newDot()
	dot.Hide()
	var lineObj fyne.CanvasObject
	if firstLine != nil {
		lineObj = firstLine
	}
	flash := canvas.NewRectangle(color.Transparent)
	flash.CornerRadius = 6
	objs := []fyne.CanvasObject{flash, dot, left}
	if control != nil {
		objs = append(objs, control)
	}
	return &insetRow{
		Container: container.New(&insetRowLayout{label: lineObj}, objs...),
		dot:       dot,
		label:     firstLine,
		flash:     flash,
	}
}

// newFlushRow builds a row whose left block is a strip of buttons (no inner text padding).
func newFlushRow(left fyne.CanvasObject, control fyne.CanvasObject) *insetRow {
	r := newInsetRowWithLeft(left, nil, control)
	r.Container.Layout.(*insetRowLayout).flush = true
	return r
}

// blockRow pads a full-width block (lists, pickers) to the row's horizontal padding.
func blockRow(content fyne.CanvasObject, padX, padY float32) fyne.CanvasObject {
	return container.New(&paddedLayout{x: padX, y: padY}, content)
}

type paddedLayout struct{ x, y float32 }

func (p *paddedLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var w, h float32
	for _, o := range objects {
		if !o.Visible() {
			continue
		}
		ms := o.MinSize()
		if ms.Width > w {
			w = ms.Width
		}
		if ms.Height > h {
			h = ms.Height
		}
	}
	if h == 0 {
		return fyne.NewSize(0, 0)
	}
	return fyne.NewSize(w+2*p.x, h+2*p.y)
}

func (p *paddedLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		o.Move(fyne.NewPos(p.x, p.y))
		o.Resize(fyne.NewSize(size.Width-2*p.x, size.Height-2*p.y))
	}
}

// fixedWidthLayout gives its content a fixed width (or its MinSize width when larger).
type fixedWidthLayout struct{ width float32 }

func (f *fixedWidthLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var w, h float32
	for _, o := range objects {
		ms := o.MinSize()
		if ms.Width > w {
			w = ms.Width
		}
		if ms.Height > h {
			h = ms.Height
		}
	}
	if w < f.width {
		w = f.width
	}
	return fyne.NewSize(w, h)
}

func (f *fixedWidthLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		o.Move(fyne.NewPos(0, 0))
		o.Resize(size)
	}
}

func fixedWidth(obj fyne.CanvasObject, w float32) fyne.CanvasObject {
	return container.New(&fixedWidthLayout{width: w}, obj)
}

// newCard wraps a divided box of rows in a rounded card with a hairline border.
func newCard(box *fyne.Container) *fyne.Container {
	bg := newThemedRect(colorNameCard, colorNameBorder, themeSize(sizeNameCardRadius))
	return container.NewStack(bg, box)
}

// pageColumnLayout stacks a page's sections with group gaps, horizontal page padding and a
// maximum content width (centered). bottom reserves room for the floating save pill.
type pageColumnLayout struct {
	top, bottom float32
	padX        float32
	gap         float32
	maxWidth    float32
}

func (p *pageColumnLayout) visible(objects []fyne.CanvasObject) []fyne.CanvasObject {
	var out []fyne.CanvasObject
	for _, o := range objects {
		if o.Visible() && o.MinSize().Height > 0 {
			out = append(out, o)
		}
	}
	return out
}

func (p *pageColumnLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	vis := p.visible(objects)
	var w, h float32
	for i, o := range vis {
		ms := o.MinSize()
		if ms.Width > w {
			w = ms.Width
		}
		h += ms.Height
		if i > 0 {
			h += p.gap
		}
	}
	return fyne.NewSize(w+2*p.padX, h+p.top+p.bottom)
}

func (p *pageColumnLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	w := size.Width - 2*p.padX
	x := p.padX
	if p.maxWidth > 0 && w > p.maxWidth {
		x += (w - p.maxWidth) / 2
		w = p.maxWidth
	}
	y := p.top
	for _, o := range objects {
		if !o.Visible() || o.MinSize().Height == 0 {
			o.Resize(fyne.NewSize(w, 0))
			continue
		}
		if y > p.top {
			y += p.gap
		}
		h := o.MinSize().Height
		o.Move(fyne.NewPos(x, y))
		o.Resize(fyne.NewSize(w, h))
		y += h
	}
}
