//go:build windows

package update

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
)

// installBinary is a no-op on Windows: a running executable cannot be
// replaced, so the staged file is left for the updater helper to move into
// place once this process exits.
func installBinary(_, _ string) error { return nil }

// restartSelf spawns a detached PowerShell helper that waits for this process
// to exit, moves the staged binary over the executable and starts it again,
// then exits so the swap can happen. The helper exits 1 without relaunching
// when the move never succeeds — relaunching the old binary would hide the
// failure behind a healthy-looking restart.
func restartSelf(exe, staged string) error {
	cwd, _ := os.Getwd()
	// -ArgumentList takes one string, not an array: PowerShell would join
	// array items with a bare space and corrupt args containing spaces.
	argLine := winArgv(os.Args[1:])
	script := fmt.Sprintf(`$exe = %s
$new = %s
$parentPid = %d
while (Get-Process -Id $parentPid -ErrorAction SilentlyContinue) { Start-Sleep -Milliseconds 400 }
$moved = $false
for ($i = 0; $i -lt 40; $i++) {
  try { Move-Item -LiteralPath $new -Destination $exe -Force; $moved = $true; break }
  catch { Start-Sleep -Milliseconds 500 }
}
if (-not $moved) { exit 1 }
$argLine = %s
if ($argLine) { Start-Process -FilePath $exe -ArgumentList $argLine -WorkingDirectory %s }
else { Start-Process -FilePath $exe -WorkingDirectory %s }`,
		psQuote(exe), psQuote(staged), os.Getpid(), psQuote(argLine), psQuote(cwd), psQuote(cwd))

	cmd := exec.Command(powershellPath(), "-NoProfile", "-NonInteractive", "-EncodedCommand", encodeUTF16(script))
	// Detached + no window: the helper must outlive this process.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000 | 0x00000008 | 0x00000200} // CREATE_NO_WINDOW | DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start updater: %w", err)
	}
	os.Exit(0)
	return nil // unreachable
}

// winArgv renders args the way CommandLineToArgvW expects: every argument is
// double-quoted, embedded quotes are escaped as \" and a backslash run before
// a quote (or at the end) is doubled.
func winArgv(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('"')
		slashes := 0
		for _, c := range a {
			switch {
			case c == '\\':
				slashes++
			case c == '"':
				b.WriteString(strings.Repeat(`\`, slashes*2+1))
				b.WriteByte('"')
				slashes = 0
			default:
				if slashes > 0 {
					b.WriteString(strings.Repeat(`\`, slashes))
					slashes = 0
				}
				b.WriteRune(c)
			}
		}
		b.WriteString(strings.Repeat(`\`, slashes*2))
		b.WriteByte('"')
	}
	return b.String()
}

// powershellPath prefers PATH, then falls back to the standard System32
// location — service environments can carry a thin PATH.
func powershellPath() string {
	if p, err := exec.LookPath("powershell.exe"); err == nil {
		return p
	}
	if root := os.Getenv("SystemRoot"); root != "" {
		p := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "powershell.exe"
}

func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func encodeUTF16(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}
