package ui

import (
	"context"
	"reflect"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"github.com/dixieflatline76/Spice/v2/pkg/i18n"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
)

// Live items: the adapter for the read-only schema.StatsItem and schema.PreviewStripItem. Their
// content is computed by the domain from the staged (unsaved) values of other settings, off the
// UI thread, and drawn when it arrives. They never register a baseline or a pending change: they
// show the effect of a choice, they are not a choice.

// runLiveAsync is how live items run their compute step. Tests replace it with a synchronous runner.
func (sm *SettingsManager) runLiveAsync(f func()) {
	if sm.runAsync != nil {
		sm.runAsync(f)
		return
	}
	go f()
}

// bindLive wires a live item to its dependencies. compute runs off the UI thread and returns a
// function that applies the result; that function runs on the UI thread, and only if no newer
// computation started meanwhile. The first computation waits until the item's page is first
// shown (every page is rendered up front, and an unvisited page must not decode images); after
// that the item recomputes whenever the staged value of a dependency changes.
func (sm *SettingsManager) bindLive(deps []string, compute func(ctx context.Context, get func(string) interface{}) func()) {
	var (
		cancel  context.CancelFunc
		gen     int
		last    map[string]interface{}
		started bool
	)
	run := func(force bool) {
		snap := make(map[string]interface{}, len(deps))
		for _, d := range deps {
			if g, ok := sm.valueGetters[d]; ok {
				snap[d] = g()
			}
		}
		if !force && reflect.DeepEqual(snap, last) {
			return
		}
		last = snap
		if cancel != nil {
			cancel()
		}
		ctx, c := context.WithCancel(context.Background())
		cancel = c
		gen++
		mine := gen
		get := func(name string) interface{} { return snap[name] }
		sm.runLiveAsync(func() {
			apply := compute(ctx, get)
			if ctx.Err() != nil || apply == nil {
				return
			}
			fyne.Do(func() {
				if mine == gen {
					apply()
					sm.relayoutPages()
				}
			})
		})
	}
	start := func() {
		started = true
		run(true)
	}
	if sm.currentPage == "" {
		start()
	} else {
		sm.liveInits[sm.currentPage] = append(sm.liveInits[sm.currentPage], start)
	}
	sm.dirtyObservers = append(sm.dirtyObservers, func() {
		if started {
			run(false)
		}
	})
}

// notePageShown runs the first computation of the live items on a page that was just shown.
func (sm *SettingsManager) notePageShown(page string) {
	inits := sm.liveInits[page]
	if len(inits) == 0 {
		return
	}
	delete(sm.liveInits, page)
	for _, fn := range inits {
		fn()
	}
}

// relayoutPages re-lays out every rendered page after a live item changed height.
func (sm *SettingsManager) relayoutPages() {
	for _, s := range sm.scrolls {
		s.Content.Refresh()
		s.Refresh()
	}
}

// --- Stats --------------------------------------------------------------------------------------

func (sm *SettingsManager) renderStatsRows(v schema.StatsItem) []fyne.CanvasObject {
	box := newDividedBox()
	setStatRows(box, []fyne.CanvasObject{statNote(i18n.T("Loading…"))})
	if v.Compute == nil {
		return []fyne.CanvasObject{box}
	}
	sm.allWidgets[v.Name] = box
	sm.bindLive(v.DependsOn, func(ctx context.Context, get func(string) interface{}) func() {
		rows := v.Compute(ctx, get)
		return func() {
			objs := make([]fyne.CanvasObject, 0, len(rows))
			for _, r := range rows {
				objs = append(objs, statRowObject(r))
			}
			if len(objs) == 0 && v.Placeholder != "" {
				objs = append(objs, statNote(v.Placeholder))
			}
			setStatRows(box, objs)
		}
	})
	return []fyne.CanvasObject{box}
}

// setStatRows swaps the rows of a stats box. A box with no rows keeps one empty row so the divided
// layout always has something to measure.
func setStatRows(box *fyne.Container, rows []fyne.CanvasObject) {
	setDividedRows(box, rows)
}

func statNote(text string) fyne.CanvasObject {
	return newInsetRowWithLeft(newHelpText(text), nil, nil).Container
}

func statRowObject(r schema.StatRow) fyne.CanvasObject {
	if r.Value == "" {
		return statNote(r.Label)
	}
	value := widget.NewLabel(r.Value)
	if r.Muted {
		value.Importance = widget.LowImportance
	}
	return newInsetRow(r.Label, "", nil, value).Container
}

// --- Preview strip --------------------------------------------------------------------------

const (
	thumbSlotHeight   float32 = 120
	thumbSlotMinWidth float32 = 120
)

func (sm *SettingsManager) renderPreviewRows(v schema.PreviewStripItem) []fyne.CanvasObject {
	holder := container.NewStack()
	setPreview := func(obj fyne.CanvasObject) {
		holder.Objects = []fyne.CanvasObject{obj}
		holder.Refresh()
	}
	setPreview(newHelpText(i18n.T("Loading…")))
	row := newInsetRowWithLeft(container.New(&paddedLayout{x: 0, y: 4}, holder), nil, nil)
	if v.Compute == nil {
		return []fyne.CanvasObject{row.Container}
	}
	sm.allWidgets[v.Name] = holder
	sm.bindLive(v.DependsOn, func(ctx context.Context, get func(string) interface{}) func() {
		thumbs := v.Compute(ctx, get)
		return func() {
			if len(thumbs) == 0 {
				setPreview(newHelpText(v.Placeholder))
				return
			}
			cells := make([]fyne.CanvasObject, 0, len(thumbs))
			for _, t := range thumbs {
				cells = append(cells, newThumbCell(t))
			}
			setPreview(container.New(&cardRowLayout{gap: cardGap}, cells...))
		}
	})
	return []fyne.CanvasObject{row.Container}
}

// newThumbCell draws one thumbnail with its caption, or a placeholder carrying the note.
func newThumbCell(t schema.PreviewThumb) fyne.CanvasObject {
	bg := newThemedRect(colorNameSidebar, colorNameBorder, 6)
	bg.minSize = fyne.NewSize(thumbSlotMinWidth, thumbSlotHeight)
	var content fyne.CanvasObject
	if t.Image != nil {
		img := canvas.NewImageFromImage(t.Image)
		img.FillMode = canvas.ImageFillContain
		img.ScaleMode = canvas.ImageScaleSmooth
		content = container.New(&paddedLayout{x: 4, y: 4}, img)
	} else {
		note := widget.NewLabel(t.Note)
		note.Alignment = fyne.TextAlignCenter
		note.Wrapping = fyne.TextWrapWord
		note.Importance = widget.LowImportance
		content = container.NewCenter(note)
	}
	slot := container.NewStack(bg, content)

	caption := widget.NewLabel(cleanLabel(t.Caption))
	caption.TextStyle = fyne.TextStyle{Bold: true}
	caption.Wrapping = fyne.TextWrapWord
	objs := []fyne.CanvasObject{slot, container.New(&paddedLayout{x: 0, y: 2}, caption)}
	if t.Sub != "" {
		objs = append(objs, newHelpText(t.Sub))
	}
	return container.New(&tightVBoxLayout{overlap: rowInnerPad() + 1}, objs...)
}
