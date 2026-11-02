package whatsapp

import (
	"fmt"
	"log/slog"

	waLog "go.mau.fi/whatsmeow/util/log"
)

type waLogger struct {
	log    *slog.Logger
	module string
}

func (l waLogger) Debugf(msg string, args ...any) {
	l.log.Debug(fmt.Sprintf(msg, args...), "module", l.module)
}
func (l waLogger) Infof(msg string, args ...any) {
	l.log.Debug(fmt.Sprintf(msg, args...), "module", l.module)
}
func (l waLogger) Warnf(msg string, args ...any) {
	l.log.Warn(fmt.Sprintf(msg, args...), "module", l.module)
}
func (l waLogger) Errorf(msg string, args ...any) {
	l.log.Error(fmt.Sprintf(msg, args...), "module", l.module)
}
func (l waLogger) Sub(module string) waLog.Logger {
	return waLogger{log: l.log, module: l.module + "/" + module}
}

var _ waLog.Logger = waLogger{}
