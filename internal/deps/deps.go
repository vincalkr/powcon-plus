// Package deps checks the system dependencies the program needs and explains
// how to install whatever is missing. It is the backend of `powcon-plus check`.
package deps

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/vincalkr/powcon-plus/internal/psu"
)

// Status is the outcome of a single probe.
type Status string

const (
	StatusOK      Status = "ok"
	StatusWarn    Status = "warn"
	StatusMissing Status = "missing"
	StatusSkip    Status = "skip" // not applicable on this host
)

// Finding is one probe result. Fix holds the command that installs or enables
// what is missing; Essential marks what the program cannot work without.
type Finding struct {
	Name      string
	Status    Status
	Detail    string
	Fix       string
	Essential bool
}

// Options selects which groups of dependencies are probed.
type Options struct {
	Build     bool   // toolchain and development packages, for compiling
	GUIBinary string // binary to inspect for unresolved libraries (empty = look around)
}

// goMin is the Go toolchain version the project builds with.
const goMin = 24

// Check runs the probes for the requested groups.
func Check(o Options) []Finding {
	f := []Finding{
		checkSensor(),
		checkSessionBus(),
		checkTrayHost(),
		checkGUIBinary(o.GUIBinary),
	}
	if o.Build {
		f = append(f, checkBuildDeps()...)
	}
	return f
}

// MissingEssential returns the findings that block normal use.
func MissingEssential(fs []Finding) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Essential && f.Status == StatusMissing {
			out = append(out, f)
		}
	}
	return out
}

// Report writes a human readable listing, one block per finding.
func Report(w io.Writer, fs []Finding) {
	width := 0
	for _, f := range fs {
		if len(f.Name) > width {
			width = len(f.Name)
		}
	}
	for _, f := range fs {
		mark := string(f.Status)
		if f.Essential && f.Status == StatusMissing {
			mark = "missing*" // essential
		}
		fmt.Fprintf(w, "  %-*s  %-*s  %s\n", 8, mark, width, f.Name, f.Detail)
		if f.Fix != "" && f.Status != StatusOK && f.Status != StatusSkip {
			for i, line := range strings.Split(f.Fix, "\n") {
				label := "     fix"
				if i > 0 {
					label = "         "
				}
				fmt.Fprintf(w, "  %s  %s\n", label, line)
			}
		}
	}
	fmt.Fprintln(w, "\n  * the program cannot do its main job without these")
}

// pkgManager picks the installer command for the host, empty if unknown.
func pkgManager() (name, install string) {
	switch {
	case have("apt-get"):
		return "apt", "sudo apt-get install -y"
	case have("dnf"):
		return "dnf", "sudo dnf install -y"
	case have("pacman"):
		return "pacman", "sudo pacman -S"
	case have("zypper"):
		return "zypper", "sudo zypper install -y"
	}
	return "", ""
}

// have reports whether an executable is on PATH.
func have(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// checkSensor verifies the PSU hwmon node, the one thing nothing works without.
func checkSensor() Finding {
	p, ok := psu.Available("")
	if ok {
		return Finding{Name: "psu sensor", Status: StatusOK, Detail: p, Essential: true}
	}
	fix := "connect the power supply USB cable"
	if _, err := os.Stat("/sys/module/" + psu.Chip); err != nil {
		fix += "\nload the kernel module: sudo modprobe " + psu.Chip
	} else if have("lsmod") {
		fix += "\nthe module is loaded: check that no other tool (OpenLinkHub,\n         liquidctl, iCUE) has taken over the device"
	}
	return Finding{
		Name:      "psu sensor",
		Status:    StatusMissing,
		Detail:    fmt.Sprintf("no hwmon with name=%q under /sys/class/hwmon", psu.Chip),
		Fix:       fix,
		Essential: true,
	}
}

// checkSessionBus verifies there is a user session bus, which the tray and the
// desktop notifications need.
func checkSessionBus() Finding {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		return Finding{
			Name:   "session bus",
			Status: StatusMissing,
			Detail: "DBUS_SESSION_BUS_ADDRESS is not set",
			Fix: "run the program inside the graphical session;\n" +
				"over ssh: export DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$(id -u)/bus\n" +
				"without a tray use: powcon-plus run --no-tray",
			Essential: true,
		}
	}
	return Finding{Name: "session bus", Status: StatusOK, Detail: "available"}
}

// checkTrayHost reports whether the desktop actually provides a SNI tray.
func checkTrayHost() Finding {
	desktop := strings.ToLower(os.Getenv("XDG_CURRENT_DESKTOP"))
	switch {
	case desktop == "":
		return Finding{
			Name:   "tray host",
			Status: StatusWarn,
			Detail: "XDG_CURRENT_DESKTOP is not set, cannot tell the desktop",
			Fix: "the tray needs a StatusNotifierItem host: GNOME (with the\n" +
				"AppIndicator extension), KDE, XFCE, Hyprland or swaybar",
		}
	case strings.Contains(desktop, "kde") || strings.Contains(desktop, "plasma"):
		return Finding{Name: "tray host", Status: StatusOK, Detail: "KDE: SNI tray built in"}
	case strings.Contains(desktop, "xfce"):
		return Finding{Name: "tray host", Status: StatusOK, Detail: "XFCE: SNI tray built in"}
	case strings.Contains(desktop, "hyprland") || strings.Contains(desktop, "sway"):
		return Finding{Name: "tray host", Status: StatusOK, Detail: "wayland compositor: needs a SNI host (waybar, swaybar)"}
	case strings.Contains(desktop, "gnome") || strings.Contains(desktop, "ubuntu"):
		return checkAppIndicator(desktop)
	}
	return Finding{
		Name:   "tray host",
		Status: StatusWarn,
		Detail: "unrecognised desktop: " + desktop,
		Fix:    "verify that the desktop provides a StatusNotifierItem host",
	}
}

// checkAppIndicator looks for the GNOME extension that draws SNI icons.
func checkAppIndicator(desktop string) Finding {
	if !have("gnome-extensions") {
		return Finding{
			Name:   "tray host",
			Status: StatusWarn,
			Detail: desktop + ": gnome-extensions not found, cannot verify",
			Fix: "install the AppIndicator extension, then:\n" +
				"  gnome-extensions enable appindicatorsupport@rgcjonas.gmail.com",
		}
	}
	out, err := exec.Command("gnome-extensions", "list", "--enabled").Output()
	if err != nil {
		return Finding{
			Name:   "tray host",
			Status: StatusWarn,
			Detail: desktop + ": gnome-extensions failed, cannot verify",
			Fix:    "check the extension status: gnome-extensions list",
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.Contains(strings.ToLower(line), "appindicator") {
			return Finding{Name: "tray host", Status: StatusOK, Detail: "enabled: " + line}
		}
	}
	_, install := pkgManager()
	fix := "enable or install the AppIndicator extension:\n  gnome-extensions enable ubuntu-appindicators@ubuntu.com"
	if install != "" {
		fix += "\n  " + install + " gnome-shell-extension-appindicator"
	}
	return Finding{
		Name:   "tray host",
		Status: StatusMissing,
		Detail: desktop + ": no AppIndicator extension enabled",
		Fix:    fix,
	}
}

// guiBinaryCandidates are the usual places the GUI binary is looked for.
func guiBinaryCandidates() []string {
	out := []string{"bin/powcon-plus-gui"}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, ".local", "bin", "powcon-plus-gui"))
	}
	if exe, err := os.Executable(); err == nil {
		out = append(out, exe)
	}
	return out
}

// checkGUIBinary asks the loader which shared objects the GUI binary cannot
// resolve. Deriving the list from the binary itself beats a hardcoded one:
// which libraries are actually linked depends on how the toolkit was built.
func checkGUIBinary(hint string) Finding {
	path := hint
	if path == "" {
		for _, c := range guiBinaryCandidates() {
			if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
				path = c
				break
			}
		}
	}
	if path == "" {
		return Finding{
			Name:   "gui libraries",
			Status: StatusSkip,
			Detail: "no GUI binary found to inspect (build it with `make gui`)",
		}
	}
	if !have("ldd") {
		return Finding{
			Name:   "gui libraries",
			Status: StatusWarn,
			Detail: "ldd is not available, cannot verify " + path,
			Fix:    "check the missing libraries by hand: ldd " + path,
		}
	}
	out, err := exec.Command("ldd", path).Output()
	if err != nil && len(out) == 0 {
		return Finding{
			Name:   "gui libraries",
			Status: StatusWarn,
			Detail: "ldd failed on " + path,
			Fix:    "check by hand: ldd " + path,
		}
	}
	var missing []string
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "not found") {
			continue
		}
		if fields := strings.Fields(line); len(fields) > 0 {
			missing = append(missing, fields[0])
		}
	}
	if len(missing) == 0 {
		return Finding{
			Name:   "gui libraries",
			Status: StatusOK,
			Detail: "every shared object resolves in " + path,
		}
	}
	_, install := pkgManager()
	fix := "the detail window cannot start, the binary needs:\n  " + strings.Join(missing, " ")
	if install != "" {
		fix += "\non Debian/Ubuntu the packages are usually:\n  " + install +
			" libgl1 libx11-6 libxcursor1 libxrandr2 libxinerama1 libxkbcommon0"
	}
	fix += "\nthe daemon and the tray work without them"
	return Finding{
		Name:   "gui libraries",
		Status: StatusWarn,
		Detail: "unresolved in " + path + ": " + strings.Join(missing, " "),
		Fix:    fix,
	}
}

// checkBuildDeps reports the toolchain needed to compile the project.
func checkBuildDeps() []Finding {
	var out []Finding
	name, install := pkgManager()

	goBin, err := exec.LookPath("go")
	switch {
	case err != nil:
		fix := "install Go 1.24 or newer"
		if install != "" {
			fix += "\n  " + install + " golang"
		}
		out = append(out, Finding{
			Name:   "go toolchain",
			Status: StatusMissing,
			Detail: "go not found on PATH",
			Fix:    fix,
		})
	default:
		ver, err := exec.Command(goBin, "version").Output()
		major, minor, ok := parseGoVersion(string(ver))
		switch {
		case err != nil:
			out = append(out, Finding{
				Name:   "go toolchain",
				Status: StatusWarn,
				Detail: "go version failed: " + err.Error(),
			})
		case !ok:
			out = append(out, Finding{
				Name:   "go toolchain",
				Status: StatusWarn,
				Detail: "cannot parse: " + strings.TrimSpace(string(ver)),
			})
		case major < 1 || (major == 1 && minor < goMin):
			out = append(out, Finding{
				Name:   "go toolchain",
				Status: StatusMissing,
				Detail: fmt.Sprintf("%s is older than 1.%d", strings.TrimSpace(string(ver)), goMin),
				Fix:    "install Go 1.24 or newer (https://go.dev/dl/)",
			})
		default:
			out = append(out, Finding{Name: "go toolchain", Status: StatusOK, Detail: strings.TrimSpace(string(ver))})
		}
	}

	if have("cc") || have("gcc") {
		out = append(out, Finding{Name: "c compiler", Status: StatusOK, Detail: "needed by the gui build only"})
	} else {
		fix := "the gui build needs cgo: install a C compiler"
		if install != "" {
			fix += "\n  " + install + " build-essential"
		}
		out = append(out, Finding{
			Name:   "c compiler",
			Status: StatusMissing,
			Detail: "no cc/gcc on PATH",
			Fix:    fix,
		})
	}

	const devPkgConfig = "gl x11 xcursor xrandr xinerama xkbcommon"
	pkgs := strings.Fields(devPkgConfig)
	if !have("pkg-config") {
		out = append(out, Finding{
			Name:   "gui headers",
			Status: StatusWarn,
			Detail: "pkg-config not found, cannot verify the development packages",
		})
		return out
	}
	args := append([]string{"--exists"}, pkgs...)
	if err := exec.Command("pkg-config", args...).Run(); err == nil {
		out = append(out, Finding{Name: "gui headers", Status: StatusOK, Detail: "gl, x11, xcursor, xrandr, xinerama, xkbcommon"})
	} else {
		fix := "the gui build needs the development packages"
		if install != "" {
			switch name {
			case "apt":
				fix += "\n  " + install + " libgl1-mesa-dev libx11-dev libxcursor-dev libxrandr-dev libxinerama-dev libxkbcommon-dev"
			case "dnf":
				fix += "\n  " + install + " mesa-libGL-devel libX11-devel libXcursor-devel libXrandr-devel libXinerama-devel libxkbcommon-devel"
			case "pacman":
				fix += "\n  " + install + " mesa libx11 libxcursor libxrandr libxinerama libxkbcommon"
			case "zypper":
				fix += "\n  " + install + " libgl1-mesa-devel libX11-devel libXcursor-devel libXrandr-devel libXinerama-devel libxkbcommon-devel"
			}
		}
		out = append(out, Finding{
			Name:   "gui headers",
			Status: StatusWarn,
			Detail: "some development package is missing (" + devPkgConfig + ")",
			Fix:    fix,
		})
	}
	return out
}

var goVersionRe = regexp.MustCompile(`go(\d+)\.(\d+)`)

// parseGoVersion reads major.minor out of `go version go1.26.0 linux/amd64`.
func parseGoVersion(s string) (major, minor int, ok bool) {
	m := goVersionRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	return major, minor, true
}
