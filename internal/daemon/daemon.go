// Package daemon contains the collection loop, the tray icon and the control
// channel. It is meant to run inside the user's graphical session.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"powcon-plus/internal/autostart"
	"powcon-plus/internal/config"
	"powcon-plus/internal/ctl"
	"powcon-plus/internal/energy"
	"powcon-plus/internal/notify"
	"powcon-plus/internal/psu"
	"powcon-plus/internal/status"
	"powcon-plus/internal/store"
	"powcon-plus/internal/tray"
)

// Startup options.
type Options struct {
	ConfigPath string
	NoTray     bool
	NoStore    bool
	Verbose    bool
}

// App is the daemon.
type App struct {
	cfg     config.Config
	cfgPath string
	opts    Options
	log     *log.Logger

	rd     *psu.Reader
	sensor string
	st     *store.Store
	acc    *energy.Accumulator

	item   *tray.Item
	ctlSrv *ctl.Server

	mu         sync.RWMutex
	last       *psu.Sample
	paused     bool
	labelMode  config.LabelMode
	alertW     float64
	overSince  time.Time
	lastAlert  time.Time
	lastLabel  string
	lastStore  time.Time
	lastSave   time.Time
	sensorLost bool

	subMu sync.Mutex
	subs  map[chan status.Snapshot]struct{}

	stopOnce sync.Once
	stop     chan struct{}
}

// New prepares the daemon (config, sensor, store, lock, socket).
func New(o Options) (*App, error) {
	a := &App{
		opts: o,
		log:  log.New(os.Stderr, "powcon: ", 0),
		subs: map[chan status.Snapshot]struct{}{},
		stop: make(chan struct{}),
	}
	path := o.ConfigPath
	if path == "" {
		path = config.Path()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	a.cfg = cfg
	a.cfgPath = path
	a.labelMode = cfg.LabelMode
	a.alertW = cfg.AlertPowerW

	dir, err := psu.Detect(cfg.Sensor)
	if err != nil {
		return nil, err
	}
	rd, err := psu.New(dir)
	if err != nil {
		return nil, err
	}
	a.rd = rd
	a.sensor = dir

	st, err := store.New(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	a.st = st

	if err := a.loadHistory(); err != nil && o.Verbose {
		a.log.Printf("storico: %v", err)
	}
	a.acc = energy.NewAccumulator(3*cfg.PollInterval.Duration, 720, 2*time.Second)
	if days, err := st.LoadDaily(); err == nil {
		a.acc.SetDays(days)
	}
	return a, nil
}

func (a *App) loadHistory() error { return nil }

// ControlSocketPath is the path of the control socket.
func (a *App) ControlSocketPath() string {
	return filepath.Join(a.cfg.DataDir, "control.sock")
}

// Run executes until the context is cancelled or Stop() is called.
func (a *App) Run(ctx context.Context) error {
	if a.cfg.SingleInstance {
		lk, err := store.AcquireLock(filepath.Join(a.cfg.DataDir, "lock"))
		if err != nil {
			return err
		}
		defer lk.Release()
	}

	srv, err := ctl.NewServer(a.ControlSocketPath())
	if err != nil {
		return err
	}
	a.ctlSrv = srv
	defer func() {
		srv.Close()
		if a.opts.Verbose {
			a.log.Printf("socket di controllo rimosso")
		}
	}()
	a.registerCtl(srv)
	go func() {
		if err := srv.Serve(); err != nil {
			a.log.Printf("ctl: %v", err)
		}
	}()

	if !a.opts.NoTray {
		go a.runTray()
	}
	if a.cfg.StartWindow {
		go a.launchWindow()
	}

	go a.alertOnStartup()

	tick := time.NewTicker(a.cfg.PollInterval.Duration)
	defer tick.Stop()
	// Take the first sample immediately.
	a.collect()

	for {
		select {
		case <-ctx.Done():
			a.shutdown()
			return nil
		case <-a.stop:
			a.shutdown()
			return nil
		case <-tick.C:
			a.collect()
		}
	}
}

// Stop requests an orderly shutdown.
func (a *App) Stop() { a.stopOnce.Do(func() { close(a.stop) }) }

func (a *App) shutdown() {
	if err := a.persistDaily(); err != nil {
		a.log.Printf("salvataggio riepiloghi: %v", err)
	}
	if a.item != nil {
		a.item.SetStatus("Passive")
	}
}

func (a *App) alertOnStartup() {
	time.Sleep(2 * time.Second)
	if _, ok := psu.Available(a.cfg.Sensor); !ok {
		notify.Send("powcon-plus", "PSU non rilevato",
			"Il sensore hwmon del Corsair HX non è presente: controlla il cavo USB.", 0)
	}
}

// --- collection ---

func (a *App) collect() {
	smp, err := a.rd.Read()
	a.mu.Lock()
	if err != nil {
		if !a.sensorLost {
			a.log.Printf("lettura sensore: %v", err)
			a.sensorLost = true
		}
		a.pausedKeepState()
		a.mu.Unlock()
		a.refreshTray(nil)
		return
	}
	if a.sensorLost {
		a.log.Printf("sensore di nuovo disponibile")
		a.sensorLost = false
	}
	a.last = smp
	if !a.paused {
		a.acc.Add(smp)
	}
	paused := a.paused
	shouldStore := a.cfg.SampleStore && !a.opts.NoStore && !paused &&
		time.Since(a.lastStore) >= a.cfg.SampleEvery.Duration
	if shouldStore {
		a.lastStore = time.Now()
	}
	shouldSave := time.Since(a.lastSave) >= time.Minute
	if shouldSave {
		a.lastSave = time.Now()
	}
	a.alertCheck(smp)
	a.mu.Unlock()

	if shouldStore {
		if err := a.st.AppendSample(smp); err != nil {
			a.log.Printf("registrazione campione: %v", err)
		}
	}
	if shouldSave {
		if err := a.persistDaily(); err != nil {
			a.log.Printf("riepiloghi: %v", err)
		}
	}
	a.refreshTray(smp)
	a.publish(a.snapshot())
}

func (a *App) pausedKeepState() {}

func (a *App) persistDaily() error {
	return a.st.SaveDaily(a.acc.Days())
}

func (a *App) alertCheck(smp *psu.Sample) {
	if a.alertW <= 0 {
		return
	}
	if smp.PTot < a.alertW {
		a.overSince = time.Time{}
		return
	}
	if a.overSince.IsZero() {
		a.overSince = smp.Time
		return
	}
	if smp.Time.Sub(a.overSince) < a.cfg.AlertSustain.Duration {
		return
	}
	if time.Since(a.lastAlert) < a.cfg.AlertCool.Duration {
		return
	}
	a.lastAlert = smp.Time
	msg := fmt.Sprintf("Assorbio %.0f W da rete (soglia %.0f W) da %s.",
		smp.PTot, a.alertW, formatDur(smp.Time.Sub(a.overSince)))
	a.log.Printf("allerta: %s", msg)
	notify.SendWithIcon("powcon-plus", "Consumo elevato", msg, "dialog-warning", 0)
}

// --- state / snapshot ---

func (a *App) snapshot() status.Snapshot {
	a.mu.RLock()
	smp := a.last
	paused := a.paused
	mode := a.labelMode
	alertW := a.alertW
	a.mu.RUnlock()
	return status.Build(smp, a.acc, a.sensor, paused, mode, alertW, a.cfg.EurPerKWh,
		lastDays(a.acc.Days(), 14), a.acc.History())
}

func lastDays(d []energy.Day, n int) []energy.Day {
	if len(d) <= n {
		return d
	}
	return d[:n]
}

// formatDur formats a short duration in Italian.
func formatDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02d", int(d.Hours()), int(d.Minutes())%60)
	}
}

// --- tray ---

func (a *App) runTray() {
	item, err := tray.New(tray.Options{ID: "powcon-plus", Title: "Consumi workstation"})
	if err != nil {
		a.log.Printf("tray non disponibile (%v): esecuzione senza icona", err)
		return
	}
	if err := item.ClaimName(); err != nil {
		a.log.Printf("tray: %v", err)
		return
	}
	a.item = item
	pal := tray.DefaultPalette()
	item.SetPalette(pal)
	item.SetIconState(tray.StateNormal)
	item.OnActivate(func(x, y int32) { a.launchWindow() })
	item.OnSecondaryActivate(func(x, y int32) { a.cycleLabel() })
	item.OnScroll(func(delta int32, orientation string) { a.cycleLabel() })
	a.rebuildMenu()

	delay := 2 * time.Second
	for attempt := 1; ; attempt++ {
		if err := item.Register(); err == nil {
			if a.opts.Verbose {
				a.log.Printf("tray registrato (tentativo %d)", attempt)
			}
			break
		} else if attempt < 30 {
			if a.opts.Verbose {
				a.log.Printf("watcher tray non pronto: %v", err)
			}
		} else {
			a.log.Printf("impossibile registrare l'icona di tray: %v", err)
			break
		}
		select {
		case <-a.stop:
			return
		case <-time.After(delay):
		}
		if delay < 30*time.Second {
			delay *= 2
		}
	}
	a.refreshTray(a.lastSample())
}

func (a *App) lastSample() *psu.Sample {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.last
}

func (a *App) refreshTray(smp *psu.Sample) {
	if a.item == nil {
		return
	}
	snap := a.snapshot()
	label := snap.Label(a.cfg)
	if label != a.lastLabel {
		a.item.SetLabel(label)
		a.lastLabel = label
	}
	if snap.Online {
		a.item.SetToolTip("Consumi workstation", a.toolTipText(snap))
	} else {
		a.item.SetLabel("n/d")
		a.item.SetToolTip("Consumi workstation", "Sensore PSU non disponibile")
	}
	switch {
	case a.sensorLost:
		a.item.SetIconState(tray.StateAlert)
	case a.paused:
		a.item.SetIconState(tray.StatePaused)
	default:
		a.item.SetIconState(tray.StateNormal)
	}
}

func (a *App) toolTipText(s status.Snapshot) string {
	return fmt.Sprintf(
		"Potenza AC: %.0f W\nOggi: %.2f kWh (%.2f €)\nMedia oggi: %.0f W · picco %.0f W\n"+
			"+12V %.1f A · +5V %.1f A · +3.3V %.1f A\nVRM %.1f °C · case %.1f °C · ventola %.0f RPM",
		s.W, s.TodayWh/1000, s.TodayCostEuro, s.TodayAvgW, s.TodayMaxW,
		s.I12, s.I5, s.I33, s.TVRM, s.TCase, s.Fan)
}

func (a *App) cycleLabel() {
	order := []config.LabelMode{config.ModePower, config.ModeEnergy, config.ModeBoth, config.ModeNone}
	a.mu.Lock()
	cur := a.labelMode
	next := order[0]
	for i, m := range order {
		if m == cur {
			next = order[(i+1)%len(order)]
			break
		}
	}
	a.labelMode = next
	a.mu.Unlock()
	a.lastLabel = ""
	a.refreshTray(a.lastSample())
	a.rebuildMenu()
}

func (a *App) rebuildMenu() {
	if a.item == nil {
		return
	}
	snap := a.snapshot()
	installed, path := autostart.XDGInstalled()
	a.item.Menu().Rebuild(func(b *tray.MenuBuilder) {
		b.Disabled(fmt.Sprintf("%.0f W · oggi %.2f kWh", snap.W, snap.TodayWh/1000))
		b.Separator()
		b.Item("window", "Apri dettaglio consumi", func() { a.launchWindow() })
		b.Item("report", "Report in terminale (notifica)", func() {
			notify.Send("powcon-plus", "Report ultimi 7 giorni", a.reportText(7), 0)
		})
		b.Check("pause", "Pausa raccolta", snap.Paused, func() { a.togglePause() })
		b.Sub("mode", "Etichetta nella barra", func(sb *tray.MenuBuilder) {
			sb.Radio("mode-power", "Potenza (W)", snap.LabelMode == config.ModePower,
				func() { a.setLabelMode(config.ModePower) })
			sb.Radio("mode-energy", "Energia di oggi (Wh/kWh)", snap.LabelMode == config.ModeEnergy,
				func() { a.setLabelMode(config.ModeEnergy) })
			sb.Radio("mode-both", "Potenza + energia", snap.LabelMode == config.ModeBoth,
				func() { a.setLabelMode(config.ModeBoth) })
			sb.Radio("mode-none", "Solo icona", snap.LabelMode == config.ModeNone,
				func() { a.setLabelMode(config.ModeNone) })
		})
		b.Sub("alert", "Avviso consumo elevato", func(sb *tray.MenuBuilder) {
			for _, w := range []float64{0, 400, 600, 800, 1000} {
				w := w
				label := "Disattivo"
				if w > 0 {
					label = fmt.Sprintf("> %.0f W", w)
				}
				sb.Radio(fmt.Sprintf("alert-%v", w), label, a.alertThreshold() == w, func() {
					a.setAlert(w)
				})
			}
		})
		b.Sub("autostart", "Avvio automatico", func(sb *tray.MenuBuilder) {
			sb.Check("as-xdg", "All'avvio della sessione (XDG)", installed, func() {
				if installed {
					if _, err := autostart.RemoveXDG(); err != nil {
						a.log.Printf("rimozione autostart: %v", err)
					}
				} else {
					if p, err := autostart.InstallXDG("", nil); err != nil {
						a.log.Printf("installazione autostart: %v", err)
					} else {
						a.log.Printf("autostart XDG scritto in %s", p)
					}
				}
				a.rebuildMenu()
			})
			sb.Disabled(path)
		})
		b.Separator()
		b.Item("save", "Salva configurazione", func() {
			if err := config.Save(a.cfgPath, a.effectiveConfig()); err != nil {
				a.log.Printf("salvataggio configurazione: %v", err)
			}
		})
		b.Item("quit", "Esci", func() { a.Stop() })
	})
}

func (a *App) reportText(days int) string {
	all := a.acc.Days()
	var lines []string
	total := 0.0
	for i, d := range all {
		if i >= days {
			break
		}
		total += d.Wh
		lines = append(lines, fmt.Sprintf("%s  %6.2f kWh  %5.2f €  picco %.0f W",
			d.Date, d.Wh/1000, d.Costo(a.cfg.EurPerKWh), d.MaxW))
	}
	if len(lines) == 0 {
		return "Nessun dato raccolto."
	}
	return fmt.Sprintf("%v\nTotale %.2f kWh (%.2f €)", lines, total/1000, total/1000*a.cfg.EurPerKWh)
}

func (a *App) effectiveConfig() config.Config {
	c := a.cfg
	c.LabelMode = a.labelMode
	c.AlertPowerW = a.alertW
	return c
}

// --- commands ---

func (a *App) togglePause() {
	a.mu.Lock()
	a.paused = !a.paused
	paused := a.paused
	a.mu.Unlock()
	a.refreshTray(a.lastSample())
	a.rebuildMenu()
	if a.opts.Verbose {
		a.log.Printf("raccolta: pausa=%v", paused)
	}
}

func (a *App) setLabelMode(m config.LabelMode) {
	a.mu.Lock()
	a.labelMode = m
	a.mu.Unlock()
	a.lastLabel = ""
	a.refreshTray(a.lastSample())
	a.rebuildMenu()
}

func (a *App) setAlert(w float64) {
	a.mu.Lock()
	a.alertW = w
	a.overSince = time.Time{}
	a.mu.Unlock()
	a.rebuildMenu()
}

func (a *App) alertThreshold() float64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.alertW
}

// launchWindow opens the detail window in a detached process. When the
// running build is headless, it prefers the "-gui" sibling binary, which
// carries the window subcommand.
func (a *App) launchWindow() {
	exe, err := autostart.SelfPath()
	if err != nil {
		return
	}
	if guiExe := filepath.Join(filepath.Dir(exe), filepath.Base(exe)+"-gui"); pathExists(guiExe) {
		exe = guiExe
	}
	cmd := exec.Command(exe, "window")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		a.log.Printf("apertura finestra: %v", err)
	}
}

// pathExists reports whether path exists (any file kind).
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// --- streaming to GUI/CLI ---

func (a *App) subscribe() (<-chan status.Snapshot, func()) {
	ch := make(chan status.Snapshot, 4)
	a.subMu.Lock()
	a.subs[ch] = struct{}{}
	a.subMu.Unlock()
	return ch, func() {
		a.subMu.Lock()
		delete(a.subs, ch)
		a.subMu.Unlock()
	}
}

func (a *App) publish(s status.Snapshot) {
	a.subMu.Lock()
	defer a.subMu.Unlock()
	for ch := range a.subs {
		select {
		case ch <- s:
		default:
			// slow subscriber: drop
		}
	}
}

// --- ctl ---

// autostartStatusMap reports the current autostart state (XDG entry and
// optional systemd user unit).
func autostartStatusMap() map[string]any {
	xdg, path := autostart.XDGInstalled()
	unit := false
	unitPath := ""
	if up, err := autostart.UnitPath(); err == nil {
		unitPath = up
		if _, statErr := os.Stat(unitPath); statErr == nil {
			unit = true
		}
	}
	return map[string]any{"xdg": xdg, "path": path, "unit": unit, "unit_path": unitPath}
}

// parseEnabled extracts a boolean from ctl args ("enabled"), tolerating the
// JSON number/string forms produced by the CLI.
func parseEnabled(args map[string]any) bool {
	switch v := args["enabled"].(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		switch v {
		case "1", "true", "on", "yes", "si", "sì":
			return true
		}
	}
	return false
}

func (a *App) registerCtl(s *ctl.Server) {
	s.Handle("status", func(map[string]any) (any, error) { return a.snapshot(), nil })
	s.Handle("pause", func(map[string]any) (any, error) {
		a.mu.Lock()
		a.paused = true
		a.mu.Unlock()
		a.refreshTray(a.lastSample())
		a.rebuildMenu()
		return map[string]any{"paused": true}, nil
	})
	s.Handle("resume", func(map[string]any) (any, error) {
		a.mu.Lock()
		a.paused = false
		a.mu.Unlock()
		a.refreshTray(a.lastSample())
		a.rebuildMenu()
		return map[string]any{"paused": false}, nil
	})
	s.Handle("label-mode", func(args map[string]any) (any, error) {
		m, _ := args["mode"].(string)
		if m == "" {
			return nil, errors.New("argomento mode mancante")
		}
		a.setLabelMode(config.LabelMode(m))
		return map[string]any{"label_mode": m}, nil
	})
	s.Handle("alert", func(args map[string]any) (any, error) {
		if v, ok := args["w"]; ok {
			switch t := v.(type) {
			case float64:
				a.setAlert(t)
			case string:
				if t == "off" {
					a.setAlert(0)
				} else {
					return nil, errors.New("w deve essere un numero o \"off\"")
				}
			}
		}
		return map[string]any{"alert_w": a.alertThreshold()}, nil
	})
	s.Handle("quit", func(map[string]any) (any, error) {
		go a.Stop()
		return map[string]any{"ok": true}, nil
	})
	s.Handle("menu-refresh", func(map[string]any) (any, error) {
		a.rebuildMenu()
		return nil, nil
	})
	s.Handle("config", func(map[string]any) (any, error) { return a.effectiveConfig(), nil })
	s.Handle("save-config", func(map[string]any) (any, error) {
		return nil, config.Save(a.cfgPath, a.effectiveConfig())
	})

	// Autostart is toggled from the UI (window checkbox / tray menu item);
	// the XDG entry always points to the executable that is currently running.
	s.Handle("autostart-status", func(map[string]any) (any, error) {
		return autostartStatusMap(), nil
	})
	s.Handle("autostart-set", func(args map[string]any) (any, error) {
		enabled := parseEnabled(args)
		if enabled {
			if _, err := autostart.InstallXDG("", nil); err != nil {
				return nil, err
			}
		} else {
			if _, err := autostart.RemoveXDG(); err != nil {
				return nil, err
			}
		}
		a.rebuildMenu()
		return autostartStatusMap(), nil
	})

	s.HandleStream("subscribe", func(args map[string]any, enc *json.Encoder, deadline time.Duration) error {
		ch, cancel := a.subscribe()
		defer cancel()
		deadlineAt := time.Time{}
		if deadline > 0 {
			deadlineAt = time.Now().Add(deadline)
		}
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case s := <-ch:
				if err := enc.Encode(ctl.Response{OK: true, Data: mustMarshal(s)}); err != nil {
					return nil
				}
			case <-t.C:
				// keep the connection alive even without samples
			case <-a.stop:
				return nil
			}
			if !deadlineAt.IsZero() && time.Now().After(deadlineAt) {
				return nil
			}
		}
	})
}

// mustMarshal packs the payload into the Response (streaming helper).
func mustMarshal(s status.Snapshot) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return b
}

var _ = json.Marshal
