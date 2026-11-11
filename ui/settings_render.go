package ui

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/dixieflatline76/Spice/v2/pkg/i18n"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
	utilLog "github.com/dixieflatline76/Spice/v2/util/log"
)

// Inset-list renderer: every SectionSchema becomes an accent group label plus a rounded card;
// every item becomes one or more rows (label + help on the left, control on the right).
// The registry pattern (baseline in sm.registry, live getter in sm.valueGetters, apply wrapper
// in sm.applyFuncs, staged callbacks in sm.chgPrefsCallbacks) is unchanged: only widget
// construction changed.

const (
	textFieldWidth    float32 = 240
	numericFieldWidth float32 = 110
	sliderWidth       float32 = 220
	secretFieldWidth  float32 = 200
	selectMinWidth    float32 = 180
)

// cardBuilder accumulates rows for one section; LabelItem{IsTitle} starts a new group.
type cardBuilder struct {
	blocks []fyne.CanvasObject // group labels, descriptions and finished cards, in order
	rows   []fyne.CanvasObject
	cards  []*fyne.Container
}

func (cb *cardBuilder) addRows(rows ...fyne.CanvasObject) {
	for _, r := range rows {
		if r != nil {
			cb.rows = append(cb.rows, r)
		}
	}
}

func (cb *cardBuilder) flush() {
	if len(cb.rows) == 0 {
		return
	}
	box := newDividedBox(cb.rows...)
	card := newCard(box)
	cb.cards = append(cb.cards, card)
	cb.blocks = append(cb.blocks, card)
	cb.rows = nil
}

func (cb *cardBuilder) addHeading(text string) {
	cb.flush()
	cb.blocks = append(cb.blocks, newGroupLabel(text))
}

// RenderSchema takes a pure Go UI definition and renders it to a Fyne container: one outer
// VScroll whose content stacks one block per section.
func (sm *SettingsManager) RenderSchema(p schema.PanelSchema) fyne.CanvasObject {
	var sectionObjs []fyne.CanvasObject
	for _, section := range p.Sections {
		if obj := sm.renderSection(section); obj != nil {
			sectionObjs = append(sectionObjs, obj)
		}
	}
	column := container.New(&pageColumnLayout{
		top:      sm.pageTopInset,
		bottom:   sm.pageBottomInset,
		padX:     sm.pagePadX,
		gap:      groupGap,
		maxWidth: sm.pageMaxWidth,
	}, sectionObjs...)
	scroll := container.NewVScroll(column)
	sm.scrolls = append(sm.scrolls, scroll)
	return scroll
}

// renderSection renders one section as [group label] [description] card(s).
func (sm *SettingsManager) renderSection(section schema.SectionSchema) fyne.CanvasObject {
	cb := &cardBuilder{}
	if section.Title != "" {
		cb.blocks = append(cb.blocks, newGroupLabel(section.Title))
	}
	if section.Description != "" {
		d := newHelpText(section.Description)
		cb.blocks = append(cb.blocks, d)
	}
	for _, item := range section.Items {
		if li, ok := item.(schema.LabelItem); ok && li.IsTitle {
			cb.addHeading(li.Text)
			continue
		}
		cb.addRows(sm.renderItemRows(item)...)
	}
	cb.flush()
	if len(cb.blocks) == 0 {
		return nil
	}

	block := container.New(&tightVBoxLayout{overlap: 2}, cb.blocks...)
	// Hide the whole group when every row in it is hidden (e.g. OS-specific startup rows).
	cards := cb.cards
	if len(cards) > 0 {
		sm.managedWidgets = append(sm.managedWidgets, managedWidget{
			widget: block,
			visibleIf: func() bool {
				for _, c := range cards {
					box := c.Objects[1].(*fyne.Container)
					for i := 0; i < len(box.Objects); i += 2 {
						if box.Objects[i].Visible() {
							return true
						}
					}
				}
				return false
			},
		})
	}
	return block
}

// renderItemRows renders a single schema item into card rows.
func (sm *SettingsManager) renderItemRows(item schema.ItemSchema) []fyne.CanvasObject {
	switch v := item.(type) {
	case *schema.OAuthPickerItem:
		return sm.renderOAuthPickerRows(*v)
	case schema.OAuthPickerItem:
		return sm.renderOAuthPickerRows(v)
	case schema.BoolItem:
		return []fyne.CanvasObject{sm.renderBoolRow(v, nil)}
	case schema.TextItem:
		return []fyne.CanvasObject{sm.renderTextRow(v)}
	case schema.SelectItem:
		return []fyne.CanvasObject{sm.renderSelectRow(v)}
	case schema.SliderItem:
		return []fyne.CanvasObject{sm.renderSliderRow(v)}
	case schema.ChoiceCardsItem:
		return sm.renderChoiceCardsRows(v)
	case schema.StatsItem:
		return sm.renderStatsRows(v)
	case schema.PreviewStripItem:
		return sm.renderPreviewRows(v)
	case schema.AsyncButtonItem:
		return []fyne.CanvasObject{sm.renderAsyncButtonRow(v)}
	case schema.ConfirmButtonItem:
		return []fyne.CanvasObject{sm.renderConfirmButtonRow(v)}
	case schema.ButtonItem:
		return []fyne.CanvasObject{sm.renderButtonRow(v)}
	case schema.HyperlinkItem:
		return []fyne.CanvasObject{sm.renderHyperlinkRow(v)}
	case schema.LabelItem:
		return []fyne.CanvasObject{sm.renderLabelRow(v)}
	case schema.QueryListItem:
		return []fyne.CanvasObject{sm.renderQueryListBlock(v)}
	case schema.FolderPickerItem:
		return []fyne.CanvasObject{sm.renderFolderPickerRow(v)}
	case schema.AppPickerItem:
		return sm.renderAppPickerRows(v)
	case *schema.HorizontalRowItem:
		return sm.renderHorizontalRows(*v)
	case schema.HorizontalRowItem:
		return sm.renderHorizontalRows(v)
	case schema.SecretItem:
		return []fyne.CanvasObject{sm.renderSecretRow(v)}
	}
	return nil
}

// manage registers EnabledIf/VisibleIf: enabledIf applies to the control, visibleIf to the row.
func (sm *SettingsManager) manage(control, row fyne.CanvasObject, enabledIf, visibleIf func() bool) {
	if enabledIf != nil && control != nil {
		sm.managedWidgets = append(sm.managedWidgets, managedWidget{widget: control, enabledIf: enabledIf})
	}
	if visibleIf != nil {
		// Hide the control itself (callers inspect it) and the whole row around it.
		if control != nil && control != row {
			sm.managedWidgets = append(sm.managedWidgets, managedWidget{widget: control, visibleIf: visibleIf})
		}
		if row != nil {
			sm.managedWidgets = append(sm.managedWidgets, managedWidget{widget: row, visibleIf: visibleIf})
		}
	}
}

// trackRow records the row's changed-dot and the page it lives on for the setting name.
func (sm *SettingsManager) trackRow(name string, row *insetRow) {
	if name == "" || row == nil {
		return
	}
	sm.rowDots[name] = append(sm.rowDots[name], row.dot)
	sm.notePage(name)
}

// notePage maps a setting name to the page currently being rendered (for sidebar dots / pill).
func (sm *SettingsManager) notePage(name string) {
	if name != "" && sm.currentPage != "" {
		if _, ok := sm.pageOf[name]; !ok {
			sm.pageOf[name] = sm.currentPage
		}
	}
}

func (sm *SettingsManager) newSmallButton(text string, icon fyne.Resource, onTapped func()) *widget.Button {
	b := widget.NewButtonWithIcon(text, icon, onTapped)
	return b
}

// --- Bool --------------------------------------------------------------------------------------

// renderBoolRow renders a BoolItem as a switch row. extra controls (e.g. a Preview button) are
// placed before the switch.
func (sm *SettingsManager) renderBoolRow(v schema.BoolItem, extra []fyne.CanvasObject) fyne.CanvasObject {
	sw := NewSwitch(v.InitialValue, nil)
	sm.registry[v.Name] = v.InitialValue
	if v.ApplyFunc != nil {
		sm.applyFuncs[v.Name] = func(val interface{}) { v.ApplyFunc(val.(bool)) }
	}
	stage := func(b bool) {
		if base, ok := sm.registry[v.Name].(bool); ok && b == base {
			delete(sm.chgPrefsCallbacks, v.Name)
			return
		}
		applyFunc := v.ApplyFunc
		needsRefresh := v.NeedsRefresh
		settingName := v.Name
		sm.chgPrefsCallbacks[settingName] = func() {
			if applyFunc != nil {
				applyFunc(b)
			}
			if needsRefresh {
				sm.refreshFlags[settingName] = true
			}
		}
	}
	sw.OnChanged = func(b bool) {
		stage(b)
		if v.OnChanged != nil {
			v.OnChanged(b)
		}
		sm.checkAndEnableApply()
	}
	// Programmatic SetValue re-evaluates the staged state against the baseline (as widget.Check's
	// OnChanged did before) but never re-enters the item's OnChanged hook.
	sm.stagers[v.Name] = func(val interface{}) {
		if b, ok := val.(bool); ok {
			stage(b)
		}
	}
	sm.valueGetters[v.Name] = func() interface{} { return sw.Checked }
	sm.allWidgets[v.Name] = sw

	var control fyne.CanvasObject = sw
	var trailing []fyne.CanvasObject
	if v.ActionText != "" && v.ActionFunc != nil {
		link := widget.NewHyperlink(v.ActionText, nil)
		link.OnTapped = v.ActionFunc
		trailing = append(trailing, link)
	}
	trailing = append(trailing, extra...)
	if len(trailing) > 0 {
		control = container.NewHBox(append(trailing, sw)...)
	}

	row := newInsetRow(v.Label, v.Help, nil, control)
	sm.trackRow(v.Name, row)
	sm.manage(sw, row.Container, v.EnabledIf, v.VisibleIf)
	return row.Container
}

// --- Text --------------------------------------------------------------------------------------

func (sm *SettingsManager) renderTextRow(v schema.TextItem) fyne.CanvasObject {
	var validator fyne.StringValidator
	if v.Validator != nil {
		validator = v.Validator
	}
	cfg := &textEntrySettingConfig{
		Name:               v.Name,
		InitialValue:       v.InitialValue,
		PlaceHolder:        v.PlaceHolder,
		Validator:          validator,
		OnChanged:          v.OnChanged,
		PostValidateCheck:  v.PostValidateCheck,
		ApplyFunc:          v.ApplyFunc,
		NeedsRefresh:       v.NeedsRefresh,
		SkipApply:          v.SkipApply,
		DisplayStatus:      v.DisplayStatus,
		IsPassword:         v.IsPassword,
		IsNumeric:          v.IsNumeric,
		EnabledIf:          v.EnabledIf,
		VisibleIf:          v.VisibleIf,
		ValidationDebounce: v.ValidationDebounce,
	}
	entry, status := sm.buildTextEntry(cfg)
	width := textFieldWidth
	if v.IsNumeric {
		width = numericFieldWidth
	}
	if len(v.Presets) > 0 {
		return sm.renderPresetTextRow(v, entry, status, width)
	}
	row := newInsetRow(v.Label, v.Help, status, fixedWidth(entry, width))
	sm.trackRow(v.Name, row)
	sm.manage(entry, row.Container, v.EnabledIf, v.VisibleIf)
	return row.Container
}

// renderPresetTextRow renders a TextItem with Presets: a select of named values plus a Custom
// option that reveals the entry. The entry stays the registered setting; picking a preset sets its
// text, which stages the change through the entry's normal OnChanged path.
func (sm *SettingsManager) renderPresetTextRow(v schema.TextItem, entry *widget.Entry, status *widget.Label, width float32) fyne.CanvasObject {
	options := make([]string, 0, len(v.Presets)+1)
	for _, p := range v.Presets {
		options = append(options, p.Label)
	}
	customIdx := len(options)
	options = append(options, v.CustomLabel)

	presetIndex := func(val string) int {
		for i, p := range v.Presets {
			if p.Value == val {
				return i
			}
		}
		return -1
	}

	sel := widget.NewSelect(options, nil)
	custom := []fyne.CanvasObject{fixedWidth(entry, width)}
	if v.Suffix != "" {
		suffix := widget.NewLabel(v.Suffix)
		suffix.Importance = widget.LowImportance
		custom = append(custom, suffix)
	}
	customBox := container.NewHBox(custom...)
	control := container.NewHBox(customBox, fixedWidth(sel, selectMinWidth))

	showCustom := func(show bool) {
		if show == customBox.Visible() {
			return
		}
		if show {
			customBox.Show()
		} else {
			customBox.Hide()
		}
		control.Refresh()
	}
	// selectSilently mirrors a value in the select without re-entering its OnChanged.
	selectSilently := func(val string) {
		handler := sel.OnChanged
		sel.OnChanged = nil
		if i := presetIndex(val); i >= 0 {
			sel.SetSelectedIndex(i)
			showCustom(false)
		} else {
			sel.SetSelectedIndex(customIdx)
			showCustom(true)
		}
		sel.OnChanged = handler
	}
	selectSilently(v.InitialValue)

	sel.OnChanged = func(string) {
		i := sel.SelectedIndex()
		if i == customIdx || i < 0 {
			showCustom(true)
			return
		}
		showCustom(false)
		entry.SetText(v.Presets[i].Value) // stages via the entry's OnChanged, like typing
	}
	// Programmatic SetValue / Revert on the entry keeps the select in step.
	sm.afterSetHooks[v.Name] = func(val interface{}) {
		if s, ok := val.(string); ok {
			selectSilently(s)
		}
	}
	sm.allWidgets[v.Name+"__preset"] = sel
	sm.allWidgets[v.Name+"__custom"] = customBox

	row := newInsetRow(v.Label, v.Help, status, control)
	sm.trackRow(v.Name, row)
	sm.manage(entry, row.Container, v.EnabledIf, v.VisibleIf)
	if v.EnabledIf != nil {
		sm.managedWidgets = append(sm.managedWidgets, managedWidget{widget: sel, enabledIf: v.EnabledIf})
	}
	return row.Container
}

// buildTextEntry creates and registers a text entry (registry, getter, apply wrapper, status label).
func (sm *SettingsManager) buildTextEntry(cfg *textEntrySettingConfig) (*widget.Entry, *widget.Label) {
	entry := widget.NewEntry()
	entry.SetPlaceHolder(cfg.PlaceHolder)
	entry.SetText(cfg.InitialValue)
	sm.registry[cfg.Name] = cfg.InitialValue
	sm.applyFuncs[cfg.Name] = func(val interface{}) {
		if cfg.ApplyFunc != nil {
			cfg.ApplyFunc(val.(string))
		}
	}
	sm.valueGetters[cfg.Name] = func() interface{} { return entry.Text }
	sm.allWidgets[cfg.Name] = entry
	if cfg.IsPassword {
		entry.Password = true
	}
	if cfg.Validator != nil {
		entry.Validator = cfg.Validator
	}

	statusLabel := widget.NewLabel("")
	statusLabel.SizeName = theme.SizeNameCaptionText
	statusLabel.Wrapping = fyne.TextWrapWord
	sm.statusLabels[cfg.Name] = statusLabel

	var debounceTimer *time.Timer
	entry.OnChanged = func(s string) {
		if cfg.IsNumeric {
			var clean []rune
			for _, r := range s {
				if r >= '0' && r <= '9' {
					clean = append(clean, r)
				}
			}
			for len(clean) > 1 && clean[0] == '0' {
				clean = clean[1:]
			}
			cleanStr := string(clean)
			if s != cleanStr {
				entry.SetText(cleanStr)
				return
			}
		}
		sm.handleTextEntryChanged(s, cfg, entry, statusLabel, &debounceTimer)
	}
	return entry, statusLabel
}

// --- Select ------------------------------------------------------------------------------------

func (sm *SettingsManager) renderSelectRow(v schema.SelectItem) fyne.CanvasObject {
	selectWidget := widget.NewSelect(v.Options, func(string) {})
	initial, _ := v.InitialValue.(int)
	selectWidget.SetSelectedIndex(initial)
	sm.registry[v.Name] = initial
	sm.applyFuncs[v.Name] = func(val interface{}) {
		switch x := val.(type) {
		case int:
			v.ApplyFunc(x)
		case string:
			for i, opt := range v.Options {
				if opt == x {
					v.ApplyFunc(i)
					return
				}
			}
		}
	}
	selectWidget.OnChanged = func(s string) {
		selectedIndex := -1
		for i, opt := range v.Options {
			if opt == s {
				selectedIndex = i
				break
			}
		}
		if selectedIndex != sm.registry[v.Name].(int) {
			sm.chgPrefsCallbacks[v.Name] = func() {
				v.ApplyFunc(selectedIndex)
				if v.NeedsRefresh {
					sm.refreshFlags[v.Name] = true
				}
			}
		} else {
			delete(sm.chgPrefsCallbacks, v.Name)
		}
		if v.OnChanged != nil {
			v.OnChanged(s, selectedIndex)
		}
		sm.checkAndEnableApply()
	}
	sm.valueGetters[v.Name] = func() interface{} {
		for i, opt := range v.Options {
			if opt == selectWidget.Selected {
				return i
			}
		}
		return -1
	}
	sm.allWidgets[v.Name] = selectWidget

	row := newInsetRow(v.Label, v.Help, nil, fixedWidth(selectWidget, selectMinWidth))
	sm.trackRow(v.Name, row)
	sm.manage(selectWidget, row.Container, v.EnabledIf, v.VisibleIf)
	return row.Container
}

// --- Slider ------------------------------------------------------------------------------------

func (sm *SettingsManager) renderSliderRow(v schema.SliderItem) fyne.CanvasObject {
	slider := widget.NewSlider(v.Min, v.Max)
	slider.Step = v.Step
	slider.SetValue(v.InitialValue)
	valLabel := widget.NewLabel(fmt.Sprintf(v.Format, v.InitialValue))
	slider.OnChanged = func(f float64) { valLabel.SetText(fmt.Sprintf(v.Format, f)) }

	sm.registry[v.Name] = v.InitialValue
	if v.ApplyFunc != nil {
		sm.applyFuncs[v.Name] = func(val interface{}) { v.ApplyFunc(val.(float64)) }
	}
	slider.OnChangeEnded = func(f float64) {
		if f != sm.registry[v.Name].(float64) {
			applyFunc := v.ApplyFunc
			needsRefresh := v.NeedsRefresh
			settingName := v.Name
			sm.chgPrefsCallbacks[settingName] = func() {
				if applyFunc != nil {
					applyFunc(f)
				}
				if needsRefresh {
					sm.refreshFlags[settingName] = true
				}
			}
		} else {
			delete(sm.chgPrefsCallbacks, v.Name)
		}
		if v.OnChanged != nil {
			v.OnChanged(f)
		}
		sm.checkAndEnableApply()
	}
	sm.valueGetters[v.Name] = func() interface{} { return slider.Value }
	sm.allWidgets[v.Name] = slider

	control := fixedWidth(container.NewBorder(nil, nil, nil, valLabel, slider), sliderWidth)
	row := newInsetRow(v.Label, v.Help, nil, control)
	sm.trackRow(v.Name, row)
	sm.manage(slider, row.Container, v.EnabledIf, v.VisibleIf)
	if v.EnabledIf != nil {
		sm.managedWidgets = append(sm.managedWidgets, managedWidget{widget: valLabel, enabledIf: v.EnabledIf})
	}
	return row.Container
}

// --- Buttons -----------------------------------------------------------------------------------

// actionRow renders a label-less button as a left-aligned action row ("+ Add ..." style).
func actionRow(btn *widget.Button) *insetRow {
	return newFlushRow(container.NewHBox(btn), nil)
}

func (sm *SettingsManager) renderButtonRow(v schema.ButtonItem) fyne.CanvasObject {
	btn := sm.newSmallButton(v.ButtonText, sm.getIconResource(v.IconName), v.OnPressed)
	btn.Importance = sm.mapImportance(v.Importance)
	if v.Importance == "" {
		btn.Importance = widget.MediumImportance
	}
	var row *insetRow
	if v.Label != "" {
		row = newInsetRow(v.Label, v.Help, nil, btn)
	} else {
		if isAddIcon(v.IconName) {
			btn.Importance = widget.LowImportance
		}
		row = actionRow(btn)
	}
	sm.manage(btn, row.Container, v.EnabledIf, v.VisibleIf)
	return row.Container
}

func isAddIcon(name string) bool {
	n := strings.ToLower(name)
	return n == "add" || n == "plus"
}

func (sm *SettingsManager) renderConfirmButtonRow(v schema.ConfirmButtonItem) fyne.CanvasObject {
	button := sm.newSmallButton(v.ButtonText, sm.getIconResource(v.IconName), func() {
		if v.ConfirmTitle != "" && v.ConfirmMessage != "" {
			d := dialog.NewConfirm(v.ConfirmTitle, v.ConfirmMessage, func(b bool) {
				if b {
					v.OnPressed()
				}
			}, sm.prefsWindow)
			d.Show()
		} else {
			v.OnPressed()
		}
	})
	button.Importance = sm.mapImportanceDefault(v.Importance, widget.LowImportance)
	var row *insetRow
	if v.Label != "" {
		row = newInsetRow(v.Label, v.Help, nil, button)
	} else {
		row = actionRow(button)
	}
	sm.manage(button, row.Container, v.EnabledIf, v.VisibleIf)
	return row.Container
}

func (sm *SettingsManager) renderAsyncButtonRow(v schema.AsyncButtonItem) fyne.CanvasObject {
	importance := widget.LowImportance
	switch v.Style {
	case schema.ButtonStylePrimary:
		importance = widget.HighImportance
	case schema.ButtonStyleDanger:
		importance = widget.DangerImportance
	case schema.ButtonStyleSuccess:
		importance = widget.SuccessImportance
	}
	btn := sm.buildAsyncButton(v, importance)
	row := newInsetRowWithLeft(layout.NewSpacer(), nil, btn)
	sm.manage(btn, row.Container, v.EnabledIf, v.VisibleIf)
	return row.Container
}

// buildAsyncButton creates a button that runs OnPressed in the background and returns to the UI thread.
func (sm *SettingsManager) buildAsyncButton(cfg schema.AsyncButtonItem, importance widget.Importance) *widget.Button {
	btn := widget.NewButtonWithIcon(cfg.ButtonText, sm.getIconResource(cfg.IconName), nil)
	btn.Importance = importance
	btn.OnTapped = func() {
		originalText := btn.Text
		btn.Disable()
		sm.applyButton.Disable()
		btn.SetText(cfg.LoadingText)
		btn.Refresh()

		go func() {
			err := cfg.OnPressed()
			fyne.Do(func() {
				btn.Enable()
				btn.SetText(originalText)
				sm.applyButton.Enable()

				if cfg.TargetStatusKey != "" {
					if err != nil {
						sm.SetSettingStatus(cfg.TargetStatusKey, err.Error(), schema.ImportanceDanger)
					} else {
						sm.SetSettingStatus(cfg.TargetStatusKey, i18n.T("Success"), schema.ImportanceSuccess)
					}
				}

				cfg.OnCompleted(err)
				if cfg.NeedsRefresh {
					sm.fullRefresh()
				} else {
					sm.refreshWidgetStates()
				}
				sm.checkAndEnableApply()
			})
		}()
	}
	sm.allWidgets[cfg.Name] = btn
	return btn
}

// --- Static rows -------------------------------------------------------------------------------

func (sm *SettingsManager) renderHyperlinkRow(v schema.HyperlinkItem) fyne.CanvasObject {
	var hl fyne.CanvasObject
	if u, err := url.Parse(v.URL); err == nil {
		hl = widget.NewHyperlink(v.Text, u)
	} else {
		hl = widget.NewLabel(v.Text + " (" + v.URL + ")")
	}
	if v.ID != "" {
		sm.allWidgets[v.ID] = hl
	}
	return newInsetRowWithLeft(hl, nil, nil).Container
}

func (sm *SettingsManager) renderLabelRow(v schema.LabelItem) fyne.CanvasObject {
	var content fyne.CanvasObject
	if v.Importance == schema.ImportanceLow {
		content = newHelpText(v.Text)
	} else {
		lbl := widget.NewLabel(v.Text)
		lbl.Wrapping = fyne.TextWrapWord
		content = lbl
	}
	row := newInsetRowWithLeft(content, nil, nil)
	if v.ID != "" {
		sm.allWidgets[v.ID] = content
	}
	return row.Container
}

// --- Horizontal rows ---------------------------------------------------------------------------

// renderHorizontalRows maps a HorizontalRowItem onto the inset list:
//   - BoolItem first: one switch row with the remaining items as trailing controls (e.g. Preview)
//   - LabelItem first: the label on the left, remaining items on the right
//   - otherwise: a strip of small buttons/links
func (sm *SettingsManager) renderHorizontalRows(v schema.HorizontalRowItem) []fyne.CanvasObject {
	if len(v.Items) == 0 {
		return nil
	}
	var rows []fyne.CanvasObject
	switch first := v.Items[0].(type) {
	case schema.BoolItem:
		var extra []fyne.CanvasObject
		for _, it := range v.Items[1:] {
			if c := sm.renderInlineControl(it); c != nil {
				extra = append(extra, c)
			}
		}
		rows = append(rows, sm.renderBoolRow(first, extra))
	case schema.LabelItem:
		var rest []fyne.CanvasObject
		for _, it := range v.Items[1:] {
			if c := sm.renderInlineControl(it); c != nil {
				rest = append(rest, c)
			}
		}
		var left fyne.CanvasObject
		if first.Importance == schema.ImportanceLow {
			left = newHelpText(first.Text)
		} else {
			left = newRowLabel(first.Text)
		}
		var right fyne.CanvasObject
		if len(rest) > 0 {
			right = container.NewHBox(rest...)
		}
		rows = append(rows, newInsetRowWithLeft(left, nil, right).Container)
	default:
		var ctrls []fyne.CanvasObject
		for _, it := range v.Items {
			if c := sm.renderInlineControl(it); c != nil {
				ctrls = append(ctrls, c)
			}
		}
		rows = append(rows, newFlushRow(container.NewHBox(ctrls...), nil).Container)
	}
	if v.ID != "" && len(rows) > 0 {
		sm.allWidgets[v.ID] = rows[0]
	}
	return rows
}

// renderInlineControl renders an item as a bare control for use inside a horizontal row.
func (sm *SettingsManager) renderInlineControl(item schema.ItemSchema) fyne.CanvasObject {
	switch it := item.(type) {
	case schema.ButtonItem:
		btn := sm.newSmallButton(it.ButtonText, sm.getIconResource(it.IconName), it.OnPressed)
		btn.Importance = sm.mapImportance(it.Importance)
		sm.manage(btn, btn, it.EnabledIf, it.VisibleIf)
		return btn
	case schema.ConfirmButtonItem:
		btn := sm.newSmallButton(it.ButtonText, sm.getIconResource(it.IconName), func() {
			if it.ConfirmTitle != "" && it.ConfirmMessage != "" {
				dialog.NewConfirm(it.ConfirmTitle, it.ConfirmMessage, func(b bool) {
					if b {
						it.OnPressed()
					}
				}, sm.prefsWindow).Show()
			} else {
				it.OnPressed()
			}
		})
		btn.Importance = sm.mapImportance(it.Importance)
		sm.manage(btn, btn, it.EnabledIf, it.VisibleIf)
		return btn
	case schema.HyperlinkItem:
		if u, err := url.Parse(it.URL); err == nil {
			return widget.NewHyperlink(it.Text, u)
		}
		return widget.NewLabel(it.Text)
	case schema.LabelItem:
		if it.Importance == schema.ImportanceLow {
			return newHelpText(it.Text)
		}
		return widget.NewLabel(it.Text)
	case schema.TextItem:
		var validator fyne.StringValidator
		if it.Validator != nil {
			validator = it.Validator
		}
		entry, _ := sm.buildTextEntry(&textEntrySettingConfig{
			Name:               it.Name,
			InitialValue:       it.InitialValue,
			PlaceHolder:        it.PlaceHolder,
			IsPassword:         it.IsPassword,
			NeedsRefresh:       it.NeedsRefresh,
			DisplayStatus:      it.DisplayStatus,
			ValidationDebounce: it.ValidationDebounce,
			OnChanged:          it.OnChanged,
			PostValidateCheck:  it.PostValidateCheck,
			ApplyFunc:          it.ApplyFunc,
			Validator:          validator,
		})
		sm.notePage(it.Name)
		sm.manage(entry, entry, it.EnabledIf, it.VisibleIf)
		return fixedWidth(entry, textFieldWidth)
	case schema.BoolItem:
		// A bool that is not the first item of a row: render as a labelled switch pair.
		return sm.renderBoolRow(it, nil)
	}
	return nil
}

// --- Folder & app pickers ----------------------------------------------------------------------

func (sm *SettingsManager) renderFolderPickerRow(v schema.FolderPickerItem) fyne.CanvasObject {
	page := sm.currentPage
	btn := widget.NewButtonWithIcon(v.ButtonText, theme.FolderOpenIcon(), func() {
		showOSFolderPicker(sm.GetSettingsWindow(), func(path string, err error) {
			if err != nil || path == "" {
				return
			}
			queryID, err := v.OnFolderSelected(path)
			if err != nil || queryID == "" {
				return
			}
			if page != "" {
				sm.pageOf[queryID] = page
			}
			// Seed baseline as inactive so the pending change shows as checked.
			sm.SeedBaseline(queryID, false)
			// Register a pending callback to enable the query when Apply is clicked.
			enableQuery := v.EnableQuery
			sm.SetSettingChangedCallback(queryID, func() {
				if enableQuery != nil {
					_ = enableQuery(queryID)
				}
				sm.SeedBaseline(queryID, true)
			})
			sm.SetRefreshFlag(queryID)
			sm.fullRefresh()
			sm.checkAndEnableApply()
		})
	})
	btn.Importance = widget.LowImportance
	sm.allWidgets[v.Name] = btn
	var row *insetRow
	if v.Label != "" {
		row = newInsetRow(v.Label, v.Help, nil, btn)
	} else {
		row = actionRow(btn)
	}
	sm.manage(btn, row.Container, v.EnabledIf, v.VisibleIf)
	return row.Container
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

// renderAppPickerRows renders a declarative executable/application picker and per-item list
// fully integrated with SettingsManager's baseline registry and the staged save workflow.
func (sm *SettingsManager) renderAppPickerRows(v schema.AppPickerItem) []fyne.CanvasObject {
	items := append([]string{}, v.InitialValue...)
	sm.registry[v.Name] = append([]string{}, items...)
	sm.valueGetters[v.Name] = func() interface{} { return append([]string{}, items...) }
	if v.ApplyFunc != nil {
		sm.applyFuncs[v.Name] = func(val interface{}) {
			if s, ok := val.([]string); ok {
				v.ApplyFunc(s)
			}
		}
	}

	syncDirtyState := func() {
		baseline, _ := sm.registry[v.Name].([]string)
		if !stringSlicesEqual(items, baseline) {
			sm.SetSettingChangedCallback(v.Name, func() {
				if v.ApplyFunc != nil {
					cp := append([]string{}, items...)
					v.ApplyFunc(cp)
				}
			})
		} else {
			sm.RemoveSettingChangedCallback(v.Name)
		}
		if sm.checkAndEnableApply != nil {
			sm.checkAndEnableApply()
		}
	}

	listContainer := newDividedBox()
	var rebuildList func()
	rebuildList = func() {
		var rows []fyne.CanvasObject
		for _, appName := range items {
			currentApp := appName
			delBtn := widget.NewButton(i18n.T("Delete"), func() {
				kept := make([]string, 0, len(items))
				for _, existing := range items {
					if !strings.EqualFold(existing, currentApp) {
						kept = append(kept, existing)
					}
				}
				items = kept
				rebuildList()
				syncDirtyState()
			})
			delBtn.Importance = widget.LowImportance
			lbl := widget.NewLabel(currentApp)
			lbl.Truncation = fyne.TextTruncateEllipsis
			rows = append(rows, newInsetRowWithLeft(lbl, nil, delBtn).Container)
		}
		setDividedRows(listContainer, rows)
	}
	rebuildList()

	// Revert support: restore the staged list to a baseline without staging anything.
	sm.valueSetters[v.Name] = func(val interface{}) {
		if s, ok := val.([]string); ok {
			items = append([]string{}, s...)
			rebuildList()
		}
	}

	btn := widget.NewButtonWithIcon(v.ButtonText, theme.FolderOpenIcon(), func() {
		showOSAppPicker(sm.GetSettingsWindow(), func(appName string, err error) {
			if err != nil || appName == "" {
				return
			}
			appName = strings.TrimSpace(appName)
			if appName == "" {
				return
			}
			fyne.Do(func() {
				for _, existing := range items {
					if strings.EqualFold(existing, appName) {
						return
					}
				}
				items = append(items, appName)
				rebuildList()
				syncDirtyState()
			})
		})
	})

	head := newInsetRow(v.Label, v.Help, nil, btn)
	sm.trackRow(v.Name, head)
	sm.manage(btn, head.Container, v.EnabledIf, v.VisibleIf)
	sm.manage(nil, listContainer, nil, v.VisibleIf)
	sm.allWidgets[v.Name] = btn
	return []fyne.CanvasObject{head.Container, listContainer}
}

// --- Secret (API key) --------------------------------------------------------------------------

// renderSecretRow renders an API key/credential row: entry + Verify & Save when empty,
// masked value + Clear when saved.
func (sm *SettingsManager) renderSecretRow(v schema.SecretItem) fyne.CanvasObject {
	if sm.GetBaseline(v.Name) == nil {
		sm.SeedBaseline(v.Name, v.InitialValue)
	}
	// Live getter returns the registry value (kept in sync by CommitSetting/SeedBaseline) so
	// cross-item dependencies (e.g. Wallhaven username verification) can read the key.
	sm.valueGetters[v.Name] = func() interface{} { return sm.registry[v.Name] }
	if v.ApplyFunc != nil {
		sm.applyFuncs[v.Name] = func(val interface{}) { v.ApplyFunc(val.(string)) }
	}
	sm.notePage(v.Name)

	entry := widget.NewEntry()
	entry.Password = true
	entry.SetPlaceHolder(v.Placeholder)

	saveBtn := widget.NewButtonWithIcon(i18n.T("Verify and save"), theme.ConfirmIcon(), nil)
	saveBtn.Importance = widget.HighImportance
	saveBtn.OnTapped = func() {
		saveBtn.Disable()
		if err := v.OnVerify(entry.Text); err != nil {
			dialog.ShowError(err, sm.GetSettingsWindow())
			saveBtn.Enable()
			return
		}
		// Temporarily read the fresh entry text so CommitSetting picks it up, then restore the
		// registry-based getter.
		sm.valueGetters[v.Name] = func() interface{} { return entry.Text }
		sm.CommitSetting(v.Name)
		sm.valueGetters[v.Name] = func() interface{} { return sm.registry[v.Name] }
	}
	emptyState := container.NewHBox(fixedWidth(entry, secretFieldWidth), saveBtn)

	display := widget.NewEntry()
	display.SetText("********************************")
	display.Disable()
	clearBtn := widget.NewButtonWithIcon(i18n.T("Clear API key"), theme.DeleteIcon(), func() {
		v.OnClear() // OnClear implementations call ResetSettings which already refreshes
	})
	clearBtn.Importance = widget.DangerImportance
	savedState := container.NewHBox(fixedWidth(display, secretFieldWidth), clearBtn)

	stack := container.NewStack(emptyState, savedState)
	refresh := func() {
		baseline, _ := sm.registry[v.Name].(string)
		if baseline == "" {
			emptyState.Show()
			savedState.Hide()
			entry.SetText("") // Clear sensitive input when returning to empty state
			saveBtn.Enable()
		} else {
			emptyState.Hide()
			savedState.Show()
		}
		stack.Refresh()
	}
	sm.RegisterRefreshFunc(refresh)
	refresh()
	sm.allWidgets[v.Name] = stack

	return newInsetRow(v.Label, v.Help, nil, stack).Container
}

// --- OAuth picker ------------------------------------------------------------------------------

// renderOAuthPickerRows renders the OAuth authorize/disconnect row plus the picker block.
func (sm *SettingsManager) renderOAuthPickerRows(v schema.OAuthPickerItem) []fyne.CanvasObject {
	statusLabel := widget.NewLabel(i18n.T("Checking..."))
	statusLabel.SizeName = theme.SizeNameCaptionText
	var connectBtn *widget.Button
	var safeFullRefresh func()

	updateUI := func() {
		isAuth, _ := v.CheckAuthStatus()
		if isAuth {
			statusLabel.SetText(i18n.T("Authorized and ready to select photos"))
			connectBtn.SetText(i18n.T("Disconnect"))
			connectBtn.Importance = widget.DangerImportance
			connectBtn.OnTapped = func() {
				if err := v.OnDisconnect(); err != nil {
					dialog.ShowError(err, sm.GetSettingsWindow())
				}
				if safeFullRefresh != nil {
					safeFullRefresh()
				}
			}
		} else {
			statusLabel.SetText(i18n.T("Not authorized"))
			connectBtn.SetText(i18n.T("Authorize"))
			connectBtn.Importance = widget.MediumImportance
			connectBtn.OnTapped = func() {
				if err := v.OnAuthorize(); err != nil {
					dialog.ShowError(err, sm.GetSettingsWindow())
				} else {
					dialog.ShowInformation(i18n.T("Success"), i18n.T("Spice is now authorized."), sm.GetSettingsWindow())
					if safeFullRefresh != nil {
						safeFullRefresh()
					}
				}
			}
		}
		connectBtn.Refresh()
	}
	connectBtn = widget.NewButton("", nil)

	progressBar := widget.NewProgressBarInfinite()
	progressBar.Hide()
	pickerStatus := widget.NewLabel("")
	pickerStatus.SizeName = theme.SizeNameCaptionText

	addBtn := widget.NewButton(i18n.T("Select photos in browser"), nil)
	cancelBtn := widget.NewButton(i18n.T("Cancel"), nil)
	cancelBtn.Importance = widget.LowImportance
	cancelBtn.Hide()

	var cancelFunc context.CancelFunc
	updateAddBtn := func() {
		isAuth, _ := v.CheckAuthStatus()
		if isAuth {
			addBtn.Enable()
			pickerStatus.SetText("")
		} else {
			addBtn.Disable()
			pickerStatus.SetText(i18n.T("Authorize Spice first."))
		}
	}
	safeFullRefresh = func() {
		updateUI()
		updateAddBtn()
	}
	safeFullRefresh()

	cancelBtn.OnTapped = func() {
		if cancelFunc != nil {
			cancelFunc()
			pickerStatus.SetText(i18n.T("Cancelled."))
		}
		cancelBtn.Hide()
		progressBar.Hide()
		addBtn.Show()
		addBtn.Enable()
	}

	addBtn.OnTapped = func() {
		isAuth, _ := v.CheckAuthStatus()
		if !isAuth {
			dialog.ShowError(fmt.Errorf("please authorize first"), sm.GetSettingsWindow())
			return
		}
		addBtn.Disable()
		addBtn.Hide()
		cancelBtn.Show()
		progressBar.Show()

		updateStatus := func(msg string) {
			fyne.Do(func() { pickerStatus.SetText(msg) })
		}
		updateStatus(i18n.T("Starting a browser session..."))

		ctx, cancel := context.WithCancel(context.Background())
		cancelFunc = cancel

		go func() {
			count, guid, err := v.OnLaunchPicker(ctx, updateStatus)
			fyne.Do(func() {
				cancelBtn.Hide()
				addBtn.Show()
				addBtn.Enable()
				progressBar.Hide()
				pickerStatus.SetText("")
				cancelFunc = nil

				if err != nil {
					if ctx.Err() != context.Canceled {
						dialog.ShowError(err, sm.GetSettingsWindow())
						pickerStatus.SetText(i18n.T("Error: ") + err.Error())
					}
					return
				}
				if count == 0 {
					dialog.ShowError(fmt.Errorf("no photos selected"), sm.GetSettingsWindow())
					return
				}

				urlStr := "googlephotos://" + guid
				defaultDesc := fmt.Sprintf(i18n.T("Collection %s (%d items)"), time.Now().Format("Jan 02 15:04"), count)

				urlEntry := widget.NewEntry()
				urlEntry.SetText(urlStr)
				urlEntry.Disable()
				descEntry := widget.NewEntry()
				descEntry.SetText(defaultDesc)
				activeCheck := widget.NewCheck(i18n.T("Active"), nil)
				activeCheck.SetChecked(true)

				form := container.NewVBox(
					widget.NewLabel(i18n.T("Internal ID")),
					urlEntry,
					widget.NewLabel(i18n.T("Description")),
					descEntry,
					activeCheck,
				)
				d := dialog.NewCustomConfirm(i18n.T("Save collection"), i18n.T("Save"), i18n.T("Cancel"), form,
					func(save bool) {
						if save {
							if err := v.OnSaveCollection(guid, descEntry.Text, activeCheck.Checked); err != nil {
								dialog.ShowError(err, sm.GetSettingsWindow())
							} else {
								sm.SetRefreshFlag("queries")
								sm.GetCheckAndEnableApplyFunc()()
								sm.fullRefresh()
							}
						} else if v.OnCancelCollection != nil {
							v.OnCancelCollection(guid)
						}
					}, sm.GetSettingsWindow())
				d.Resize(fyne.NewSize(500, 350))
				d.Show()
			})
		}()
	}

	authRow := newInsetRow(v.Label, v.Help, statusLabel, connectBtn)
	pickerBlock := blockRow(container.NewVBox(
		container.NewHBox(container.NewStack(addBtn, cancelBtn)),
		progressBar,
		container.New(collapseLayout{}, pickerStatus),
	), rowPadX-rowInnerPad()/2, rowPadY)

	// Register the auth row (already part of the card) so RefreshUI repaints it; never re-parent
	// the rows into a second container.
	sm.allWidgets[v.Name] = authRow.Container
	return []fyne.CanvasObject{authRow.Container, pickerBlock}
}

// --- Query lists -------------------------------------------------------------------------------

type queryRowWidgets struct {
	id           string
	row          *fyne.Container
	dot          *themedRect
	urlLink      *widget.Hyperlink
	activeCheck  *Switch
	editButton   *widget.Button
	deleteButton *widget.Button
}

// queryListWidget renders query rows in a non-scrolling divided box so its MinSize reflects all
// rows and it scrolls naturally as part of the enclosing page VScroll (avoiding 1-row collapse).
type queryListWidget struct {
	widget.BaseWidget
	box    *fyne.Container
	rows   []queryRowWidgets
	syncFn func()
}

func (q *queryListWidget) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(q.box)
}

func (q *queryListWidget) Refresh() {
	if q.syncFn != nil {
		q.syncFn()
	}
	q.BaseWidget.Refresh()
}

// renderQueryListBlock renders a query list and registers it for refreshes.
func (sm *SettingsManager) renderQueryListBlock(v schema.QueryListItem) fyne.CanvasObject {
	list := sm.renderQueryList(v)
	sm.RegisterRefreshFunc(func() { list.Refresh() })
	// Always register in allWidgets so RefreshUI() can repaint the list (e.g., after an async sync
	// operation completes in a provider goroutine).
	widgetID := v.ID
	if widgetID == "" {
		widgetID = fmt.Sprintf("__querylist_%d", len(sm.allWidgets))
	}
	sm.allWidgets[widgetID] = list
	return list
}

// renderQueryList builds the query list widget: one switch row per query with edit (pencil) and
// delete actions. It handles baseline seeding, pending state preservation across refreshes, and
// wires the enable/disable/delete interactions to the SettingsManager.
func (sm *SettingsManager) renderQueryList(v schema.QueryListItem) fyne.CanvasObject {
	ql := &queryListWidget{box: newDividedBox()}
	ql.ExtendBaseWidget(ql)
	page := sm.currentPage

	bindRow := func(rw *queryRowWidgets, query schema.Query) {
		queryKey := query.ID
		if page != "" {
			if _, ok := sm.pageOf[queryKey]; !ok {
				sm.pageOf[queryKey] = page
			}
		}
		sm.rowDots[queryKey] = []*themedRect{rw.dot}

		if v.GetDisplayText != nil {
			rw.urlLink.SetText(v.GetDisplayText(query))
		} else {
			rw.urlLink.SetText(query.Description)
		}
		if v.GetDisplayURL != nil {
			if u := v.GetDisplayURL(query); u != nil {
				rw.urlLink.SetURL(u)
			}
		} else if u, err := url.Parse(query.URL); err == nil {
			rw.urlLink.SetURL(u)
		}

		if sm.GetBaseline(queryKey) == nil {
			sm.SeedBaseline(queryKey, query.Active)
		}

		// Clear OnChanged before syncing state to prevent recursive callback triggers.
		rw.activeCheck.OnChanged = nil
		if sm.HasPendingChange(queryKey) {
			rw.activeCheck.SetChecked(!sm.GetBaseline(queryKey).(bool))
		} else {
			rw.activeCheck.SetChecked(query.Active)
		}
		if sm.HasPendingChange(queryKey) {
			rw.dot.Show()
		} else {
			rw.dot.Hide()
		}

		rw.activeCheck.OnChanged = func(b bool) {
			if b != sm.GetBaseline(queryKey).(bool) {
				sm.SetSettingChangedCallback(queryKey, func() {
					var err error
					if b {
						err = v.EnableQuery(query.ID)
					} else {
						err = v.DisableQuery(query.ID)
					}
					if err != nil {
						utilLog.Printf("Failed to update query status: %v", err)
					} else {
						sm.SeedBaseline(queryKey, b)
						query.Active = b
					}
				})
				sm.SetRefreshFlag(queryKey)
			} else {
				sm.RemoveSettingChangedCallback(queryKey)
				sm.UnsetRefreshFlag(queryKey)
			}
			sm.checkAndEnableApply()
			sm.RefreshUI() // Update UI states (badges, titles) without triggering wallpaper changes
		}

		// Edit: only for editable lists and non-managed rows. Edits commit immediately (like
		// Add/Delete) so they never register a pending change or light up the save pill.
		if v.EditConfig != nil && !query.Managed {
			editCfg := *v.EditConfig
			rw.editButton.OnTapped = func() {
				initialURL := query.URL
				if v.GetDisplayURL != nil {
					if u := v.GetDisplayURL(query); u != nil {
						initialURL = u.String()
					}
				}
				sm.showEditQueryDialog(editCfg, query.ID, initialURL, query.Description, ql.Refresh)
			}
			rw.editButton.Show()
		} else {
			rw.editButton.OnTapped = nil
			rw.editButton.Hide()
		}

		label := i18n.T("Delete")
		if v.DeleteLabel != "" {
			label = v.DeleteLabel
		}
		rw.deleteButton.SetText(label)
		rw.deleteButton.OnTapped = func() {
			confirmTitle := i18n.T("Confirm")
			confirmMsg := i18n.Tf("Delete {{.Description}}?", map[string]any{"Description": query.Description})
			if v.DeleteConfirmMessage != "" {
				confirmMsg = v.DeleteConfirmMessage
			} else if query.Managed || v.ForceActionEnabled {
				if v.DeleteLabel == i18n.T("Clear") {
					confirmMsg = i18n.T("Delete all saved favorites?")
				}
			}
			sm.ShowConfirm(confirmTitle, confirmMsg, func(b bool) {
				if b {
					if query.Active {
						sm.SetRefreshFlag(queryKey)
						sm.checkAndEnableApply()
					}
					if err := v.RemoveQuery(query.ID); err != nil {
						utilLog.Printf("Failed to remove query: %v", err)
					}
					ql.Refresh()
				}
			})
		}
		// Managed/synced queries keep a disabled Delete button for visual consistency.
		if query.Managed && !v.ForceActionEnabled {
			rw.deleteButton.Disable()
		} else {
			rw.deleteButton.Enable()
		}
		rw.deleteButton.Refresh()
	}

	ql.syncFn = func() {
		queries := v.GetQueries()
		sameIDs := len(queries) == len(ql.rows)
		if sameIDs {
			for i, q := range queries {
				if ql.rows[i].id != q.ID {
					sameIDs = false
					break
				}
			}
		}
		if sameIDs {
			for i, q := range queries {
				bindRow(&ql.rows[i], q)
			}
			return
		}

		newRows := make([]queryRowWidgets, len(queries))
		objects := make([]fyne.CanvasObject, len(queries))
		for i, q := range queries {
			urlLink := widget.NewHyperlink(i18n.T("Placeholder"), nil)
			// Truncate long descriptions so they never inflate the panel's (and window's) minimum width.
			urlLink.Truncation = fyne.TextTruncateEllipsis
			activeCheck := NewSwitch(false, nil)
			deleteButton := widget.NewButton(i18n.T("Delete"), func() {})
			deleteButton.Importance = widget.LowImportance
			editButton := widget.NewButtonWithIcon("", theme.DocumentCreateIcon(), nil)
			editButton.Importance = widget.LowImportance
			editButton.Hide()
			row := newInsetRowWithLeft(urlLink, nil, container.NewHBox(editButton, deleteButton, activeCheck))
			newRows[i] = queryRowWidgets{
				id:           q.ID,
				row:          row.Container,
				dot:          row.dot,
				urlLink:      urlLink,
				activeCheck:  activeCheck,
				editButton:   editButton,
				deleteButton: deleteButton,
			}
			bindRow(&newRows[i], q)
			objects[i] = row.Container
		}
		ql.rows = newRows
		setDividedRows(ql.box, objects)
	}

	ql.syncFn()
	return ql
}
