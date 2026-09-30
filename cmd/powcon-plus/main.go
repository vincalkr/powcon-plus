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
	"encoding/csv"
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

	"powcon-plus/internal/autostart"
	"powcon-plus/internal/config"
	"powcon-plus/internal/ctl"
	"powcon-plus/internal/daemon"
	"powcon-plus/internal/energy"
	"powcon-plus/internal/psu"
	"powcon-plus/internal/status"
	"powcon-plus/internal/store"
)

const version = "0.1.0"

func main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
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
	case "install-autostart":
		err = cmdInstallAutostart(args)
	case "uninstall-autostart":
		err = cmdUninstallAutostart(args)
	case "autostart-status":
		err = cmdAutostartStatus(args)
	case "init-config":
		err = cmdInitConfig(args)
	case "version", "--version":
		fmt.Printf("powcon-plus %s\n", version)
	case "help", "-h", "--help":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "comando sconosciuto: %s\n\n", cmd)
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "errore: %v\n", err)
		os.Exit(1)
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `powcon-plus — consumi della workstation dal PSU Corsair (hx1200i)

Uso:
  powcon-plus run [flag]            avvia il daemon con icona di tray
  powcon-plus status [--json]       lettura istantanea (usa il daemon se attivo)
  powcon-plus report [--days N] [--csv]
                                    kWh/costo per giorno
  powcon-plus window                finestra di dettaglio (build -tags gui)
  powcon-plus ctl <cmd> [k=v...]    controlla il daemon (status|pause|resume|label-mode|alert|autostart-set enabled=1|quit)
  powcon-plus sensors               elenca i canali hwmon del PSU rilevati
  powcon-plus install-autostart [--mode xdg|systemd]
  powcon-plus uninstall-autostart
  powcon-plus autostart-status
  powcon-plus init-config           scrive ~/.config/powcon-plus/config.toml

Flag comuni:
  --config PATH      file di configurazione (default ~/.config/powcon-plus/config.toml)
  --no-tray          non creare l'icona di tray
  --no-store         non scrivere campioni su disco
  --verbose          log dettagliato

File:
  ~/.config/powcon-plus/config.toml   configurazione
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
	fs.StringVar(&f.config, "config", "", "file di configurazione")
	fs.BoolVar(&f.noTray, "no-tray", false, "nessuna icona di tray")
	fs.BoolVar(&f.noStore, "no-store", false, "nessuna scrittura di campioni")
	fs.BoolVar(&f.verbose, "verbose", false, "log dettagliato")
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
	configPath := fs.String("config", "", "file di configurazione")
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
	snap := status.Build(s, acc, dir, false, cfg.LabelMode, 0, cfg.EurPerKWh, nil, nil)
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
	fmt.Printf("sensore:      %s (%s)\n", s.Sensor, map[bool]string{true: "attivo", false: "NON attivo"}[s.Online])
	fmt.Printf("potenza AC:   %6.1f W   [%s]\n", s.W, s.Label(cfg))
	fmt.Printf("ingresso:     %6.1f V AC\n", s.VIn)
	fmt.Printf("+12V:         %6.2f V  %6.2f A  %6.1f W\n", s.V12, s.I12, s.P12)
	fmt.Printf("+5V:          %6.2f V  %6.2f A  %6.1f W\n", s.V5, s.I5, s.P5)
	fmt.Printf("+3.3V:        %6.2f V  %6.2f A  %6.1f W\n", s.V33, s.I33, s.P33)
	fmt.Printf("temperature:  VRM %.1f °C   case %.1f °C   ventola %.0f RPM (%.0f%%)\n",
		s.TVRM, s.TCase, s.Fan, s.PWM)
	if s.SessionSeconds > 0 {
		fmt.Printf("sessione:     %.2f kWh in %s (media %.0f W, picco %.0f W)\n",
			s.SessionWh/1000, fmtDur(s.SessionSeconds), s.SessionAvgW, s.SessionMaxW)
	}
	if s.TodaySamples > 0 {
		fmt.Printf("oggi:         %.2f kWh  (%.2f €)  media %.0f W  picco %.0f W  min %.0f W\n",
			s.TodayWh/1000, s.TodayCostEuro, s.TodayAvgW, s.TodayMaxW, s.TodayMinW)
	}
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	configPath := fs.String("config", "", "file di configurazione")
	days := fs.Int("days", 14, "giorni da mostrare")
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
		fmt.Println("nessun dato: avvia `powcon-plus run` per raccogliere campioni.")
		return nil
	}
	if len(all) > *days {
		all = all[:*days]
	}
	w := csv.NewWriter(os.Stdout)
	if *asCSV {
		w.Write([]string{"data", "kwh", "euro", "media_w", "picco_w", "min_w", "campioni"})
		for _, d := range all {
			w.Write([]string{d.Date, fmt.Sprintf("%.3f", d.Wh/1000),
				fmt.Sprintf("%.3f", d.Costo(cfg.EurPerKWh)), fmt.Sprintf("%.0f", d.AvgW()),
				fmt.Sprintf("%.0f", d.MaxW), fmt.Sprintf("%.0f", d.MinW), strconv.Itoa(d.Samples)})
		}
		w.Flush()
		return w.Error()
	}
	var tot float64
	fmt.Printf("%-12s %9s %9s %9s %9s %9s %9s\n", "data", "kWh", "€", "media W", "picco W", "min W", "campioni")
	for _, d := range all {
		tot += d.Wh
		fmt.Printf("%-12s %9.3f %9.3f %9.0f %9.0f %9.0f %9d\n",
			d.Date, d.Wh/1000, d.Costo(cfg.EurPerKWh), d.AvgW(), d.MaxW, d.MinW, d.Samples)
	}
	fmt.Printf("%-12s %9.3f %9.3f\n", "TOTALE", tot/1000, tot/1000*cfg.EurPerKWh)
	return nil
}

func cmdSensors(args []string) error {
	fs := flag.NewFlagSet("sensors", flag.ContinueOnError)
	configPath := fs.String("config", "", "file di configurazione")
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

func cmdCtl(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("uso: powcon-plus ctl <cmd> [k=v ...] (status|pause|resume|label-mode|alert|autostart-status|autostart-set enabled=1|quit)")
	}
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	req := ctl.Request{Cmd: args[0], Args: map[string]any{}}
	for _, kv := range args[1:] {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("argomento %q: atteso chiave=valore", kv)
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
	execPath := fs.String("exec", "", "eseguibile da lanciare (default: quello corrente)")
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
		fmt.Println("scritto:", p)
		fmt.Println("Attivo dal prossimo accesso grafico (o con logout/login).")
	case "systemd":
		p, err := autostart.InstallSystemd(exe, nil)
		if err != nil {
			return err
		}
		fmt.Println("scritto:", p)
		if err := autostart.SystemctlUser("daemon-reload"); err != nil {
			return err
		}
		if err := autostart.SystemctlUser("enable", "powcon-plus.service"); err != nil {
			return err
		}
		fmt.Println("abilitato: systemctl --user start powcon-plus")
	default:
		return fmt.Errorf("mode %q non valido (xdg|systemd)", *mode)
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
		fmt.Println("rimosso:", removed)
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
		fmt.Println("rimosso:", p)
	default:
		return fmt.Errorf("mode %q non valido (xdg|systemd)", *mode)
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
	out := fs.String("out", config.Path(), "dove scrivere")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := config.WriteSample(*out); err != nil {
		return err
	}
	fmt.Println("scritto:", *out)
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
		return p + " (presente)"
	}
	return "assente"
}

func mustConfigDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return "."
	}
	return d
}
