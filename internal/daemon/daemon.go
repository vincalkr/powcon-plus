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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"powcon-plus/internal/autostart"
	"powcon-plus/internal/config"
	"powcon-plus/internal/ctl"
	"powcon-plus/internal/currency"
	"powcon-plus/internal/energy"
	"powcon-plus/internal/loc"
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
	language   string
	cat        *loc.Catalog
	price      float64
	currency   string

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
	a.price = cfg.PricePerKWh
	a.currency = cfg.Currency

	// Resolve the UI language: explicit config value, then locale, then English.
	lang := cfg.Language
	if lang == "" {
		lang = loc.Auto()
	}
	if !loc.IsValid(lang) {
		lang = "en"
	}
	a.language = lang
	a.cat = loc.Load(lang)

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
		a.log.Printf("history: %v", err)
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
			a.log.Printf("control socket removed")
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
		a.log.Printf("saving daily rollups: %v", err)
	}
	if a.item != nil {
		a.item.SetStatus("Passive")
	}
}

func (a *App) alertOnStartup() {
	time.Sleep(2 * time.Second)
	if _, ok := psu.Available(a.cfg.Sensor); !ok {
		cat := a.catNow()
		if err := notify.Send("powcon-plus", cat.Get("notif.psu.title"), cat.Get("notif.psu.body"), 0); err != nil {
			a.log.Printf("sensor notification failed: %v", err)
		}
	}
}

// --- collection ---

func (a *App) collect() {
	smp, err := a.rd.Read()
	a.mu.Lock()
	if err != nil {
		if !a.sensorLost {
			a.log.Printf("sensor read: %v", err)
			a.sensorLost = true
		}
		a.pausedKeepState()
		a.mu.Unlock()
		a.refreshTray(nil)
		return
	}
	if a.sensorLost {
		a.log.Printf("sensor available again")
		a.sensorLost = false
	}
	a.last = smp
	if !a.paused {
		a.acc.Add(smp)
	}
	paused := a.paused
	shouldStore := a.storesHistory() && !paused &&
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
			a.log.Printf("storing sample: %v", err)
		}
	}
	if shouldSave {
		if err := a.persistDaily(); err != nil {
			a.log.Printf("daily rollups: %v", err)
		}
	}
	a.refreshTray(smp)
	a.publish(a.snapshot())
}

func (a *App) pausedKeepState() {}

// persistDaily writes the daily rollups. It is a no-op when history is
// disabled (same switch as the raw sample files).
func (a *App) persistDaily() error {
	if !a.storesHistory() {
		return nil
	}
	return a.st.SaveDaily(a.acc.Days())
}

// storesHistory reports whether history files (raw samples and daily
// rollups) are enabled: configuration plus the --no-store flag.
func (a *App) storesHistory() bool {
	return a.cfg.SampleStore && !a.opts.NoStore
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
	// Called with a.mu held (see collect), so read the catalog directly.
	cat := a.cat
	msg := cat.Sprintf("notif.alert.body", smp.PTot, a.alertW, formatDur(smp.Time.Sub(a.overSince)))
	a.log.Printf("alert: %s", msg)
	if err := notify.SendWithIcon("powcon-plus", cat.Get("notif.alert.title"), msg, "dialog-warning", 0); err != nil {
		a.log.Printf("alert notification failed: %v", err)
	}
}

// --- state / snapshot ---

func (a *App) snapshot() status.Snapshot {
	a.mu.RLock()
	smp := a.last
	paused := a.paused
	mode := a.labelMode
	alertW := a.alertW
	price := a.price
	currency := a.currency
	a.mu.RUnlock()
	return status.Build(smp, a.acc, a.sensor, paused, mode, alertW, price, currency,
		lastDays(a.acc.Days(), 14), a.acc.History())
}

func lastDays(d []energy.Day, n int) []energy.Day {
	if len(d) <= n {
		return d
	}
	return d[:n]
}

// formatDur formats a short duration in compact form (e.g. "45s", "3 min", "2h07").
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
	item, err := tray.New(tray.Options{ID: "powcon-plus", Title: a.catNow().Get("tray.title")})
	if err != nil {
		a.log.Printf("tray unavailable (%v): running without icon", err)
		return
	}
	if err := item.ClaimName(); err != nil {
		a.log.Printf("tray: %v", err)
		return
	}
	a.item = item
	pal := tray.DefaultPalette()
	item.SetPalette(pal)
	a.applyIcon()
	item.OnActivate(func(x, y int32) { a.launchWindow() })
	item.OnSecondaryActivate(func(x, y int32) { a.cycleLabel() })
	item.OnScroll(func(delta int32, orientation string) { a.cycleLabel() })
	a.rebuildMenu()

	delay := 2 * time.Second
	for attempt := 1; ; attempt++ {
		if err := item.Register(); err == nil {
			if a.opts.Verbose {
				a.log.Printf("tray registered (attempt %d)", attempt)
			}
			break
		} else if attempt < 30 {
			if a.opts.Verbose {
				a.log.Printf("watcher tray not ready: %v", err)
			}
		} else {
			a.log.Printf("cannot register the tray icon: %v", err)
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

// catNow returns the current translation catalog (protected by a.mu).
func (a *App) catNow() *loc.Catalog {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cat
}

func (a *App) refreshTray(smp *psu.Sample) {
	if a.item == nil {
		return
	}
	snap := a.snapshot()
	cat := a.catNow()
	label := snap.Label(a.cfg)
	if label != a.lastLabel {
		a.item.SetLabel(label)
		a.lastLabel = label
	}
	if snap.Online {
		a.item.SetToolTip(cat.Get("tray.title"), cat.Sprintf("tray.tooltip",
			snap.W, snap.TodayWh/1000, snap.TodayCost, currency.Symbol(snap.Currency),
			snap.TodayAvgW, snap.TodayMaxW,
			snap.I12, snap.I5, snap.I33, snap.TVRM, snap.TCase, snap.Fan))
	} else {
		a.item.SetLabel(cat.Get("tray.offline.label"))
		a.item.SetToolTip(cat.Get("tray.title"), cat.Get("tray.offline.tooltip"))
	}
	a.applyIcon()
}

// applyIcon publishes the tray glyph. With icon_mode = "name" the icon comes
// from the desktop theme (a static glyph, so the state cannot recolour it);
// otherwise the generated pixmap reflects the state.
func (a *App) applyIcon() {
	if a.cfg.IconMode == "name" {
		a.item.SetIconName(a.cfg.IconName, "")
		return
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
	installed, _ := autostart.XDGInstalled()
	cat := a.catNow()
	a.item.Menu().Rebuild(func(b *tray.MenuBuilder) {
		b.Disabled(cat.Sprintf("tray.menu.info", snap.W, snap.TodayWh/1000))
		b.Separator()
		b.Item("window", cat.Get("tray.menu.window"), func() { a.launchWindow() })
		b.Sub("mode", cat.Get("tray.menu.mode"), func(sb *tray.MenuBuilder) {
			sb.Radio("mode-power", cat.Get("tray.menu.mode.power"), snap.LabelMode == config.ModePower,
				func() { a.setLabelMode(config.ModePower) })
			sb.Radio("mode-energy", cat.Get("tray.menu.mode.energy"), snap.LabelMode == config.ModeEnergy,
				func() { a.setLabelMode(config.ModeEnergy) })
			sb.Radio("mode-both", cat.Get("tray.menu.mode.both"), snap.LabelMode == config.ModeBoth,
				func() { a.setLabelMode(config.ModeBoth) })
			sb.Radio("mode-none", cat.Get("tray.menu.mode.none"), snap.LabelMode == config.ModeNone,
				func() { a.setLabelMode(config.ModeNone) })
		})
		b.Sub("alert", cat.Get("tray.menu.alert"), func(sb *tray.MenuBuilder) {
			for _, w := range []float64{0, 400, 600, 800, 1000} {
				w := w
				label := cat.Get("tray.menu.alert.off")
				if w > 0 {
					label = cat.Sprintf("tray.menu.alert.on", w)
				}
				sb.Radio(fmt.Sprintf("alert-%v", w), label, a.alertThreshold() == w, func() {
					a.setAlert(w)
				})
			}
		})
		b.Check("autostart", cat.Get("tray.menu.autostart"), installed, func() {
			if installed {
				if _, err := autostart.RemoveXDG(); err != nil {
					a.log.Printf("removing autostart: %v", err)
				}
			} else {
				if p, err := autostart.InstallXDG("", nil); err != nil {
					a.log.Printf("installing autostart: %v", err)
				} else {
					a.log.Printf("autostart XDG written to %s", p)
				}
			}
			a.rebuildMenu()
		})
		b.Separator()
		b.Item("quit", cat.Get("tray.menu.quit"), func() { a.Stop() })
	})
}

func (a *App) effectiveConfig() config.Config {
	a.mu.RLock()
	c := a.cfg
	c.LabelMode = a.labelMode
	c.AlertPowerW = a.alertW
	c.Language = a.language
	c.PricePerKWh = a.price
	c.Currency = a.currency
	a.mu.RUnlock()
	return c
}

// --- commands ---

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
		a.log.Printf("opening window: %v", err)
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

// parseFloat64 parses a ctl numeric argument, tolerating the string form
// produced by the CLI.
func parseFloat64(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
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
			return nil, errors.New("missing mode argument")
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
					return nil, errors.New("w must be a number or \"off\"")
				}
			}
		}
		return map[string]any{"alert_w": a.alertThreshold()}, nil
	})
	s.Handle("quit", func(map[string]any) (any, error) {
		go a.Stop()
		return map[string]any{"ok": true}, nil
	})
	s.Handle("config", func(map[string]any) (any, error) { return a.effectiveConfig(), nil })
	s.Handle("ui-language", func(args map[string]any) (any, error) {
		lang, _ := args["language"].(string)
		if !loc.IsValid(lang) {
			return nil, fmt.Errorf("language %q is not supported (it|en|de|fr|es|zh|ja)", lang)
		}
		a.mu.Lock()
		a.language = lang
		a.cat = loc.Load(lang)
		a.mu.Unlock()
		if err := config.Save(a.cfgPath, a.effectiveConfig()); err != nil {
			a.log.Printf("saving configuration: %v", err)
		}
		a.refreshTray(a.lastSample())
		a.rebuildMenu()
		return map[string]any{"language": lang}, nil
	})

	s.Handle("price", func(args map[string]any) (any, error) {
		var price float64
		hasPrice := false
		if v, ok := args["price"]; ok {
			switch t := v.(type) {
			case float64:
				price, hasPrice = t, true
			case string:
				if p, err := parseFloat64(t); err == nil {
					price, hasPrice = p, true
				} else {
					return nil, errors.New("price must be a number")
				}
			}
		}
		if hasPrice && price < 0 {
			return nil, errors.New("price must be >= 0")
		}
		cur, _ := args["currency"].(string)
		if cur != "" && !currency.IsValid(cur) {
			return nil, fmt.Errorf("currency %q is not supported (EUR|USD|GBP|CNY|JPY)", cur)
		}
		a.mu.Lock()
		if hasPrice {
			a.price = price
		}
		if cur != "" {
			a.currency = cur
		}
		p, c := a.price, a.currency
		a.mu.Unlock()
		if err := config.Save(a.cfgPath, a.effectiveConfig()); err != nil {
			a.log.Printf("saving configuration: %v", err)
		}
		a.refreshTray(a.lastSample())
		a.rebuildMenu()
		return map[string]any{"price_per_kwh": p, "currency": c}, nil
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
