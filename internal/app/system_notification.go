package app

import (
	"errors"
	"io"
)

// ErrDesktopNotificationUnsupported is returned when no desktop notification
// mechanism is available, so the caller can fall back to the terminal bell.
var ErrDesktopNotificationUnsupported = errors.New("desktop notifications unsupported")

// NewDefaultSystemNotifier returns the platform notifier: a desktop
// notification when one can be delivered, and the terminal bell written to w
// otherwise. It never fails the caller - a missed notification must not take
// down chat.
func NewDefaultSystemNotifier(w io.Writer) SystemNotifier {
	return newDefaultSystemNotifier(w)
}
