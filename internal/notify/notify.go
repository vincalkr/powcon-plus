// Package notify sends desktop notifications via the freedesktop Notifications interface.
package notify

import (
	"github.com/godbus/dbus/v5"
)

// Send publishes a notification. hints/timeout are optional.
func Send(appName, title, body string, timeoutMs int) error {
	conn, err := dbus.SessionBus()
	if err != nil {
		return err
	}
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	call := obj.Call("org.freedesktop.Notifications.Notify", 0,
		appName, uint32(0), "", title, body, []string{}, map[string]dbus.Variant{}, int32(timeoutMs))
	return call.Store()
}

// SendWithIcon publishes a notification with a theme icon.
func SendWithIcon(appName, title, body, icon string, timeoutMs int) error {
	conn, err := dbus.SessionBus()
	if err != nil {
		return err
	}
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	call := obj.Call("org.freedesktop.Notifications.Notify", 0,
		appName, uint32(0), icon, title, body, []string{}, map[string]dbus.Variant{}, int32(timeoutMs))
	return call.Store()
}
