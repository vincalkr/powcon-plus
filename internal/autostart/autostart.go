// Package autostart manages autostart in the user session.
//
// Modes:
//   - xdg:     ~/.config/autostart/powcon-plus.desktop (graphical session, KDE/GNOME)
//   - systemd: ~/.config/systemd/user/powcon-plus.service + enable on graphical-session.target
package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// AppID is the identifier used for the desktop entry and the systemd unit.
const AppID = "powcon-plus"

// SelfPath returns the absolute path of the current executable.
func SelfPath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(p); err == nil && r != "" {
		p = r
	}
	return filepath.Abs(p)
}

// XDGPath is the XDG autostart file.
func XDGPath() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "autostart", AppID+".desktop"), nil
}

// UnitPath is the user systemd unit.
func UnitPath() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "systemd", "user", AppID+".service"), nil
}

// InstallXDG writes the autostart desktop entry.
func InstallXDG(execPath string, extraArgs []string) (string, error) {
	if execPath == "" {
		var err error
		execPath, err = SelfPath()
		if err != nil {
			return "", err
		}
	}
	path, err := XDGPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	args := append([]string{"run"}, extraArgs...)
	body := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=%s
GenericName=Monitor consumi
Comment=Potenza ed energia della workstation dal PSU Corsair
Exec=%s %s
Icon=utilities-system-monitor
Terminal=false
X-GNOME-Autostart-enabled=true
X-GNOME-Autostart-Delay=4
X-KDE-autostart-after=panel
`, AppID, execPath, strings.Join(args, " "))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// RemoveXDG deletes the desktop entry.
func RemoveXDG() (bool, error) {
	path, err := XDGPath()
	if err != nil {
		return false, err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// XDGInstalled reports whether the XDG autostart entry exists.
func XDGInstalled() (bool, string) {
	path, err := XDGPath()
	if err != nil {
		return false, ""
	}
	if _, err := os.Stat(path); err != nil {
		return false, path
	}
	return true, path
}

// InstallSystemd writes the user unit (it does not enable it: the caller does that).
func InstallSystemd(execPath string, extraArgs []string) (string, error) {
	if execPath == "" {
		var err error
		execPath, err = SelfPath()
		if err != nil {
			return "", err
		}
	}
	path, err := UnitPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	args := append([]string{"run"}, extraArgs...)
	body := fmt.Sprintf(`[Unit]
Description=%s — monitor consumi workstation (PSU Corsair)
Documentation=man:powcon-plus
PartOf=graphical-session.target
After=graphical-session.target

[Service]
Type=simple
ExecStart=%s %s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=graphical-session.target
`, AppID, execPath, strings.Join(args, " "))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// SystemctlUser runs systemctl --user (reload/enable/start), ignoring
// non-critical environment errors.
func SystemctlUser(args ...string) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemctl non disponibile")
	}
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
