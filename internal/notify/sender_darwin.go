//go:build darwin

package notify

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type darwinSender struct{ bin string }

func platformSender() Sender {
	bin, err := exec.LookPath("osascript")
	if err != nil {
		return Null{Reason: "osascript is not installed"}
	}
	return darwinSender{bin: bin}
}

func (s darwinSender) Name() string    { return "osascript" }
func (s darwinSender) Available() bool { return true }

func (s darwinSender) Notify(ctx context.Context, n Notification) error {
	title := n.Title
	if strings.TrimSpace(title) == "" {
		title = "NusaMation"
	}
	script := fmt.Sprintf(`display notification %s with title %s`, appleQuote(n.Body), appleQuote(title))
	out, err := exec.CommandContext(ctx, s.bin, "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func appleQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
