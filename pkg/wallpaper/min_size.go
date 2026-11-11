package wallpaper

import (
	"math"

	"github.com/dixieflatline76/Spice/v2/pkg/provider"
)

// Minimum image size helpers that need the connected displays. The size rule itself is in
// fit_policy.go.

// connectedResolutions returns the unique resolutions of the connected displays, falling back
// to the desktop dimension when no monitor list is available. It returns nil when neither is known.
func (wp *Plugin) connectedResolutions() []Resolution {
	if wp.os == nil {
		return nil
	}
	var resolutions []Resolution
	monitors, err := wp.os.GetMonitors()
	if err == nil && len(monitors) > 0 {
		resolutions = GetUniqueResolutions(monitors)
	}
	if len(resolutions) == 0 {
		w, h, err := wp.os.GetDesktopDimension()
		if err == nil {
			resolutions = append(resolutions, Resolution{Width: w, Height: h})
		}
	}
	return resolutions
}

// tooSmallForAllDisplays reports whether img is known to be too small for every connected
// display, so it can be skipped before it is downloaded. Unknown image dimensions or an
// unknown display list never skip: the check is repeated after download.
func (wp *Plugin) tooSmallForAllDisplays(img provider.Image) bool {
	if img.Width <= 0 || img.Height <= 0 || wp.cfg == nil {
		return false
	}
	mode := wp.cfg.GetMinImageSize()
	if mode.MaxUpscale() == 0 {
		return false
	}
	resolutions := wp.connectedResolutions()
	if len(resolutions) == 0 {
		return false
	}
	for _, res := range resolutions {
		if !ImageTooSmall(img.Width, img.Height, res.Width, res.Height, mode) {
			return false
		}
	}
	return true
}

// minResolutionFloor returns the smallest width and height an image needs to be usable on at
// least one connected display. It is the component-wise minimum over the displays, so it never
// excludes an image that fits some display (one that fits only the smaller display stays).
// ok is false when the setting is "Any size" or no display is known.
func (wp *Plugin) minResolutionFloor() (w, h int, ok bool) {
	if wp.cfg == nil {
		return 0, 0, false
	}
	limit := wp.cfg.GetMinImageSize().MaxUpscale()
	if limit == 0 {
		return 0, 0, false
	}
	resolutions := wp.connectedResolutions()
	if len(resolutions) == 0 {
		return 0, 0, false
	}
	w, h = math.MaxInt, math.MaxInt
	for _, res := range resolutions {
		if rw := requiredDimension(res.Width, limit); rw < w {
			w = rw
		}
		if rh := requiredDimension(res.Height, limit); rh < h {
			h = rh
		}
	}
	return w, h, w > 0 && h > 0
}

// requiredDimension is the smallest image dimension that needs at most `limit` enlargement to
// reach display dimension d.
func requiredDimension(d int, limit float64) int {
	return int(math.Ceil(float64(d)/limit - sizeEpsilon))
}
