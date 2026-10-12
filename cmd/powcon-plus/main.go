// Command powcon-plus monitors workstation power and energy by reading the
// Corsair PSU (HX/AX "i" series) through the kernel hwmon driver.
//
//	powcon-plus run              daemon with a tray icon (default)
//	powcon-plus status           one-shot reading
//	powcon-plus report           per-day summary
//	powcon-plus window           detail window (gui-tagged build)
//	powcon-plus ctl <cmd>        control the running daemon
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vincalkr/powcon-plus/internal/autostart"
	"github.com/vincalkr/powcon-plus/internal/config"
	"github.com/vincalkr/powcon-plus/internal/ctl"
	"github.com/vincalkr/powcon-plus/internal/currency"
	"github.com/vincalkr/powcon-plus/internal/daemon"
	"github.com/vincalkr/powcon-plus/internal/deps"
	"github.com/vincalkr/powcon-plus/internal/energy"
	"github.com/vincalkr/powcon-plus/internal/psu"
	"github.com/vincalkr/powcon-plus/internal/report"
	"github.com/vincalkr/powcon-plus/internal/status"
	"github.com/vincalkr/powcon-plus/internal/store"
	"github.com/vincalkr/powcon-plus/internal/version"
)

func main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			usage(os.Stdout)
			return
		}
		if !strings.HasPrefix(args[0], "-") {
			cmd, args = args[0], args[1:]
		}
	}
	var err error
	switch cmd {
	case "run":
		err = cmdRun(args)
	case "status":
		err = cmdStatus(args)
	case "report":
		err = cmdReport(args)
	case "window":
		err = cmdWindow(args)
	case "ctl":
		err = cmdCtl(args)
	case "sensors":
		err = cmdSensors(args)
	case "check", "doctor":
		err = cmdCheck(args)
	case "install-autostart":
		err = cmdInstallAutostart(args)
	case "uninstall-autostart":
		err = cmdUninstallAutostart(args)
	case "uninstall":
		err = cmdUninstall(args)
	case "autostart-status":
		err = cmdAutostartStatus(args)
	case "init-config":
		err = cmdInitConfig(args)
	case "version", "--version":
		fmt.Printf("powcon-plus %s\n", version.Version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `powcon-plus — workstation power/energy monitor (Corsair HX1200i PSU)

Usage:
  powcon-plus run [flags]            start the daemon with a tray icon (default)
  powcon-plus status [--json]        one-shot reading (uses the daemon when up)
  powcon-plus report [--days N] [--csv]
                                     per-day kWh/cost summary, newest first
                                     (N=0 → every day on disk)
  powcon-plus window                 detail window (gui-tagged build)
  powcon-plus ctl <cmd> [k=v...]     control the daemon
                                      (status|config|pause|resume|label-mode|alert|ui-language|price|autostart-status|autostart-set enabled=1|quit)
  powcon-plus sensors                list the detected PSU hwmon channels
  powcon-plus check [--build]        verify the system dependencies (sensor, tray,
                                      libraries) and print how to install what is
                                      missing; --build also checks the toolchain
  powcon-plus install-autostart [--mode xdg|systemd]
  powcon-plus uninstall-autostart
  powcon-plus uninstall [--purge]    stop the daemon, remove autostart and binaries;
                                      --purge also removes data and configuration
  powcon-plus autostart-status
  powcon-plus init-config            write ~/.config/powcon-plus/config.toml
  powcon-plus version                print the version
  powcon-plus help                   show this help

Common flags:
  --config PATH      configuration file (default ~/.config/powcon-plus/config.toml)
  --no-tray          do not create the tray icon
  --no-store         do not write samples to disk
  --verbose          detailed logging

Files:
  ~/.config/powcon-plus/config.toml   configuration
  ~/.local/share/powcon-plus/         daily.json, samples-*.jsonl, control.sock
`)
}

type runFlags struct {
	config  string
	noTray  bool
	noStore bool
	verbose bool
}

func parseRunFlags(name string, args []string) (runFlags, error) {
	var f runFlags
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&f.config, "config", "", "configuration file")
	fs.BoolVar(&f.noTray, "no-tray", false, "no tray icon")
	fs.BoolVar(&f.noStore, "no-store", false, "do not write samples to disk")
	fs.BoolVar(&f.verbose, "verbose", false, "detailed logging")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	return f, nil
}

func cmdRun(args []string) error {
	f, err := parseRunFlags("run", args)
	if err != nil {
		return err
	}
	app, err := daemon.New(daemon.Options{
		ConfigPath: f.config,
		NoTray:     f.noTray,
		NoStore:    f.noStore,
		Verbose:    f.verbose,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return app.Run(ctx)
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	configPath := fs.String("config", "", "configuration file")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if snap, ok := daemonSnapshot(cfg.DataDir); ok {
		printSnapshot(snap, cfg, *jsonOut)
		return nil
	}
	// No daemon running: read the sensor directly.
	dir, err := psu.Detect(cfg.Sensor)
	if err != nil {
		return err
	}
	rd, err := psu.New(dir)
	if err != nil {
		return err
	}
	s, err := rd.Read()
	if err != nil {
		return err
	}
	acc := energy.NewAccumulator(5*time.Second, 0, 0)
	acc.Add(s)
	snap := status.Build(s, acc, dir, false, cfg.LabelMode, 0, cfg.PricePerKWh, cfg.Currency, nil, nil)
	snap.TodayWh = 0
	printSnapshot(snap, cfg, *jsonOut)
	return nil
}

func daemonSnapshot(dataDir string) (status.Snapshot, bool) {
	resp, err := ctl.Call(filepath.Join(dataDir, "control.sock"), ctl.Request{Cmd: "status"})
	if err != nil || !resp.OK {
		return status.Snapshot{}, false
	}
	var s status.Snapshot
	if err := json.Unmarshal(resp.Data, &s); err != nil {
		return status.Snapshot{}, false
	}
	return s, true
}

func printSnapshot(s status.Snapshot, cfg config.Config, asJSON bool) {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(s)
		return
	}
	fmt.Printf("sensor:       %s (%s)\n", s.Sensor, map[bool]string{true: "online", false: "OFFLINE"}[s.Online])
	fmt.Printf("AC power:     %6.1f W   [%s]\n", s.W, s.Label(cfg))
	fmt.Printf("input:        %6.1f V AC\n", s.VIn)
	fmt.Printf("+12V:         %6.2f V  %6.2f A  %6.1f W\n", s.V12, s.I12, s.P12)
	fmt.Printf("+5V:          %6.2f V  %6.2f A  %6.1f W\n", s.V5, s.I5, s.P5)
	fmt.Printf("+3.3V:        %6.2f V  %6.2f A  %6.1f W\n", s.V33, s.I33, s.P33)
	fmt.Printf("temperatures: VRM %.1f °C   case %.1f °C   fan %.0f RPM (%.0f%%)\n",
		s.TVRM, s.TCase, s.Fan, s.PWM)
	if s.SessionSeconds > 0 {
		fmt.Printf("session:      %.2f kWh in %s (avg %.0f W, peak %.0f W)\n",
			s.SessionWh/1000, fmtDur(s.SessionSeconds), s.SessionAvgW, s.SessionMaxW)
	}
	if s.TodaySamples > 0 {
		fmt.Printf("today:        %.2f kWh  (%.2f %s)  avg %.0f W  peak %.0f W  min %.0f W\n",
			s.TodayWh/1000, s.TodayCost, currency.Symbol(s.Currency), s.TodayAvgW, s.TodayMaxW, s.TodayMinW)
	}
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	configPath := fs.String("config", "", "configuration file")
	days := fs.Int("days", 14, "days to show, from the most recent (0 = all)")
	asCSV := fs.Bool("csv", false, "output CSV")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	st, err := store.New(cfg.DataDir)
	if err != nil {
		return err
	}
	all, err := st.LoadDaily()
	if err != nil {
		return err
	}
	if len(all) == 0 {
		fmt.Println("no data: run `powcon-plus run` to collect samples.")
		return nil
	}
	// Newest first, like the window's table: --days keeps the most recent ones.
	all = report.MostRecent(all, *days)
	if *asCSV {
		return report.Write(os.Stdout, all, cfg.PricePerKWh)
	}
	var tot float64
	sym := currency.Symbol(cfg.Currency)
	fmt.Printf("%-12s %9s %9s %9s %9s %9s %9s\n", "date", "kWh", sym, "avg W", "peak W", "min W", "samples")
	for _, d := range all {
		tot += d.Wh
		fmt.Printf("%-12s %9.3f %9.3f %9.0f %9.0f %9.0f %9d\n",
			d.Date, d.Wh/1000, d.Cost(cfg.PricePerKWh), d.AvgW(), d.MaxW, d.MinW, d.Samples)
	}
	fmt.Printf("%-12s %9.3f %9.3f\n", "TOTAL", tot/1000, tot/1000*cfg.PricePerKWh)
	return nil
}

func cmdSensors(args []string) error {
	fs := flag.NewFlagSet("sensors", flag.ContinueOnError)
	configPath := fs.String("config", "", "configuration file")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	dir, err := psu.Detect(cfg.Sensor)
	if err != nil {
		return err
	}
	rd, err := psu.New(dir)
	if err != nil {
		return err
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"dir": dir, "channels": rd.Channel, "labels": rd.Labels})
	}
	fmt.Println("hwmon:", dir)
	keys := make([]string, 0, len(rd.Labels))
	for k := range rd.Labels {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		fmt.Printf("  %-10s %s\n", k, rd.Labels[k])
	}
	return nil
}

// cmdCheck verifies the system dependencies and prints how to install what is
// missing. It returns an error when something essential is absent, so that a
// script can stop on it.
func cmdCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	build := fs.Bool("build", false, "also check the toolchain needed to compile")
	gui := fs.String("gui", "", "GUI binary to inspect for unresolved libraries")
	if err := fs.Parse(args); err != nil {
		return err
	}
	fmt.Printf("powcon-plus %s - system dependencies\n\n", version.Version)

	found := deps.Check(deps.Options{Build: *build, GUIBinary: *gui})
	deps.Report(os.Stdout, found)

	missing := deps.MissingEssential(found)
	if len(missing) == 0 {
		fmt.Println("\nready: nothing essential is missing")
		return nil
	}
	names := make([]string, 0, len(missing))
	for _, f := range missing {
		names = append(names, f.Name)
	}
	return fmt.Errorf("missing essential dependencies: %s", strings.Join(names, ", "))
}

func cmdCtl(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: powcon-plus ctl <cmd> [k=v ...] (status|config|pause|resume|label-mode|alert|ui-language|price|autostart-status|autostart-set enabled=1|quit)")
	}
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	req := ctl.Request{Cmd: args[0], Args: map[string]any{}}
	for _, kv := range args[1:] {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("argument %q: expected key=value", kv)
		}
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			req.Args[k] = n
		} else {
			req.Args[k] = v
		}
	}
	resp, err := ctl.Call(filepath.Join(cfg.DataDir, "control.sock"), req)
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Err)
	}
	if len(resp.Data) > 0 {
		fmt.Println(string(resp.Data))
	}
	return nil
}

func cmdInstallAutostart(args []string) error {
	fs := flag.NewFlagSet("install-autostart", flag.ContinueOnError)
	mode := fs.String("mode", "xdg", "xdg | systemd")
	execPath := fs.String("exec", "", "executable to run (default: the current one)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	exe := *execPath
	switch *mode {
	case "xdg":
		p, err := autostart.InstallXDG(exe, nil)
		if err != nil {
			return err
		}
		fmt.Println("written:", p)
		fmt.Println("Active from the next graphical login (or after logout/login).")
	case "systemd":
		p, err := autostart.InstallSystemd(exe, nil)
		if err != nil {
			return err
		}
		fmt.Println("written:", p)
		if err := autostart.SystemctlUser("daemon-reload"); err != nil {
			return err
		}
		if err := autostart.SystemctlUser("enable", "powcon-plus.service"); err != nil {
			return err
		}
		fmt.Println("enabled: systemctl --user start powcon-plus")
	default:
		return fmt.Errorf("invalid mode %q (xdg|systemd)", *mode)
	}
	return nil
}

func cmdUninstallAutostart(args []string) error {
	fs := flag.NewFlagSet("uninstall-autostart", flag.ContinueOnError)
	mode := fs.String("mode", "xdg", "xdg | systemd")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch *mode {
	case "xdg":
		removed, err := autostart.RemoveXDG()
		if err != nil {
			return err
		}
		fmt.Println("removed:", removed)
	case "systemd":
		p, err := autostart.UnitPath()
		if err != nil {
			return err
		}
		autostart.SystemctlUser("disable", "powcon-plus.service")
		autostart.SystemctlUser("stop", "powcon-plus.service")
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		autostart.SystemctlUser("daemon-reload")
		fmt.Println("removed:", p)
	default:
		return fmt.Errorf("invalid mode %q (xdg|systemd)", *mode)
	}
	return nil
}

// cmdUninstall removes the daemon (if running), the autostart entries, the
// installed binaries and, with --purge, the data and configuration dirs.
func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	purge := fs.Bool("purge", false, "also remove data and configuration")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	var removed []string
	add := func(s string) {
		removed = append(removed, s)
		fmt.Println(" -", s)
	}

	// 1. stop the running daemon, if any
	sock := filepath.Join(cfg.DataDir, "control.sock")
	if _, serr := os.Stat(sock); serr == nil {
		if _, qerr := ctl.Call(sock, ctl.Request{Cmd: "quit"}); qerr == nil {
			add("daemon running: stopped")
		}
		time.Sleep(300 * time.Millisecond) // let the socket close
	}

	// 2. autostart entries (XDG and systemd user unit)
	if ok, p := autostart.XDGInstalled(); ok {
		if r, rerr := autostart.RemoveXDG(); rerr == nil && r {
			add("autostart XDG: " + p)
		}
	}
	if unit, uerr := autostart.UnitPath(); uerr == nil {
		if _, serr := os.Stat(unit); serr == nil {
			autostart.SystemctlUser("disable", autostart.AppID+".service")
			autostart.SystemctlUser("stop", autostart.AppID+".service")
			if rerr := os.Remove(unit); rerr == nil {
				add("unit systemd: " + unit)
				autostart.SystemctlUser("daemon-reload")
			}
		}
	}

	// 3. binaries: the current executable and the optional "-gui" sibling
	if exe, eerr := autostart.SelfPath(); eerr == nil {
		for _, p := range []string{exe, exe + "-gui"} {
			if rerr := os.Remove(p); rerr == nil {
				add("binary: " + p)
			}
		}
	}

	// 4. data and configuration, only with --purge
	if *purge {
		if rerr := os.RemoveAll(cfg.DataDir); rerr == nil {
			add("data: " + cfg.DataDir)
		}
		cfgDir := filepath.Dir(config.Path())
		if rerr := os.RemoveAll(cfgDir); rerr == nil {
			add("configuration: " + cfgDir)
		}
	} else {
		fmt.Println("Data (" + cfg.DataDir + ") and configuration kept; use --purge to remove them.")
	}

	if len(removed) == 0 {
		fmt.Println("nothing to remove (or already absent).")
	}
	return nil
}

func cmdAutostartStatus(args []string) error {
	ok, path := autostart.XDGInstalled()
	fmt.Printf("XDG autostart: %v  (%s)\n", ok, path)
	if _, err := exec.LookPath("systemctl"); err == nil {
		unit := filepath.Join(mustConfigDir(), "systemd", "user", autostart.AppID+".service")
		fmt.Println("systemd user unit:", fileExistsOrNot(unit))
	}
	return nil
}

func cmdInitConfig(args []string) error {
	fs := flag.NewFlagSet("init-config", flag.ContinueOnError)
	out := fs.String("out", config.Path(), "where to write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := config.WriteSample(*out); err != nil {
		return err
	}
	fmt.Println("written:", *out)
	return nil
}

// --- helper ---

func fmtDur(secs float64) string {
	d := time.Duration(secs * float64(time.Second))
	if d < time.Hour {
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func fileExistsOrNot(p string) string {
	if _, err := os.Stat(p); err == nil {
		return p + " (present)"
	}
	return "absent"
}

func mustConfigDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return "."
	}
	return d
}
