package ui

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/validation"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/dixieflatline76/Spice/v2/asset"
	"github.com/dixieflatline76/Spice/v2/pkg/i18n"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/setting"
	utilLog "github.com/dixieflatline76/Spice/v2/util/log"
)

// SettingsManager handles UI elements for settings.
type SettingsManager struct {
	chgPrefsCallbacks   map[string]func()
	refreshFlags        map[string]bool
	refreshFuncs        []func()
	registry            map[string]interface{}
	valueGetters        map[string]func() interface{}
	checkAndEnableApply func()
	applyButton         *widget.Button
	prefsWindow         fyne.Window
	onSettingsSaved     []func()
	managedWidgets      []managedWidget // Track widgets with EnabledIf conditions
	allWidgets          map[string]fyne.CanvasObject
	statusLabels        map[string]*widget.Label     // NEW: Maps setting names to their status labels
	applyFuncs          map[string]func(interface{}) // Type-safe wrappers for native apply functions
	assetMgr            *asset.Manager

	// Inset-list renderer state.
	stagers         map[string]func(interface{}) // Re-stage a switch after programmatic SetValue
	valueSetters    map[string]func(interface{}) // Custom SetValue handlers (e.g. app picker lists)
	afterSetHooks   map[string]func(interface{}) // Run after a programmatic SetValue (e.g. preset selects)
	rowDots         map[string][]*themedRect     // Setting name -> its rows' "changed" dots
	pageOf          map[string]string            // Setting name -> preferences page ID
	currentPage     string                       // Page ID being rendered (set by the nav renderer)
	dirtyObservers  []func()                     // Notified after dirty state changes (pill, sidebar dots)
	pageTopInset    float32                      // Page padding used by RenderSchema
	pageBottomInset float32
	pagePadX        float32
	pageMaxWidth    float32
	scrolls         []*container.Scroll // Rendered page scrolls (re-laid out when rows show/hide)

	// queueDialogs holds back dialogs requested while a brand-new preferences window is still
	// being built (no content/size yet). Fyne permanently clamps an overlay shown on a not-yet-sized
	// canvas to that tiny size, so CreatePreferencesWindow shows them via showQueuedDialogs() after Show().
	queueDialogs  bool
	queuedDialogs []func()
}

type managedWidget struct {
	widget    fyne.CanvasObject
	enabledIf func() bool
	visibleIf func() bool
}

type textEntrySettingConfig struct {
	Name               string
	InitialValue       string
	PlaceHolder        string
	Validator          fyne.StringValidator
	OnChanged          func(string)
	PostValidateCheck  func(string) error
	ApplyFunc          func(string)
	NeedsRefresh       bool
	SkipApply          bool
	DisplayStatus      bool
	IsPassword         bool
	IsNumeric          bool
	EnabledIf          func() bool
	VisibleIf          func() bool
	ValidationDebounce time.Duration
}

// NewSettingsManager creates a new SettingsManager.
func NewSettingsManager(window fyne.Window) setting.SettingsManager {
	cpcs := make(map[string]func())
	rns := make(map[string]bool)
	rfs := make([]func(), 0)

	sm := &SettingsManager{
		chgPrefsCallbacks:   cpcs,
		refreshFlags:        rns,
		refreshFuncs:        rfs,
		registry:            make(map[string]interface{}),
		valueGetters:        make(map[string]func() interface{}),
		checkAndEnableApply: nil,
		applyButton:         nil,
		prefsWindow:         window,
		onSettingsSaved:     make([]func(), 0),
		managedWidgets:      make([]managedWidget, 0),
		allWidgets:          make(map[string]fyne.CanvasObject),
		statusLabels:        make(map[string]*widget.Label),
		applyFuncs:          make(map[string]func(interface{})),
		assetMgr:            asset.NewManager(),
		stagers:             make(map[string]func(interface{})),
		valueSetters:        make(map[string]func(interface{})),
		afterSetHooks:       make(map[string]func(interface{})),
		rowDots:             make(map[string][]*themedRect),
		pageOf:              make(map[string]string),
	}

	sm.applyButton = createApplyButton(sm)
	sm.checkAndEnableApply = func() {
		// Evaluate UI dependencies first
		sm.refreshWidgetStates()

		if sm.IsDirty() {
			sm.applyButton.Enable() // Enable if changes or refresh needed
		} else {
			sm.applyButton.Disable() // Otherwise, disable
		}
		sm.applyButton.Refresh()
		sm.syncDirtyIndicators()
	}

	return sm
}

// SeedBaseline seeds the initial state for a setting to track changes.
func (sm *SettingsManager) SeedBaseline(name string, val interface{}) {
	sm.registry[name] = val
}

// OpenURL opens the specified URL in the system's default browser.
func (sm *SettingsManager) OpenURL(u string) {
	if parsed, err := url.Parse(u); err == nil {
		_ = fyne.CurrentApp().OpenURL(parsed)
	}
}

// refreshWidgetStates evaluates all EnabledIf and VisibleIf conditions and updates widget states.
func (sm *SettingsManager) refreshWidgetStates() {
	visibilityChanged := false
	for _, mw := range sm.managedWidgets {
		// Handle Visibility
		if mw.visibleIf != nil {
			want := mw.visibleIf()
			if want != mw.widget.Visible() {
				visibilityChanged = true
			}
			if want {
				mw.widget.Show()
			} else {
				mw.widget.Hide()
			}
		}

		// Handle Enabling (requires fyne.Disableable)
		if mw.enabledIf != nil {
			if d, ok := mw.widget.(fyne.Disableable); ok {
				if mw.enabledIf() {
					d.Enable()
				} else {
					d.Disable()
				}
			}
		}
	}
	// Rows that appeared or disappeared change their card's height: re-lay out the pages.
	if visibilityChanged {
		for _, s := range sm.scrolls {
			s.Content.Refresh()
			s.Refresh()
		}
	}
}

// GetValue returns the live/current value for a setting from its valueGetter.
func (sm *SettingsManager) GetValue(name string) interface{} {
	val, ok := sm.valueGetters[name]
	if !ok || val == nil {
		return nil
	}
	return val()
}

// SetValue programmatically updates the live value of a setting (UI synchronization). It never
// calls the item's own OnChanged hook; for switches it re-evaluates the staged state against the
// baseline exactly as the old checkbox did, so a SetValue back to the baseline leaves it clean.
func (sm *SettingsManager) SetValue(name string, val interface{}) {
	if _, ok := sm.allWidgets[name]; !ok {
		if _, ok := sm.valueSetters[name]; !ok {
			return
		}
	}
	fyne.Do(func() { sm.setValueNow(name, val) })
}

// setValueNow is SetValue on the calling (UI) goroutine.
func (sm *SettingsManager) setValueNow(name string, val interface{}) {
	if set, ok := sm.valueSetters[name]; ok {
		set(val)
		sm.refreshWidgetStates()
		return
	}
	w, ok := sm.allWidgets[name]
	if !ok {
		return
	}
	switch v := w.(type) {
	case *Switch:
		if b, ok := val.(bool); ok {
			v.SetChecked(b)
			if stage, ok := sm.stagers[name]; ok {
				stage(b)
				sm.checkAndEnableApply()
			}
		}
	case *widget.Check:
		if b, ok := val.(bool); ok {
			v.SetChecked(b)
			v.Refresh()
		}
	case *widget.Entry:
		if s, ok := val.(string); ok {
			v.SetText(s)
			v.Refresh()
		}
		if hook, ok := sm.afterSetHooks[name]; ok {
			hook(val)
		}
	case *widget.Select:
		if i, ok := val.(int); ok {
			v.SetSelectedIndex(i)
			v.Refresh()
		} else if s, ok := val.(string); ok {
			v.SetSelected(s)
			v.Refresh()
		}
	case *widget.Slider:
		if f, ok := val.(float64); ok {
			v.SetValue(f)
		}
	}
	sm.refreshWidgetStates()
}

// IsDirty reports whether anything is staged (pending changes or pending refreshes).
func (sm *SettingsManager) IsDirty() bool {
	return len(sm.refreshFlags) > 0 || len(sm.chgPrefsCallbacks) > 0
}

// DirtyNames returns the sorted names of every staged change and pending refresh.
func (sm *SettingsManager) DirtyNames() []string {
	seen := make(map[string]bool, len(sm.chgPrefsCallbacks)+len(sm.refreshFlags))
	for n := range sm.chgPrefsCallbacks {
		seen[n] = true
	}
	for n := range sm.refreshFlags {
		seen[n] = true
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// PageOf returns the preferences page a setting was rendered on ("" if unknown).
func (sm *SettingsManager) PageOf(name string) string {
	return sm.pageOf[name]
}

// AddDirtyObserver registers a callback run after every dirty-state evaluation.
func (sm *SettingsManager) AddDirtyObserver(fn func()) {
	sm.dirtyObservers = append(sm.dirtyObservers, fn)
}

// syncDirtyIndicators shows the changed-row dots of staged settings and notifies observers.
func (sm *SettingsManager) syncDirtyIndicators() {
	for name, dots := range sm.rowDots {
		pending := sm.HasPendingChange(name)
		for _, d := range dots {
			if pending {
				d.Show()
			} else {
				d.Hide()
			}
		}
	}
	for _, fn := range sm.dirtyObservers {
		fn()
	}
}

// RevertPending restores every staged widget to its registry baseline (via SetValue) and clears
// all staged callbacks and refresh flags. Nothing is written to Config.
func (sm *SettingsManager) RevertPending() {
	names := make([]string, 0, len(sm.chgPrefsCallbacks))
	for n := range sm.chgPrefsCallbacks {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if base, ok := sm.registry[n]; ok {
			if s, isSlice := base.([]string); isSlice {
				base = append([]string{}, s...)
			}
			sm.setValueNow(n, base)
		}
	}
	sm.chgPrefsCallbacks = make(map[string]func())
	sm.refreshFlags = make(map[string]bool)

	// Query lists re-bind their switches from the baseline now that nothing is pending.
	sm.refreshWidgetStates()
	for _, w := range sm.allWidgets {
		if ql, ok := w.(*queryListWidget); ok {
			ql.Refresh()
		}
	}
	sm.checkAndEnableApply()
}

// GetBaseline returns the initial state for a setting.
func (sm *SettingsManager) GetBaseline(name string) interface{} {
	return sm.registry[name]
}

// HasPendingChange returns true if the user has toggled a setting but not yet applied.
func (sm *SettingsManager) HasPendingChange(name string) bool {
	_, exists := sm.chgPrefsCallbacks[name]
	return exists
}

// CreateApplyButton is a helper function that creates and sets up the Apply Changes button.
func createApplyButton(sm *SettingsManager) *widget.Button {
	var applyButton *widget.Button
	applyButton = widget.NewButton(i18n.T("Save changes"), func() {
		originalText := applyButton.Text
		sm.applyButton.Disable()
		sm.applyButton.SetText(i18n.T("Saving..."))
		sm.applyButton.Refresh()
		fyne.Do(func() {
			defer func() {

				// Notify listeners that settings have been saved (e.g. refresh UI counters)
				for _, cb := range sm.onSettingsSaved {
					cb()
				}

				// Disable the Apply button after saving
				if sm.applyButton != nil {
					sm.applyButton.Disable()
				}
				sm.applyButton.SetText(originalText)
				sm.applyButton.Refresh()
				sm.syncDirtyIndicators()
			}()

			if len(sm.chgPrefsCallbacks) > 0 {
				for name, callback := range sm.chgPrefsCallbacks {
					callback()
					// Update registry baseline so subsequent changes are compared to NEW state
					if getter, ok := sm.valueGetters[name]; ok {
						sm.registry[name] = getter()
					}
				}
				sm.chgPrefsCallbacks = make(map[string]func())
			}

			// Conditional refresh based on dirty flags (evaluated during ApplyFuncs above)
			if len(sm.refreshFlags) > 0 {
				sm.fullRefresh()
				sm.refreshFlags = make(map[string]bool)
			}
		})
	})
	applyButton.Importance = widget.HighImportance
	applyButton.Disable()
	return applyButton
}

// GetApplySettingsButton returns the staged-save button ("Save changes" in the floating save pill).
func (sm *SettingsManager) GetApplySettingsButton() *widget.Button {
	return sm.applyButton
}

func (sm *SettingsManager) handleTextEntryChanged(s string, cfg *textEntrySettingConfig, entry *widget.Entry, statusLabel *widget.Label, debounceTimer **time.Timer) {
	if *debounceTimer != nil {
		(*debounceTimer).Stop()
	}

	var entryErr error
	if cfg.Validator != nil {
		entryErr = entry.Validate()
	}

	if entryErr != nil {
		statusLabel.SetText(entryErr.Error())
		statusLabel.Importance = widget.DangerImportance
		sm.RemoveSettingChangedCallback(cfg.Name)
		if cfg.NeedsRefresh {
			sm.UnsetRefreshFlag(cfg.Name)
		}
		sm.GetCheckAndEnableApplyFunc()()
		statusLabel.Refresh()
		if cfg.OnChanged != nil {
			cfg.OnChanged(s)
		}
		return
	}

	// Validation passed (or none), clear status and importance while waiting/running post-check
	statusLabel.SetText("")
	statusLabel.Importance = widget.LowImportance
	statusLabel.Refresh()

	runPostCheck := func(val string) {
		sm.runTextEntryPostCheck(val, cfg, entry, statusLabel)
	}

	if cfg.ValidationDebounce > 0 {
		*debounceTimer = time.AfterFunc(cfg.ValidationDebounce, func() {
			runPostCheck(s)
		})
	} else {
		runPostCheck(s)
	}

	if cfg.OnChanged != nil {
		cfg.OnChanged(s)
	}
}

func (sm *SettingsManager) runTextEntryPostCheck(val string, cfg *textEntrySettingConfig, entry *widget.Entry, statusLabel *widget.Label) {
	var postErr error
	if cfg.PostValidateCheck != nil {
		postErr = cfg.PostValidateCheck(val)
	}

	fyne.Do(func() {
		// Ensure the value hasn't changed since we started this check
		if entry.Text != val {
			return
		}

		if postErr != nil {
			statusLabel.SetText(postErr.Error())
			statusLabel.Importance = widget.DangerImportance
			sm.RemoveSettingChangedCallback(cfg.Name)
			if cfg.NeedsRefresh {
				sm.UnsetRefreshFlag(cfg.Name)
			}
		} else {
			if cfg.DisplayStatus && val != "" {
				statusLabel.SetText(fmt.Sprintf("%s OK", cfg.Name))
				statusLabel.Importance = widget.SuccessImportance
			} else {
				statusLabel.SetText("")
				statusLabel.Importance = widget.LowImportance
			}

			baseline, _ := sm.registry[cfg.Name].(string)
			if val != baseline && !cfg.SkipApply {
				sm.SetSettingChangedCallback(cfg.Name, func() {
					enteredTxt := entry.Text
					if enteredTxt != sm.registry[cfg.Name].(string) {
						cfg.ApplyFunc(enteredTxt)
					}
				})
				if cfg.NeedsRefresh {
					sm.SetRefreshFlag(cfg.Name)
				}
			} else {
				sm.RemoveSettingChangedCallback(cfg.Name)
				if cfg.NeedsRefresh {
					sm.UnsetRefreshFlag(cfg.Name)
				}
			}
		}
		statusLabel.Refresh()
		sm.GetCheckAndEnableApplyFunc()()
	})
}

// SetSettingChangedCallback sets a callback function to be called when a setting changes.
func (sm *SettingsManager) SetSettingChangedCallback(settingName string, callback func()) {
	sm.chgPrefsCallbacks[settingName] = callback
}

// RemoveSettingChangedCallback removes a callback function associated with a specific setting.
func (sm *SettingsManager) RemoveSettingChangedCallback(settingName string) {
	delete(sm.chgPrefsCallbacks, settingName)
}

// forgetQueryKey drops every piece of staged UI state keyed by a query ID that no longer
// exists (its URL was edited, which gives the query a new ID), then re-evaluates Apply.
func (sm *SettingsManager) forgetQueryKey(id string) {
	delete(sm.chgPrefsCallbacks, id)
	delete(sm.refreshFlags, id)
	delete(sm.registry, id)
	delete(sm.rowDots, id)
	delete(sm.pageOf, id)
	sm.checkAndEnableApply()
}

// SetRefreshFlag sets a flag to indicate that a specific setting needs a refresh.
func (sm *SettingsManager) SetRefreshFlag(settingName string) {
	sm.refreshFlags[settingName] = true
}

// UnsetRefreshFlag removes the refresh flag for a specific setting.
func (sm *SettingsManager) UnsetRefreshFlag(settingName string) {
	delete(sm.refreshFlags, settingName)
}

// RegisterOnSettingsSaved registers a function to be called after settings are applied.
func (sm *SettingsManager) RegisterOnSettingsSaved(callback func()) {
	sm.onSettingsSaved = append(sm.onSettingsSaved, callback)
}

// RegisterRefreshFunc registers a function to be called when the settings need to be refreshed.
// This should be done by each plugin that needs to perform some action after settings changes.
// Like refreshing all wallpaper images for example.
func (sm *SettingsManager) RegisterRefreshFunc(refreshFunc func()) {
	sm.refreshFuncs = append(sm.refreshFuncs, refreshFunc)
}

// GetSettingsWindow returns the window associated with the SettingsManager.
func (sm *SettingsManager) GetSettingsWindow() fyne.Window {
	return sm.prefsWindow
}

// GetCheckAndEnableApplyFunc returns the check and enable apply function for the SettingsManager.
func (sm *SettingsManager) GetCheckAndEnableApplyFunc() func() {
	return sm.checkAndEnableApply
}

// Refresh performs a FULL refresh cycle: UI state evaluation, registered callbacks, and widget repaints.
//
// Steps:
//  1. Evaluates all EnabledIf/VisibleIf predicates on managed widgets.
//  2. Runs ALL registered refresh functions (via RegisterRefreshFunc). These may include
//     wallpaper rotation triggers, collection sync operations, accordion title recalculations,
//     and other engine-level side effects.
//  3. Calls .Refresh() on every widget in the allWidgets registry.
//
// Use Refresh() when a state change has been COMMITTED (e.g., after Apply, after sync completes,
// after a username verification resets state). Do NOT use it for pending/uncommitted user interactions
// like checkbox toggles — use RefreshUI() instead to avoid triggering wallpaper changes.
func (sm *SettingsManager) fullRefresh() {
	// 1. Evaluate all UI dependencies (EnabledIf/VisibleIf)
	sm.refreshWidgetStates()

	// 2. Run registered refresh callbacks (may have engine-level side effects)
	for _, rf := range sm.refreshFuncs {
		rf()
	}

	// 3. Repaint all managed widgets
	for _, w := range sm.allWidgets {
		w.Refresh()
	}
	sm.syncDirtyIndicators()
}

// RefreshUI performs a UI-ONLY refresh: state evaluation and widget repaints,
// WITHOUT running registered refresh callbacks.
//
// Steps:
//  1. Evaluates all EnabledIf/VisibleIf predicates on managed widgets.
//  2. Calls .Refresh() on every widget in the allWidgets registry.
//
// This is safe to call from interactive handlers (checkbox toggles, text edits)
// because it will NOT trigger engine-level side effects like wallpaper rotation or
// collection syncs. It only updates the visual state of the UI (e.g., enabling/disabling
// dependent fields, updating accordion titles).
//
// Thread-safety: RefreshUI may be called from any goroutine (e.g., a provider's background
// sync goroutine). The work is always marshalled onto the Fyne main thread via fyne.Do,
// because the query list rebuilds its rows (mutating container objects) during Refresh.
func (sm *SettingsManager) RefreshUI() {
	fyne.Do(func() {
		// 1. Evaluate all UI dependencies (EnabledIf/VisibleIf)
		sm.refreshWidgetStates()

		// 2. Repaint all managed widgets (no registered callbacks)
		for _, w := range sm.allWidgets {
			w.Refresh()
		}
		sm.syncDirtyIndicators()
	})
}

// CommitSetting atomically reads the current UI value, applies it to the native setter, and updates the baseline.
func (sm *SettingsManager) CommitSetting(name string) {
	val := sm.GetValue(name)
	if val == nil {
		return
	}

	if apply, ok := sm.applyFuncs[name]; ok {
		apply(val)
	}

	sm.SeedBaseline(name, val)
	sm.RemoveSettingChangedCallback(name)

	// Since Commit is usually triggered by a successful verification/on-press,
	// we always refresh to catch the visibility state change.
	sm.fullRefresh()
	sm.checkAndEnableApply()
}

// ResetSettings atomically clears multiple settings, updates native getters, and resyncs baselines.
func (sm *SettingsManager) ResetSettings(resets ...setting.SettingReset) {
	for _, r := range resets {
		// 1. Update native Fyne preference
		if apply, ok := sm.applyFuncs[r.Name]; ok {
			apply(r.Value)
		}
		// 2. Update Baseline
		sm.SeedBaseline(r.Name, r.Value)
		// 3. Update UI
		sm.SetValue(r.Name, r.Value)
		// 4. Remove pending
		sm.RemoveSettingChangedCallback(r.Name)
	}

	sm.fullRefresh()
	sm.checkAndEnableApply()
}

// SetSettingStatus programmatically updates a setting's status label (thread-safe).
func (sm *SettingsManager) SetSettingStatus(name string, message string, importance schema.Importance) {
	label, ok := sm.statusLabels[name]
	if !ok || label == nil {
		return
	}

	fyneImportance := widget.LowImportance
	switch importance {
	case schema.ImportanceHigh:
		fyneImportance = widget.HighImportance
	case schema.ImportanceMedium:
		fyneImportance = widget.MediumImportance
	case schema.ImportanceLow:
		fyneImportance = widget.LowImportance
	case schema.ImportanceSuccess:
		fyneImportance = widget.SuccessImportance
	case schema.ImportanceDanger:
		fyneImportance = widget.DangerImportance
	}

	fyne.Do(func() {
		label.SetText(message)
		label.Importance = fyneImportance
		label.Refresh()
	})
}

func (sm *SettingsManager) mapImportance(i schema.Importance) widget.Importance {
	switch i {
	case schema.ImportanceHigh:
		return widget.HighImportance
	case schema.ImportanceMedium:
		return widget.MediumImportance
	case schema.ImportanceLow:
		return widget.LowImportance
	case schema.ImportanceSuccess:
		return widget.SuccessImportance
	case schema.ImportanceDanger:
		return widget.DangerImportance
	default:
		return widget.MediumImportance
	}
}

// mapImportanceDefault maps an importance, using def when none is set.
func (sm *SettingsManager) mapImportanceDefault(i schema.Importance, def widget.Importance) widget.Importance {
	if i == "" {
		return def
	}
	return sm.mapImportance(i)
}

func (sm *SettingsManager) getIconResource(name string) fyne.Resource {
	switch strings.ToLower(name) {
	case "help", "question":
		return theme.HelpIcon()
	case "map", "mappin":
		return theme.InfoIcon()
	case "home", "browser":
		return theme.HomeIcon()
	case "heart", "favorite":
		return theme.ConfirmIcon()
	case "info":
		return theme.InfoIcon()
	case "add", "plus":
		return theme.ContentAddIcon()
	case "settings", "prefs":
		return theme.SettingsIcon()
	case "search":
		return theme.SearchIcon()
	default:
		return nil
	}
}

// ShowError displays a modal error dialog to the user.
func (sm *SettingsManager) ShowError(err error) {
	dialog.ShowError(err, sm.prefsWindow)
}

// ShowConfirm displays a modal confirmation dialog to the user.
func (sm *SettingsManager) ShowConfirm(title, message string, callback func(bool)) {
	d := dialog.NewConfirm(title, message, callback, sm.prefsWindow)
	d.Show()
}

// ShowAddQueryDialog opens the modal for adding image queries.
func (sm *SettingsManager) ShowAddQueryDialog(cfg schema.AddQueryConfig, initialURL, initialDesc string, onAdded func()) {
	sm.showQueryDialog(cfg, "", initialURL, initialDesc, onAdded)
}

// showEditQueryDialog opens the same modal prefilled with an existing query; Save commits via cfg.UpdateHandler.
func (sm *SettingsManager) showEditQueryDialog(cfg schema.AddQueryConfig, queryID, initialURL, initialDesc string, onSaved func()) {
	sm.showQueryDialog(cfg, queryID, initialURL, initialDesc, onSaved)
}

// showQueryDialog implements both the add dialog (editID == "") and the edit dialog (editID != "").
// Edit mode hides the Active checkbox and commits via cfg.UpdateHandler immediately; it never stages
// anything for "Apply Changes".
func (sm *SettingsManager) showQueryDialog(cfg schema.AddQueryConfig, editID, initialURL, initialDesc string, onDone func()) {
	editing := editID != ""
	if sm.queueDialogs {
		utilLog.Debugf("showQueryDialog: window not shown yet, queueing dialog for URL: %s", initialURL)
		sm.queuedDialogs = append(sm.queuedDialogs, func() {
			sm.showQueryDialog(cfg, editID, initialURL, initialDesc, onDone)
		})
		return
	}
	utilLog.Debugf("showQueryDialog: Triggered with URL: %s", initialURL)

	urlEntry := widget.NewEntry()
	urlEntry.SetPlaceHolder(cfg.URLPlaceholder)
	urlEntry.SetText(initialURL)
	if cfg.URLValidator != "" {
		urlEntry.Validator = validation.NewRegexp(cfg.URLValidator, cfg.URLErrorMsg)
	}

	descEntry := widget.NewEntry()
	descEntry.SetPlaceHolder(cfg.DescPlaceholder)
	descEntry.SetText(initialDesc)
	if cfg.DescValidator != "" {
		descEntry.Validator = validation.NewRegexp(cfg.DescValidator, cfg.DescErrorMsg)
	}

	formStatus := widget.NewLabel("")
	activeBool := widget.NewCheck(i18n.T("Active"), nil)
	activeBool.SetChecked(true)

	cancelButton := widget.NewButton(i18n.T("Cancel"), nil)
	saveButton := widget.NewButton(i18n.T("Save"), nil)

	if initialURL == "" {
		saveButton.Disable()
	} else {
		saveButton.Enable()
	}

	// Validation logic
	formValidator := func(who *widget.Entry) bool {
		if urlEntry.Text == "" || descEntry.Text == "" {
			return false
		}
		if err := urlEntry.Validate(); err != nil {
			if who == urlEntry {
				formStatus.SetText(err.Error())
				formStatus.Importance = widget.DangerImportance
				formStatus.Refresh()
			}
			return false
		}
		if err := descEntry.Validate(); err != nil {
			if who == descEntry {
				formStatus.SetText(err.Error())
				formStatus.Importance = widget.DangerImportance
				formStatus.Refresh()
			}
			return false
		}
		if cfg.ValidateFunc != nil {
			if err := cfg.ValidateFunc(urlEntry.Text, descEntry.Text); err != nil {
				if who == urlEntry || (who == descEntry && urlEntry.Text != "") {
					formStatus.SetText(err.Error())
					formStatus.Importance = widget.DangerImportance
					formStatus.Refresh()
				}
				return false
			}
		}
		formStatus.SetText(i18n.T("Looks good"))
		formStatus.Importance = widget.SuccessImportance
		formStatus.Refresh()
		return true
	}

	// Entry listeners with max length enforcement
	const maxURLLen = 500
	const maxDescLen = 100
	newChecker := func(entry *widget.Entry, maxLen int) func(string) {
		return func(s string) {
			if len(s) > maxLen {
				entry.SetText(s[:maxLen])
				return
			}
			if formValidator(entry) {
				saveButton.Enable()
			} else {
				saveButton.Disable()
			}
		}
	}
	urlEntry.OnChanged = newChecker(urlEntry, maxURLLen)
	descEntry.OnChanged = newChecker(descEntry, maxDescLen)

	// Build dialog layout
	inputContainer := container.NewVBox()
	if cfg.Description != "" {
		inputContainer.Add(widget.NewLabel(cfg.Description))
	}
	inputContainer.Add(sm.CreateSettingTitleLabel(i18n.T("URL or search term")))
	inputContainer.Add(urlEntry)
	inputContainer.Add(sm.CreateSettingTitleLabel(i18n.T("Description")))
	inputContainer.Add(descEntry)
	inputContainer.Add(formStatus)
	if !editing {
		inputContainer.Add(activeBool)
	}
	inputContainer.Add(widget.NewSeparator())
	inputContainer.Add(container.NewHBox(cancelButton, layout.NewSpacer(), saveButton))

	d := dialog.NewCustomWithoutButtons(cfg.Title, inputContainer, sm.prefsWindow)
	d.Resize(fyne.NewSize(600, 200))

	saveButton.OnTapped = func() {
		if cfg.ValidateFunc != nil {
			if err := cfg.ValidateFunc(urlEntry.Text, descEntry.Text); err != nil {
				formStatus.SetText(err.Error())
				formStatus.Importance = widget.DangerImportance
				formStatus.Refresh()
				return
			}
		}

		if editing {
			if cfg.UpdateHandler == nil {
				return
			}
			newID, err := cfg.UpdateHandler(editID, descEntry.Text, urlEntry.Text)
			if err != nil {
				formStatus.SetText(err.Error())
				formStatus.Importance = widget.DangerImportance
				formStatus.Refresh()
				return
			}
			if newID != "" && newID != editID {
				// A URL edit re-keys the query: drop UI state keyed by the old ID so no
				// stale staged toggle is applied to a query that no longer exists. The
				// refreshed row seeds its baseline from the edited query.
				sm.forgetQueryKey(editID)
			}
			if onDone != nil {
				onDone()
			}
			d.Hide()
			return
		}

		newID, err := cfg.AddHandler(descEntry.Text, urlEntry.Text, activeBool.Checked)
		if err != nil {
			formStatus.SetText(err.Error())
			formStatus.Importance = widget.DangerImportance
			formStatus.Refresh()
			return
		}

		if onDone != nil {
			onDone()
		}

		if activeBool.Checked {
			sm.SetRefreshFlag(newID)
			sm.checkAndEnableApply()
		}

		d.Hide()
	}

	cancelButton.OnTapped = func() {
		d.Hide()
	}

	utilLog.Debug("showQueryDialog: Calling d.Show()")
	d.Show()
}

// showQueuedDialogs stops queueing and shows any dialogs requested while the window was being built.
// Must be called once the preferences window has its content, size and has been shown.
func (sm *SettingsManager) showQueuedDialogs() {
	sm.queueDialogs = false
	queued := sm.queuedDialogs
	sm.queuedDialogs = nil
	for _, show := range queued {
		show()
	}
}

func (sm *SettingsManager) resolveIcon(name string) fyne.Resource {
	if name == "" {
		return nil
	}
	switch name {
	case "settings":
		return theme.SettingsIcon()
	case "color_palette":
		return theme.ColorPaletteIcon()
	case "grid":
		return theme.GridIcon()
	case "folder":
		return theme.FolderIcon()
	case "history":
		return theme.HistoryIcon()
	case "fullscreen":
		return theme.ViewFullScreenIcon()
	case "image":
		return theme.FileImageIcon()
	case "check":
		return theme.CheckButtonCheckedIcon()
	case "computer":
		return theme.ComputerIcon()
	case "refresh":
		return theme.ViewRefreshIcon()
	case "pause":
		return theme.MediaPauseIcon()
	}
	if sm.assetMgr != nil {
		if res, err := sm.assetMgr.GetIcon(name); err == nil && res != nil {
			return res
		}
	}
	return nil
}
