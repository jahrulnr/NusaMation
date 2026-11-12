//go:build windows

package notify

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// This opt-in test must run in an interactive Windows desktop session.
func TestWindowsCriticalNotification(t *testing.T) {
	if os.Getenv("NUSAMATION_NOTIFY_TEST") != "1" {
		t.Skip("set NUSAMATION_NOTIFY_TEST=1 in an interactive desktop session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), DeliveryTimeout)
	defer cancel()
	err := Detect().Notify(ctx, Notification{Title: "NusaMation critical test", Body: "Critical notification delivery test.", Urgency: UrgencyCritical, Icon: "dialog-error"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\AppUserModelId\`+windowsAppID, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Close()
	name, _, err := key.GetStringValue("DisplayName")
	if err != nil || name != "NusaMation" {
		t.Fatalf("wrong notification identity %q: %v", name, err)
	}
	icon, _, err := key.GetStringValue("IconUri")
	if err != nil || filepath.Base(icon) != "notification-icon.png" {
		t.Fatalf("wrong notification icon %q: %v", icon, err)
	}
	if _, err := os.Stat(icon); err != nil {
		t.Fatalf("notification icon unavailable: %v", err)
	}
}
