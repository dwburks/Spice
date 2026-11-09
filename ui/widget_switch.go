package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	switchWidth  float32 = 38
	switchHeight float32 = 22
	switchInset  float32 = 2
)

// Switch is an on/off toggle (pill track with a white knob, accent when on). It replaces
// widget.Check for BoolItem rows. Tapping or pressing space toggles it and fires OnChanged;
// SetChecked is for programmatic sync and never fires OnChanged.
type Switch struct {
	widget.DisableableWidget

	Checked   bool
	OnChanged func(bool) `json:"-"`

	focused bool
	hovered bool
}

// NewSwitch creates a switch in the given state.
func NewSwitch(checked bool, changed func(bool)) *Switch {
	s := &Switch{Checked: checked, OnChanged: changed}
	s.ExtendBaseWidget(s)
	return s
}

// SetChecked updates the state without firing OnChanged (programmatic UI synchronization).
func (s *Switch) SetChecked(checked bool) {
	if s.Checked == checked {
		return
	}
	s.Checked = checked
	s.Refresh()
}

// Toggle flips the state as a user action would, firing OnChanged.
func (s *Switch) Toggle() {
	if s.Disabled() {
		return
	}
	s.Checked = !s.Checked
	s.Refresh()
	if s.OnChanged != nil {
		s.OnChanged(s.Checked)
	}
}

// Tapped implements fyne.Tappable.
func (s *Switch) Tapped(*fyne.PointEvent) {
	if s.Disabled() {
		return
	}
	if c := fyne.CurrentApp().Driver().CanvasForObject(s); c != nil {
		c.Focus(s)
	}
	s.Toggle()
}

// Cursor implements desktop.Cursorable.
func (s *Switch) Cursor() desktop.Cursor {
	if s.Disabled() {
		return desktop.DefaultCursor
	}
	return desktop.PointerCursor
}

// MouseIn implements desktop.Hoverable.
func (s *Switch) MouseIn(*desktop.MouseEvent) { s.hovered = true; s.Refresh() }

// MouseMoved implements desktop.Hoverable.
func (s *Switch) MouseMoved(*desktop.MouseEvent) {}

// MouseOut implements desktop.Hoverable.
func (s *Switch) MouseOut() { s.hovered = false; s.Refresh() }

// FocusGained implements fyne.Focusable.
func (s *Switch) FocusGained() { s.focused = true; s.Refresh() }

// FocusLost implements fyne.Focusable.
func (s *Switch) FocusLost() { s.focused = false; s.Refresh() }

// TypedRune implements fyne.Focusable: space toggles.
func (s *Switch) TypedRune(r rune) {
	if r == ' ' {
		s.Toggle()
	}
}

// TypedKey implements fyne.Focusable.
func (s *Switch) TypedKey(*fyne.KeyEvent) {}

// MinSize implements fyne.Widget.
func (s *Switch) MinSize() fyne.Size {
	s.ExtendBaseWidget(s)
	return s.BaseWidget.MinSize()
}

// CreateRenderer implements fyne.Widget.
func (s *Switch) CreateRenderer() fyne.WidgetRenderer {
	s.ExtendBaseWidget(s)
	r := &switchRenderer{
		s:     s,
		focus: canvas.NewRectangle(color.Transparent),
		track: canvas.NewRectangle(color.Transparent),
		knob:  canvas.NewCircle(color.White),
	}
	r.Refresh()
	return r
}

type switchRenderer struct {
	s     *Switch
	focus *canvas.Rectangle
	track *canvas.Rectangle
	knob  *canvas.Circle
}

func (r *switchRenderer) MinSize() fyne.Size { return fyne.NewSize(switchWidth+4, switchHeight+4) }

func (r *switchRenderer) Layout(size fyne.Size) {
	ox := (size.Width - switchWidth) / 2
	oy := (size.Height - switchHeight) / 2
	r.focus.Move(fyne.NewPos(ox-2, oy-2))
	r.focus.Resize(fyne.NewSize(switchWidth+4, switchHeight+4))
	r.focus.CornerRadius = (switchHeight + 4) / 2
	r.track.Move(fyne.NewPos(ox, oy))
	r.track.Resize(fyne.NewSize(switchWidth, switchHeight))
	r.track.CornerRadius = switchHeight / 2

	d := switchHeight - 2*switchInset
	kx := ox + switchInset
	if r.s.Checked {
		kx = ox + switchWidth - switchInset - d
	}
	r.knob.Move(fyne.NewPos(kx, oy+switchInset))
	r.knob.Resize(fyne.NewSize(d, d))
}

func (r *switchRenderer) Refresh() {
	on := themeColor(theme.ColorNamePrimary)
	off := themeColor(colorNameSwitchTrack)
	if r.s.Checked {
		r.track.FillColor = on
	} else {
		r.track.FillColor = off
	}
	r.knob.FillColor = themeColor(colorNameSwitchKnob)
	if r.s.Disabled() {
		r.track.FillColor = fadeColor(r.track.FillColor, 0x66)
		r.knob.FillColor = fadeColor(r.knob.FillColor, 0xB0)
	}
	if r.s.focused {
		r.focus.FillColor = themeColor(theme.ColorNameFocus)
	} else if r.s.hovered && !r.s.Disabled() {
		r.focus.FillColor = themeColor(theme.ColorNameHover)
	} else {
		r.focus.FillColor = color.Transparent
	}
	r.Layout(r.s.Size())
	r.focus.Refresh()
	r.track.Refresh()
	r.knob.Refresh()
}

func (r *switchRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.focus, r.track, r.knob}
}

func (r *switchRenderer) Destroy() {}

// fadeColor returns c with its alpha scaled to a/255.
func fadeColor(c color.Color, a uint8) color.Color {
	n := color.NRGBAModel.Convert(c).(color.NRGBA)
	n.A = uint8(uint16(n.A) * uint16(a) / 0xFF)
	return n
}
