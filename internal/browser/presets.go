package browser

import (
	"errors"
	"fmt"
)

// ErrUnknownPreset marks a preset name that ResolveDevice could not find.
var ErrUnknownPreset = errors.New("unknown device preset")

// Presets are named device profiles for emulate calls. "desktop" restores a
// plain viewport with the real user agent; the mobile presets switch on the
// CDP mobile flag, touch emulation, and a matching UA.
var Presets = map[string]DeviceOpts{
	"desktop": {Width: 1280, Height: 800, DPR: 1},
	"mobile": {
		Width: 390, Height: 844, DPR: 3, Mobile: true, Touch: true,
		UserAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
	},
	"tablet": {
		Width: 768, Height: 1024, DPR: 2, Mobile: true, Touch: true,
		UserAgent: "Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
	},
}

// DevicePatch carries the optional fields of an emulate request — nil means
// "keep the value from the base profile".
type DevicePatch struct {
	Width     *int64
	Height    *int64
	DPR       *float64
	Mobile    *bool
	UserAgent *string
	Touch     *bool
}

// ResolveDevice builds a full device profile from an optional preset plus a
// patch. Without a preset the current state is the base so partial changes
// compose; setting mobile without touch defaults touch to the same value.
func ResolveDevice(cur DeviceOpts, preset string, p DevicePatch) (DeviceOpts, error) {
	var o DeviceOpts
	if preset != "" {
		base, ok := Presets[preset]
		if !ok {
			return o, fmt.Errorf("%w %q", ErrUnknownPreset, preset)
		}
		o = base
	} else {
		o = cur
	}
	if p.Width != nil {
		o.Width = *p.Width
	}
	if p.Height != nil {
		o.Height = *p.Height
	}
	if p.DPR != nil {
		o.DPR = *p.DPR
	}
	if p.Mobile != nil {
		o.Mobile = *p.Mobile
	}
	if p.UserAgent != nil {
		o.UserAgent = *p.UserAgent
	}
	if p.Touch != nil {
		o.Touch = *p.Touch
	} else if p.Mobile != nil {
		o.Touch = o.Mobile
	}
	return o, nil
}
