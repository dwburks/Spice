package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

// TestScrollPageToTopResetsNestedScrolls covers the Windows report that a page opened already
// scrolled down: every scroll inside a page, however deeply nested, goes back to the top.
func TestScrollPageToTopResetsNestedScrolls(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()

	tall := widget.NewLabel("x")
	tall.Resize(fyne.NewSize(100, 2000))
	inner := container.NewVScroll(container.NewVBox(tall, widget.NewLabel("y")))
	inner.Resize(fyne.NewSize(100, 100))
	inner.Offset = fyne.NewPos(0, 500)
	page := container.NewBorder(widget.NewLabel("header"), nil, nil, nil, container.NewStack(inner))

	scrollPageToTop(page)

	if inner.Offset.Y != 0 {
		t.Fatalf("nested scroll offset = %v, want 0", inner.Offset.Y)
	}
}
