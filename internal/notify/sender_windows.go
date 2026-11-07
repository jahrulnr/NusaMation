//go:build windows

package notify

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
)

// On Windows the notification is raised through a temporary
// System.Windows.Forms.NotifyIcon balloon — the only toast mechanism that
// works from an unpackaged app without installing modules (BurntToast) or
// registering a start-menu shortcut with an AppUserModelID. On Windows 10+
// balloons surface as native toasts.
type windowsSender struct{ bin string }

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
	tip := "Info"
	switch n.Icon {
	case "dialog-error":
		tip = "Error"
	case "dialog-question", "dialog-warning":
		tip = "Warning"
	}
	hold := 6000
	if n.Urgency == UrgencyCritical {
		// Longer dwell, but the PowerShell process must finish inside the
		// caller's ~10s context or it is killed mid-balloon.
		hold = 8000
	}
	script := fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$ni = New-Object System.Windows.Forms.NotifyIcon
$ni.Icon = [System.Drawing.SystemIcons]::Information
$ni.Visible = $true
$ni.ShowBalloonTip(%d, %s, %s, [System.Windows.Forms.ToolTipIcon]::%s)
Start-Sleep -Milliseconds %d
$ni.Dispose()`, hold, psQuote(title), psQuote(n.Body), tip, hold+1500)
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
