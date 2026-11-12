//go:build windows

package notify

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"

	"github.com/jahrulnr/nusamation/web"
)

// Windows identifies unpackaged toast senders using a per-user AppUserModelId.
type windowsSender struct{ bin string }

const windowsAppID = "NusaMation.Desktop"

var windowsIconMu sync.Mutex

func windowsIcon() (string, error) {
	windowsIconMu.Lock()
	defer windowsIconMu.Unlock()
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "NusaMation")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	data, err := fs.ReadFile(web.FS(), "mark.png")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "notification-icon.png")
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, data) {
		return path, nil
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func platformSender() Sender {
	if bin, err := exec.LookPath("powershell.exe"); err == nil {
		return windowsSender{bin: bin}
	}
	// Service environments can carry a thin PATH; try the standard location.
	if root := os.Getenv("SystemRoot"); root != "" {
		bin := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		if _, err := os.Stat(bin); err == nil {
			return windowsSender{bin: bin}
		}
	}
	return Null{Reason: "powershell.exe not found"}
}

func (s windowsSender) Name() string    { return "powershell" }
func (s windowsSender) Available() bool { return true }

func (s windowsSender) Notify(ctx context.Context, n Notification) error {
	title := n.Title
	if strings.TrimSpace(title) == "" {
		title = "NusaMation"
	}
	iconPath, err := windowsIcon()
	if err != nil {
		return fmt.Errorf("notification icon: %w", err)
	}
	script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$appId = %s
$key = 'HKCU:\Software\Classes\AppUserModelId\' + $appId
New-Item -Path $key -Force | Out-Null
New-ItemProperty -Path $key -Name DisplayName -Value 'NusaMation' -PropertyType String -Force | Out-Null
New-ItemProperty -Path $key -Name IconUri -Value %s -PropertyType String -Force | Out-Null
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType=WindowsRuntime] | Out-Null
[Windows.UI.Notifications.ToastNotification, Windows.UI.Notifications, ContentType=WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType=WindowsRuntime] | Out-Null
$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml('<toast><visual><binding template="ToastGeneric"><text></text><text></text></binding></visual></toast>')
$nodes = $xml.GetElementsByTagName('text')
$nodes.Item(0).InnerText = %s
$nodes.Item(1).InnerText = %s
$toast = [Windows.UI.Notifications.ToastNotification]::new($xml)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($appId).Show($toast)`, psQuote(windowsAppID), psQuote(iconPath), psQuote(title), psQuote(n.Body))
	cmd := exec.CommandContext(ctx, s.bin,
		"-NoProfile", "-NonInteractive", "-EncodedCommand", encodeUTF16(script))
	// The app often runs without a console (hidden logon task): prevent a
	// console window from flashing on screen for each notification.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("powershell notify: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// -EncodedCommand expects UTF-16LE base64, which sidesteps every quoting and
// escaping problem in the notification text.
func encodeUTF16(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}
