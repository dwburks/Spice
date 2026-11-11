package wallpaper

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/dixieflatline76/Spice/v2/pkg/i18n"
	"github.com/dixieflatline76/Spice/v2/pkg/provider"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
)

// DisplayFit counts how the library's images sort for one display under a fit policy.
type DisplayFit struct {
	Label         string // "Display 1 · 3440×1440"
	Width, Height int
	Fit           int // usable on this display
	WrongShape    int // skipped: aspect ratio outside the Smart fit mode
	TooSmall      int // skipped: smaller than the Minimum image size allows
}

// LibraryFit is the per-display breakdown of the images in rotation under one fit policy.
type LibraryFit struct {
	Displays   []DisplayFit
	Unmeasured int // images whose size is not known yet (not counted above)
}

// snapshotDisplays copies the connected displays, ordered by ID. The lock hierarchy is
// wp.monMu -> mc.mu; both are released before the copy is used. Without monitor actors (the
// plugin is not active yet) there are no displays to report; the OS is not asked, since this
// runs from the preferences window's background work.
func (wp *Plugin) snapshotDisplays() []Monitor {
	wp.monMu.RLock()
	controllers := make(map[int]*MonitorController, len(wp.Monitors))
	for id, mc := range wp.Monitors {
		controllers[id] = mc
	}
	wp.monMu.RUnlock()

	ids := make([]int, 0, len(controllers))
	for id := range controllers {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	displays := make([]Monitor, 0, len(ids))
	for _, id := range ids {
		mc := controllers[id]
		mc.mu.RLock()
		m := mc.Monitor
		mc.mu.RUnlock()
		m.ID = id
		displays = append(displays, m)
	}
	return displays
}

// LibraryFitStats sorts the images in the store that have known dimensions into fit / wrong
// shape / too small for every connected display, under the given policy. The policy is passed in
// rather than read from the saved settings so the Smart fit page can show the effect of settings
// that are staged but not saved yet. It reads, and changes, nothing.
//
// A shape mismatch that virtual framing would rescue counts as fit, matching what the pipeline
// admits. Images without known dimensions are counted in Unmeasured, never guessed.
func (wp *Plugin) LibraryFitStats(policy FitPolicy) LibraryFit {
	displays := wp.snapshotDisplays()
	images := wp.store.List() // a copy; the store lock is already released
	rescued := wp.cfg != nil && wp.cfg.framingRescuesShape()
	return computeLibraryFit(displays, images, policy, rescued)
}

func computeLibraryFit(displays []Monitor, images []provider.Image, policy FitPolicy, shapeRescued bool) LibraryFit {
	out := LibraryFit{Displays: make([]DisplayFit, len(displays))}
	for i, m := range displays {
		w, h := m.Rect.Dx(), m.Rect.Dy()
		out.Displays[i] = DisplayFit{
			Label:  i18n.Tf("Display {{.ID}} · {{.Size}}", map[string]any{"ID": m.ID + 1, "Size": fmt.Sprintf("%d×%d", w, h)}),
			Width:  w,
			Height: h,
		}
	}
	for _, img := range images {
		if img.Width <= 0 || img.Height <= 0 {
			out.Unmeasured++
			continue
		}
		for i := range out.Displays {
			d := &out.Displays[i]
			err := policy.Check(img.Width, img.Height, d.Width, d.Height)
			switch {
			case err == nil:
				d.Fit++
			case errors.Is(err, ErrImageTooSmall):
				d.TooSmall++
			case shapeRescued:
				d.Fit++
			default:
				d.WrongShape++
			}
		}
	}
	return out
}

// Names of the Smart fit page settings the stats and preview read. They are the registry names
// the page's items are registered under, so a getter over the staged values returns them.
const (
	settingSmartFitMode  = "smartFitMode"
	settingMinImageSize  = "minImageSize"
	settingFaceCrop      = "faceCrop"
	settingFaceBoost     = "faceBoost"
	settingLibraryFit    = "libraryFit"
	settingFitPreviewRow = "fitPreview"
)

// stagedInt reads an int setting from a staged-value getter, falling back when it is missing.
func stagedInt(get func(string) interface{}, name string, fallback int) int {
	if v, ok := get(name).(int); ok && v >= 0 {
		return v
	}
	return fallback
}

func stagedBool(get func(string) interface{}, name string, fallback bool) bool {
	if v, ok := get(name).(bool); ok {
		return v
	}
	return fallback
}

// stagedFitSettings reads the Smart fit settings as the page currently shows them (staged, not
// necessarily saved), falling back to the saved value for any the getter does not know.
func (wp *Plugin) stagedFitSettings(get func(string) interface{}) FitSettings {
	return FitSettings{
		Mode:      SmartFitMode(stagedInt(get, settingSmartFitMode, int(wp.cfg.GetSmartFitMode()))),
		MinSize:   MinImageSize(stagedInt(get, settingMinImageSize, int(wp.cfg.GetMinImageSize()))),
		FaceCrop:  stagedBool(get, settingFaceCrop, wp.cfg.GetFaceCropEnabled()),
		FaceBoost: stagedBool(get, settingFaceBoost, wp.cfg.GetFaceBoostEnabled()),
	}
}

// LibraryFitRows is the Compute function of the "Your library" stats: one row per display with
// the fit / wrong shape / too small counts for the staged Smart fit mode and minimum size, plus
// a muted note for images whose size is not known yet.
func (wp *Plugin) LibraryFitRows(ctx context.Context, get func(string) interface{}) []schema.StatRow {
	s := wp.stagedFitSettings(get)
	fit := wp.LibraryFitStats(FitPolicy{Mode: s.Mode, MinSize: s.MinSize, Tuning: wp.cfg.tuningSnapshot()})
	return libraryFitRows(fit)
}

func libraryFitRows(fit LibraryFit) []schema.StatRow {
	rows := make([]schema.StatRow, 0, len(fit.Displays)+1)
	for _, d := range fit.Displays {
		rows = append(rows, schema.StatRow{
			Label: d.Label,
			Value: i18n.Tf("{{.Fit}} fit · {{.Shape}} wrong shape · {{.Small}} too small",
				map[string]any{"Fit": d.Fit, "Shape": d.WrongShape, "Small": d.TooSmall}),
		})
	}
	if fit.Unmeasured > 0 {
		rows = append(rows, schema.StatRow{
			Label: i18n.Tf("{{.Count}} images not measured yet", map[string]any{"Count": fit.Unmeasured}),
			Muted: true,
		})
	}
	return rows
}
