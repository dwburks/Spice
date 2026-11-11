package wallpaper

import (
	"errors"
	"fmt"
	"math"

	"github.com/dixieflatline76/Spice/v2/util/log"
)

// Why an image does not fit a display. Callers tell the two apart with errors.Is; the error
// text is unchanged from the plain errors this replaced.
var (
	// ErrImageTooSmall means the image would have to be enlarged more than the Minimum image
	// size setting allows. Virtual framing cannot fix this, so it is never rescued by it.
	ErrImageTooSmall = errors.New("image too small for display")
	// ErrShapeIncompatible means the image's aspect ratio is outside what the Smart fit mode accepts.
	ErrShapeIncompatible = errors.New("image shape does not fit display")
)

// fitError carries a message and a kind that errors.Is can match.
type fitError struct {
	kind error
	msg  string
}

func (e *fitError) Error() string        { return e.msg }
func (e *fitError) Is(target error) bool { return target == e.kind }

func shapeErr(format string, args ...any) error {
	return &fitError{kind: ErrShapeIncompatible, msg: fmt.Sprintf(format, args...)}
}

// sizeEpsilon absorbs float rounding so an image exactly at the limit (3440x1440 on a 3440x1440
// display, or 2752x1152 at 80%) is accepted.
const sizeEpsilon = 1e-9

// CoverScale returns the factor an image of imgW x imgH is enlarged by to cover a dispW x dispH
// display (below 1 means it is scaled down). It returns 0 when any dimension is not positive.
func CoverScale(imgW, imgH, dispW, dispH int) float64 {
	if imgW <= 0 || imgH <= 0 || dispW <= 0 || dispH <= 0 {
		return 0
	}
	return math.Max(float64(dispW)/float64(imgW), float64(dispH)/float64(imgH))
}

// ImageTooSmall reports whether an image is too small for a display under the given minimum
// size mode. Unknown (non-positive) dimensions are never too small: they are checked again
// once the image has been decoded.
func ImageTooSmall(imgW, imgH, dispW, dispH int, mode MinImageSize) bool {
	limit := mode.MaxUpscale()
	if limit == 0 {
		return false
	}
	s := CoverScale(imgW, imgH, dispW, dispH)
	if s == 0 {
		return false
	}
	return s > limit+sizeEpsilon
}

// FitPolicy is the full set of settings that decide whether an image fits a display. It is a
// plain value so the same rules can run against the saved settings (the processor) or staged,
// not-yet-saved ones (the Smart fit page's library stats and preview).
type FitPolicy struct {
	Mode    SmartFitMode
	MinSize MinImageSize
	Tuning  TuningConfig
}

// Check returns nil when an image of imgW x imgH fits a dispW x dispH display, else an error
// matching ErrImageTooSmall or ErrShapeIncompatible. The size rule applies in every Smart fit
// mode, including Off. Missing dimensions fit.
func (p FitPolicy) Check(imgW, imgH, dispW, dispH int) error {
	if imgW <= 0 || imgH <= 0 || dispW <= 0 || dispH <= 0 {
		return nil
	}

	if ImageTooSmall(imgW, imgH, dispW, dispH, p.MinSize) {
		return &fitError{kind: ErrImageTooSmall, msg: fmt.Sprintf(
			"image too small for %dx%d display (%dx%d needs %.2fx enlargement, limit %.2fx)",
			dispW, dispH, imgW, imgH, CoverScale(imgW, imgH, dispW, dispH), p.MinSize.MaxUpscale())}
	}

	if p.Mode == SmartFitOff {
		return nil
	}

	aspectDiff := math.Abs(float64(dispW)/float64(dispH) - float64(imgW)/float64(imgH))
	switch p.Mode {
	case SmartFitNormal:
		return p.checkQuality(imgW, imgH, dispW, dispH, aspectDiff)
	case SmartFitAggressive:
		return p.checkFlexibility(imgW, imgH, dispW, dispH, aspectDiff)
	}
	return nil
}

func (p FitPolicy) checkQuality(imgW, imgH, sysW, sysH int, aspectDiff float64) error {
	isSquare := imgW == imgH
	if !isSquare {
		srcLand := imgW > imgH
		tgtLand := sysW > sysH
		if srcLand != tgtLand {
			maxOriDiff := math.Min(p.Tuning.QualityOrientationMaxDiff, p.Tuning.AspectThreshold)
			if aspectDiff > maxOriDiff {
				return shapeErr("incompatible orientation for Quality mode (Diff %.2f > %.2f)", aspectDiff, maxOriDiff)
			}
		}
	}

	if aspectDiff > p.Tuning.AspectThreshold {
		return shapeErr("aspect ratio diff too large for Quality mode (%.2f > %.2f)", aspectDiff, p.Tuning.AspectThreshold)
	}
	return nil
}

func (p FitPolicy) checkFlexibility(imgW, imgH, sysW, sysH int, aspectDiff float64) error {
	scaleX := float64(imgW) / float64(sysW)
	scaleY := float64(imgH) / float64(sysH)
	surplus := math.Min(scaleX, scaleY)

	// Dynamic Formula: Base * Surplus * AggressiveMultiplier
	effectiveThreshold := p.Tuning.AspectThreshold * surplus * p.Tuning.AggressiveMultiplier

	// SAFETY CAP: Even with high resolution, don't allow insane crops.
	if effectiveThreshold > p.Tuning.FlexibilityAbsoluteMaxDiff {
		effectiveThreshold = p.Tuning.FlexibilityAbsoluteMaxDiff
	}

	// Orientation Safety: Block drastic mismatches (e.g. Landscape on Portrait)
	// Square images (imgWidth == imgHeight) are exempted to allow safe cropping.
	isSquare := imgW == imgH
	if !isSquare {
		srcLand := imgW > imgH
		tgtLand := sysW > sysH
		if srcLand != tgtLand {
			// Orientation Mismatch: Cap threshold to block bad crops (e.g. 16:9 on 9:16)
			if effectiveThreshold > p.Tuning.FlexibilityOrientationMaxDiff {
				effectiveThreshold = p.Tuning.FlexibilityOrientationMaxDiff
			}
		}
	}

	log.Debugf("SmartFit [Flexibility]: Check (Src: %dx%d, Tgt: %dx%d, Surplus: %.2f, DynamicThreshold: %.2f, Diff: %.2f)",
		imgW, imgH, sysW, sysH, surplus, effectiveThreshold, aspectDiff)

	if aspectDiff > effectiveThreshold {
		return shapeErr("image aspect ratio not compatible (Diff: %.2f > Limit: %.2f)", aspectDiff, effectiveThreshold)
	}
	return nil
}

// Policy returns the policy for the saved settings.
func (c *Config) Policy() FitPolicy {
	return FitPolicy{Mode: c.GetSmartFitMode(), MinSize: c.GetMinImageSize(), Tuning: c.tuningSnapshot()}
}

// tuningSnapshot copies the tuning values under the config lock.
func (c *Config) tuningSnapshot() TuningConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Tuning
}

// framingRescuesShape reports whether virtual framing would rescue a shape-incompatible image:
// the fallback is on, or any museum provider frames its images.
func (c *Config) framingRescuesShape() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.VirtualFramingFallback {
		return true
	}
	for _, enabled := range c.MuseumFraming {
		if enabled {
			return true
		}
	}
	return false
}
