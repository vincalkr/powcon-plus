package notify

import (
	"os"
	"testing"

	"github.com/godbus/dbus/v5"
)

// daemonPresent reports whether the session bus has a Notifications service.
func daemonPresent(t *testing.T) bool {
	t.Helper()
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		return false
	}
	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	return obj.Call("org.freedesktop.Notifications.GetCapabilities", 0).Err == nil
}

// Without a notification daemon the call must report an error rather than
// panic or claim success.
func TestSendWithoutDaemon(t *testing.T) {
	if daemonPresent(t) {
		t.Skip("a notification daemon is on this bus")
	}
	if err := Send("powcon-plus-test", "title", "body", 0); err == nil {
		t.Error("Send returned nil with no notification daemon")
	}
}

// TestSendDelivered sends two real notifications and requires them to be
// accepted. It guards the Call.Err handling: Store() used to fail with a
// spurious "length mismatch" even though the notification was delivered.
//
// Opt in, because it pops up on the desktop:
//
//	POWCON_TEST_NOTIFY=1 go test ./internal/notify/
func TestSendDelivered(t *testing.T) {
	if os.Getenv("POWCON_TEST_NOTIFY") != "1" {
		t.Skip("set POWCON_TEST_NOTIFY=1 to send real notifications")
	}
	if !daemonPresent(t) {
		t.Skip("no notification daemon on this bus")
	}
	if err := Send("powcon-plus", "powcon-plus test", "plain notification", 1000); err != nil {
		t.Errorf("Send: %v", err)
	}
	if err := SendWithIcon("powcon-plus", "powcon-plus test", "icon notification", "dialog-warning", 1000); err != nil {
		t.Errorf("SendWithIcon: %v", err)
	}
}
