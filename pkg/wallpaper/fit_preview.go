package wallpaper

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"strings"

	"github.com/disintegration/imaging"
	"github.com/dixieflatline76/Spice/v2/pkg/i18n"
	"github.com/dixieflatline76/Spice/v2/pkg/provider"
	"github.com/dixieflatline76/Spice/v2/pkg/ui/schema"
	"github.com/dixieflatline76/Spice/v2/util/log"
)

// Thumbnail limits for the Smart fit preview. Previews run the real processor on a reduced copy
// of the master, so they stay cheap enough to refresh while the user clicks through options.
const (
	previewThumbMaxW  = 440  // widest thumbnail
	previewThumbMaxH  = 300  // tallest thumbnail
	previewSourceEdge = 1024 // the master is reduced to fit this box before it is processed
)

// FitSettings are the Smart fit settings a preview is rendered with. They are passed in
// explicitly; nothing is read from or written to the saved Config.
type FitSettings struct {
	Mode      SmartFitMode
	MinSize   MinImageSize
	FaceCrop  bool
	FaceBoost bool
}

// Describe names the settings for a thumbnail caption, e.g. "Flexibility · Face boost".
func (s FitSettings) Describe() string {
	return describeFit(s.Mode, s.FaceCrop, s.FaceBoost)
}

func describeFit(mode SmartFitMode, faceCrop, faceBoost bool) string {
	parts := []string{mode.String()}
	if mode != SmartFitOff {
		if faceCrop {
			parts = append(parts, i18n.T("Face crop"))
		} else if faceBoost {
			parts = append(parts, i18n.T("Face boost"))
		}
	}
	return strings.Join(parts, " · ")
}

// describeFlags names the settings an image was processed with, from its stamped flags.
func describeFlags(flags map[string]bool) string {
	// The mode comes from the Fit* flags (derived from the saved mode). "SmartFit" is a separate
	// legacy flag and says nothing about which mode was used.
	mode := SmartFitOff
	switch {
	case flags["FitFlexibility"]:
		mode = SmartFitAggressive
	case flags["FitQuality"]:
		mode = SmartFitNormal
	}
	return describeFit(mode, flags["FaceCrop"], flags["FaceBoost"])
}

// FitPreview is the primary display's current wallpaper shown three ways. Images are small
// thumbnails; a nil image comes with a note saying why it is missing.
type FitPreview struct {
	HasWallpaper bool
	Display      string

	Original     image.Image
	OriginalNote string

	Current      image.Image // the file on disk for the saved settings
	CurrentLabel string      // the settings it was made with
	CurrentNote  string

	Staged      image.Image // the real processor run with the staged settings
	StagedLabel string
	StagedNote  string
}

// previewTarget is everything the preview needs, copied under the proper locks.
type previewTarget struct {
	monitor Monitor
	img     provider.Image
}

// snapshotPrimaryTarget copies the primary display and the image on it. Lock order
// wp.monMu -> mc.mu; both are released before anything is decoded.
func (wp *Plugin) snapshotPrimaryTarget() (previewTarget, bool) {
	wp.monMu.RLock()
	var best *MonitorController
	bestID := 0
	for id, mc := range wp.Monitors {
		if best == nil || id < bestID {
			best, bestID = mc, id
		}
	}
	wp.monMu.RUnlock()
	if best == nil {
		return previewTarget{}, false
	}
	best.mu.RLock()
	t := previewTarget{monitor: best.Monitor}
	if best.State != nil {
		t.img = best.State.CurrentImage
	}
	best.mu.RUnlock()
	t.monitor.ID = bestID
	return t, t.img.ID != ""
}

// previewProcessorBase returns the live processor's OS handle and face detector, so a private
// processor can reuse the loaded model. ok is false when the processor is not the real one.
func (wp *Plugin) previewProcessorBase() (*SmartImageProcessor, bool) {
	switch p := wp.imgProcessor.(type) {
	case *VirtualFramer:
		sip, ok := p.next.(*SmartImageProcessor)
		return sip, ok
	case *SmartImageProcessor:
		return p, true
	}
	return nil, false
}

// previewConfig builds a private Config for the given settings on in-memory preferences. Only
// the values the fit code reads are filled in; the shared Config is only read (copied from).
func (wp *Plugin) previewConfig(s FitSettings) *Config {
	prefs := newMemPreferences()
	prefs.SetInt(SmartFitModePrefKey, int(s.Mode))
	prefs.SetBool(SmartFitPrefKey, s.Mode != SmartFitOff)
	prefs.SetInt(MinImageSizePrefKey, int(s.MinSize))
	prefs.SetBool(FaceCropPrefKey, s.FaceCrop)
	prefs.SetBool(FaceBoostPrefKey, s.FaceBoost)
	return &Config{Preferences: prefs, Tuning: wp.cfg.tuningSnapshot()}
}

// FitPreviewFor renders the primary display's current wallpaper as: the original, what is on
// disk for the saved settings, and the real processor's result for the staged settings. It
// blocks while it decodes and processes (run it off the UI thread) and honors ctx.
func (wp *Plugin) FitPreviewFor(ctx context.Context, staged FitSettings) FitPreview {
	out := FitPreview{StagedLabel: staged.Describe()}
	if wp.cfg == nil || wp.fm == nil {
		return out
	}
	target, ok := wp.snapshotPrimaryTarget()
	if !ok {
		return out
	}
	out.HasWallpaper = true
	img := target.img
	dispW, dispH := target.monitor.Rect.Dx(), target.monitor.Rect.Dy()
	out.Display = i18n.Tf("Display {{.ID}} · {{.Size}}", map[string]any{"ID": target.monitor.ID + 1, "Size": fmt.Sprintf("%d×%d", dispW, dispH)})
	resKey := fmt.Sprintf("%dx%d", dispW, dispH)

	// Current: the file already on disk for the saved settings (exact, and free).
	out.CurrentLabel = describeFlags(img.ProcessingFlags)
	curPath := img.DerivativePaths[resKey]
	if curPath == "" {
		curPath = img.FilePath
	}
	if curPath == "" {
		out.CurrentNote = i18n.T("Not ready yet")
	} else if thumb, err := loadThumb(curPath); err != nil {
		out.CurrentNote = i18n.T("Not available")
	} else {
		out.Current = thumb
	}
	if ctx.Err() != nil {
		return out
	}

	// Original: the cached master.
	masterPath, onDisk := wp.masterOnDisk(img.ID, masterExtFor(img))
	if !onDisk {
		out.OriginalNote = i18n.T("Not cached")
		out.StagedNote = i18n.T("Needs the original image")
		return out
	}
	master, err := openMaster(masterPath)
	if err != nil {
		log.Debugf("Smart fit preview: %v", err)
		out.OriginalNote = i18n.T("Not available")
		out.StagedNote = i18n.T("Needs the original image")
		return out
	}
	out.Original = thumbFit(master, previewThumbMaxW, previewThumbMaxH)
	if ctx.Err() != nil {
		return out
	}

	// Staged: the real SmartImageProcessor with a private config, on a reduced copy of the master.
	w, h := img.Width, img.Height
	if w <= 0 || h <= 0 {
		w, h = master.Bounds().Dx(), master.Bounds().Dy()
	}
	policy := FitPolicy{Mode: staged.Mode, MinSize: staged.MinSize, Tuning: wp.cfg.tuningSnapshot()}
	if err := policy.Check(w, h, dispW, dispH); err != nil {
		switch {
		case isTooSmall(err):
			out.StagedNote = i18n.T("Too small, skipped")
		case wp.cfg.framingRescuesShape():
			out.StagedNote = i18n.T("Shown in a frame")
		default:
			out.StagedNote = i18n.T("Wrong shape, skipped")
		}
		return out
	}
	base, ok := wp.previewProcessorBase()
	if !ok {
		out.StagedNote = i18n.T("Not available")
		return out
	}
	proc := NewSmartImageProcessor(base.os, wp.previewConfig(staged), base.pigo)
	src := imaging.Fit(master, previewSourceEdge, previewSourceEdge, imaging.Box)
	tw, th := previewTargetSize(dispW, dispH)
	fitted, err := proc.FitImage(ctx, src, tw, th, img.GetTuning(resKey))
	if err != nil || fitted == nil {
		if ctx.Err() == nil {
			log.Debugf("Smart fit preview: fit failed: %v", err)
			out.StagedNote = i18n.T("Not available")
		}
		return out
	}
	out.Staged = thumbFit(fitted, previewThumbMaxW, previewThumbMaxH)
	return out
}

func isTooSmall(err error) bool { return errors.Is(err, ErrImageTooSmall) }

// previewTargetSize scales the display to fit the thumbnail box, keeping its aspect ratio.
func previewTargetSize(dispW, dispH int) (int, int) {
	if dispW <= 0 || dispH <= 0 {
		return previewThumbMaxW, previewThumbMaxH
	}
	scale := float64(previewThumbMaxW) / float64(dispW)
	if s := float64(previewThumbMaxH) / float64(dispH); s < scale {
		scale = s
	}
	tw, th := int(float64(dispW)*scale+0.5), int(float64(dispH)*scale+0.5)
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}
	return tw, th
}

func thumbFit(img image.Image, maxW, maxH int) image.Image {
	return imaging.Fit(img, maxW, maxH, imaging.Lanczos)
}

func loadThumb(path string) (image.Image, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	img, err := openMaster(path)
	if err != nil {
		return nil, err
	}
	return thumbFit(img, previewThumbMaxW, previewThumbMaxH), nil
}

// FitPreviewThumbs is the Compute function of the "Current wallpaper" strip: the primary
// display's current wallpaper as the original, as it is now, and as the staged settings would
// make it. The third picture is only drawn when the staged settings differ from the saved ones.
func (wp *Plugin) FitPreviewThumbs(ctx context.Context, get func(string) interface{}) []schema.PreviewThumb {
	staged := wp.stagedFitSettings(get)
	p := wp.FitPreviewFor(ctx, staged)
	if !p.HasWallpaper {
		return nil
	}
	saved := FitSettings{
		Mode:      wp.cfg.GetSmartFitMode(),
		MinSize:   wp.cfg.GetMinImageSize(),
		FaceCrop:  wp.cfg.GetFaceCropEnabled(),
		FaceBoost: wp.cfg.GetFaceBoostEnabled(),
	}
	thumbs := []schema.PreviewThumb{
		{Caption: i18n.T("Original"), Sub: p.Display, Image: p.Original, Note: p.OriginalNote},
		{Caption: i18n.T("Current"), Sub: p.CurrentLabel, Image: p.Current, Note: p.CurrentNote},
	}
	third := schema.PreviewThumb{Caption: i18n.T("With your changes"), Sub: p.StagedLabel, Image: p.Staged, Note: p.StagedNote}
	if staged.sameFit(saved) {
		third.Image, third.Note = nil, i18n.T("Change a setting to preview it")
		third.Sub = ""
	}
	return append(thumbs, third)
}

// sameFit reports whether two settings produce the same fitted image.
func (s FitSettings) sameFit(o FitSettings) bool {
	if s.Mode != o.Mode || s.MinSize != o.MinSize {
		return false
	}
	if s.Mode == SmartFitOff {
		return true
	}
	return s.FaceCrop == o.FaceCrop && s.FaceBoost == o.FaceBoost
}
