package browser

import "testing"

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }
func b(v bool) *bool         { return &v }
func str(v string) *string   { return &v }

func TestResolveDevicePreset(t *testing.T) {
	o, err := ResolveDevice(DeviceOpts{Width: 1280, Height: 800, DPR: 1}, "mobile", DevicePatch{})
	if err != nil {
		t.Fatal(err)
	}
	if !o.Mobile || !o.Touch || o.UserAgent == "" {
		t.Fatalf("mobile preset = %+v", o)
	}
	if o.Width != 390 || o.Height != 844 || o.DPR != 3 {
		t.Fatalf("mobile dims = %+v", o)
	}
}

func TestResolveDeviceUnknownPreset(t *testing.T) {
	if _, err := ResolveDevice(DeviceOpts{}, "fliptop", DevicePatch{}); err == nil {
		t.Fatal("unknown preset should error")
	}
}

func TestResolveDevicePatchOverridesPreset(t *testing.T) {
	o, err := ResolveDevice(DeviceOpts{}, "mobile", DevicePatch{Width: i64(400), DPR: f64(2)})
	if err != nil {
		t.Fatal(err)
	}
	if o.Width != 400 || o.Height != 844 || o.DPR != 2 || !o.Mobile {
		t.Fatalf("patched mobile = %+v", o)
	}
}

func TestResolveDevicePartialOnCurrent(t *testing.T) {
	cur := DeviceOpts{Width: 500, Height: 500, DPR: 2, Mobile: true, Touch: true, UserAgent: "ua"}
	o, err := ResolveDevice(cur, "", DevicePatch{Width: i64(900)})
	if err != nil {
		t.Fatal(err)
	}
	if o.Width != 900 || o.Height != 500 || o.DPR != 2 || !o.Mobile || o.UserAgent != "ua" {
		t.Fatalf("partial = %+v", o)
	}
}

func TestResolveDeviceMobileImpliesTouch(t *testing.T) {
	o, err := ResolveDevice(DeviceOpts{}, "", DevicePatch{Mobile: b(true)})
	if err != nil {
		t.Fatal(err)
	}
	if !o.Touch {
		t.Fatal("mobile=true without touch should default touch on")
	}
	o, err = ResolveDevice(DeviceOpts{}, "", DevicePatch{Mobile: b(true), Touch: b(false)})
	if err != nil {
		t.Fatal(err)
	}
	if o.Touch {
		t.Fatal("explicit touch=false should win over the mobile default")
	}
}

func TestResolveDeviceDesktopReset(t *testing.T) {
	cur := Presets["mobile"]
	o, err := ResolveDevice(cur, "desktop", DevicePatch{})
	if err != nil {
		t.Fatal(err)
	}
	if o.Mobile || o.Touch || o.UserAgent != "" || o.DPR != 1 {
		t.Fatalf("desktop should clear mobile state = %+v", o)
	}
}

func TestResolveDeviceUAString(t *testing.T) {
	o, err := ResolveDevice(DeviceOpts{UserAgent: "x"}, "", DevicePatch{UserAgent: str("")})
	if err != nil {
		t.Fatal(err)
	}
	if o.UserAgent != "" {
		t.Fatal("empty ua should clear the override")
	}
}
