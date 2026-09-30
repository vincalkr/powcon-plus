// Package gui implements the native detail window (Fyne).
// The window is a client of the daemon: it subscribes to the control socket
// ("subscribe" command, one JSON snapshot every second). Without a daemon it
// falls back to reading the hwmon sensor directly with a local accumulator,
// so it stays usable even with the daemon off.
package gui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"powcon-plus/internal/autostart"
	"powcon-plus/internal/config"
	"powcon-plus/internal/ctl"
	"powcon-plus/internal/energy"
	"powcon-plus/internal/psu"
	"powcon-plus/internal/status"
)

// Options for opening the window.
type Options struct {
	ConfigPath string // configuration file ("" = config.Path())
	DataDir    string // daemon data dir ("" = the one from the configuration)
}

const (
	windowTitle = "powcon-plus — consumi workstation"
	chartSpan   = 30 * time.Minute // time window of the live chart
	chartMaxSeg = 240              // cap on the segments drawn
	tableDays   = 14               // days in the table
	sockName    = "control.sock"
)

// Run opens the window and returns when it is closed.
func Run(o Options) error {
	cfg, err := config.Load(o.ConfigPath)
	if err != nil {
		return err
	}
	dataDir := o.DataDir
	if dataDir == "" {
		dataDir = cfg.DataDir
	}
	a := app.NewWithID("plus.powcon.window")
	w := a.NewWindow(windowTitle)
	u := newUI(a, w, cfg, filepath.Join(dataDir, sockName))
	w.SetContent(u.root)
	w.Resize(fyne.NewSize(760, 560))
	w.SetOnClosed(u.stop)
	u.start()
	w.Show() // map the window before entering the event loop (Fyne does not auto-show)
	a.Run() // blocks until the last window is closed
	u.stop()
	return nil
}

// windowUI is the window content with the widgets to update.
type windowUI struct {
	app  fyne.App
	win  fyne.Window
	cfg  config.Config
	sock string

	root      *fyne.Container
	power     *widget.Label
	summary   *widget.Label
	daemon    *widget.Label
	chart     *powerChart
	cells     map[string]*widget.Label
	table     *widget.Table
	pauseBtn  *widget.Button
	modeSel   *widget.Select
	autostart *widget.Check

	days        []energy.Day
	localSeries []energy.Sample // used when the snapshot carries no history
	paused      bool
	syncingMode bool // avoids a loop when syncing the Select from the snapshot
	autoSync    bool // avoids a loop when syncing the autostart checkbox
	autoOn      bool // last known autostart state

	ctx    context.Context
	cancel context.CancelFunc
}

func newUI(a fyne.App, w fyne.Window, cfg config.Config, sock string) *windowUI {
	u := &windowUI{app: a, win: w, cfg: cfg, sock: sock}
	u.cells = map[string]*widget.Label{}
	for _, k := range []string{"vin", "v12", "v5", "v33", "i12", "i5", "i33",
		"p12", "p5", "p33", "tvrm", "tcase", "fan"} {
		u.cells[k] = widget.NewLabel("—")
	}

	// command bar
	u.pauseBtn = widget.NewButton("Pausa", u.togglePause)
	u.modeSel = widget.NewSelect([]string{"power", "energy", "both", "none"}, u.setMode)
	u.syncingMode = true // initialization must not command the daemon
	u.modeSel.SetSelected(string(cfg.LabelMode))
	u.syncingMode = false
	reportBtn := widget.NewButton("Report", u.showReport)
	quitBtn := widget.NewButton("Esci", func() { u.win.Close() })
	u.autostart = widget.NewCheck("Avvio automatico", u.toggleAutostart)
	u.autoSync = true // initial sync does not command the daemon
	u.autostart.SetChecked(false)
	u.autoSync = false
	bar := container.NewHBox(u.pauseBtn, caption("etichetta:"), u.modeSel,
		u.autostart, reportBtn, quitBtn)

	// daemon status display (on the right in the header)
	u.daemon = caption("…")

	// header: large power + summary + daemon state
	u.power = widget.NewLabel("—")
	u.power.SizeName = theme.SizeNameHeadingText
	u.power.Importance = widget.HighImportance
	u.summary = caption("in attesa di dati…")
	header := container.NewBorder(nil, nil, nil, u.daemon,
		container.NewVBox(u.power, u.summary))

	// live chart
	u.chart = newPowerChart()

	// rails grid +12V / +5V / +3.3V
	railGrid := container.NewGridWithColumns(4,
		caption(""), caption("+12 V"), caption("+5 V"), caption("+3.3 V"),
		caption("tensione (V)"), u.cells["v12"], u.cells["v5"], u.cells["v33"],
		caption("corrente (A)"), u.cells["i12"], u.cells["i5"], u.cells["i33"],
		caption("potenza (W)"), u.cells["p12"], u.cells["p5"], u.cells["p33"])

	// AC input, temperatures, fan
	envGrid := container.NewGridWithColumns(4,
		caption("ingresso (V AC)"), caption("VRM (°C)"), caption("case (°C)"),
		caption("ventola RPM · PWM %"),
		u.cells["vin"], u.cells["tvrm"], u.cells["tcase"], u.cells["fan"])

	// last days table
	u.table = widget.NewTable(
		func() (int, int) { return len(u.days), 5 },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.TableCellID, o fyne.CanvasObject) {
			l, ok := o.(*widget.Label)
			if !ok || id.Row >= len(u.days) {
				return
			}
			d := u.days[id.Row]
			switch id.Col {
			case 0:
				l.SetText(d.Date)
			case 1:
				l.SetText(fmt.Sprintf("%.3f", d.Wh/1000))
			case 2:
				l.SetText(fmt.Sprintf("%.2f", d.Costo(u.cfg.EurPerKWh)))
			case 3:
				l.SetText(fmt.Sprintf("%.0f", d.AvgW()))
			case 4:
				l.SetText(fmt.Sprintf("%.0f", d.MaxW))
			}
		})
	for i, wd := range []float32{110, 90, 80, 90, 90} {
		u.table.SetColumnWidth(i, wd)
	}

	top := container.NewVBox(bar, header, u.chart, railGrid, envGrid)
	u.root = container.NewBorder(top, nil, nil, nil, u.table)
	registerUI(w, u)
	return u
}

func caption(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Importance = widget.LowImportance
	return l
}

// --- UI updates (from the UI thread only) ---

// applySnapshot populates the window from a snapshot. Exposed for the
// headless tests: the real path goes through apply (goroutine) with fyne.Do.
func applySnapshot(w fyne.Window, s status.Snapshot) {
	if u := uiFor(w); u != nil {
		u.render(s)
	}
}

// apply updates the window from an arbitrary goroutine.
func (u *windowUI) apply(s status.Snapshot) {
	fyne.Do(func() { u.render(s) })
}

func (u *windowUI) render(s status.Snapshot) {
	u.paused = s.Paused
	u.power.SetText(s.WLabel(u.cfg.FormatW))
	u.summary.SetText(fmt.Sprintf(
		"oggi %.2f kWh (%.2f €) · media %.0f W · picco %.0f W",
		s.TodayWh/1000, s.TodayCostEuro, s.TodayAvgW, s.TodayMaxW))

	val := func(v float64, format string) string {
		if !s.Online {
			return "—"
		}
		return fmt.Sprintf(format, v)
	}
	u.cells["vin"].SetText(val(s.VIn, "%.1f"))
	u.cells["v12"].SetText(val(s.V12, "%.2f"))
	u.cells["v5"].SetText(val(s.V5, "%.2f"))
	u.cells["v33"].SetText(val(s.V33, "%.2f"))
	u.cells["i12"].SetText(val(s.I12, "%.2f"))
	u.cells["i5"].SetText(val(s.I5, "%.2f"))
	u.cells["i33"].SetText(val(s.I33, "%.2f"))
	u.cells["p12"].SetText(val(s.P12, "%.1f"))
	u.cells["p5"].SetText(val(s.P5, "%.1f"))
	u.cells["p33"].SetText(val(s.P33, "%.1f"))
	u.cells["tvrm"].SetText(val(s.TVRM, "%.1f"))
	u.cells["tcase"].SetText(val(s.TCase, "%.1f"))
	u.cells["fan"].SetText(fmt.Sprintf("%.0f · %.0f%%", s.Fan, s.PWM))

	u.days = s.Days
	if len(u.days) > tableDays {
		u.days = u.days[:tableDays]
	}
	u.table.Refresh()

	label := "Pausa"
	if s.Paused {
		label = "Riprendi"
	}
	u.pauseBtn.SetText(label)
	if u.modeSel.Selected != string(s.LabelMode) {
		u.syncingMode = true
		u.modeSel.SetSelected(string(s.LabelMode))
		u.syncingMode = false
	}
	u.chart.SetSeries(u.serieFrom(s), s.AlertW)
}

// serieFrom picks the chart series: it uses the daemon's history and, if
// absent, accumulates the samples received by the window itself.
func (u *windowUI) serieFrom(s status.Snapshot) []energy.Sample {
	if len(s.History) > 0 {
		return s.History
	}
	if s.Online {
		at := s.Time
		if at.IsZero() {
			at = time.Now()
		}
		u.localSeries = append(u.localSeries, energy.Sample{Time: at, W: s.W})
		if len(u.localSeries) > 1800 {
			u.localSeries = append(u.localSeries[:0],
				u.localSeries[len(u.localSeries)-1800:]...)
		}
	}
	return u.localSeries
}

// --- commands ---

func (u *windowUI) togglePause() {
	cmd := "pause"
	if u.paused {
		cmd = "resume"
	}
	sock := u.sock
	go func() {
		if _, err := ctl.Call(sock, ctl.Request{Cmd: cmd}); err != nil {
			return
		}
		// the stream will confirm the state; update the button right away
		fyne.Do(func() {
			if cmd == "pause" {
				u.pauseBtn.SetText("Riprendi")
			} else {
				u.pauseBtn.SetText("Pausa")
			}
		})
	}()
}

func (u *windowUI) setMode(mode string) {
	if u.syncingMode {
		return
	}
	sock := u.sock
	go func() {
		ctl.Call(sock, ctl.Request{Cmd: "label-mode", Args: map[string]any{"mode": mode}})
	}()
}

// syncAutostartState loads the current autostart state into the checkbox.
// It asks the daemon when it is up, otherwise it reads the filesystem.
func (u *windowUI) syncAutostartState() {
	on := false
	if u.sockUp() {
		if resp, err := ctl.Call(u.sock, ctl.Request{Cmd: "autostart-status"}); err == nil && resp.OK {
			var st struct {
				Xdg bool `json:"xdg"`
			}
			if json.Unmarshal(resp.Data, &st) == nil {
				on = st.Xdg
			}
		}
	} else {
		on, _ = autostart.XDGInstalled()
	}
	fyne.Do(func() {
		u.autoSync = true
		u.autoOn = on
		u.autostart.SetChecked(on)
		u.autoSync = false
	})
}

// toggleAutostart enables or disables the XDG autostart entry. It goes
// through the daemon when it is running (so the tray menu stays in sync),
// otherwise it writes the desktop entry directly.
func (u *windowUI) toggleAutostart(v bool) {
	if u.autoSync {
		return
	}
	want := v
	go func() {
		var err error
		if u.sockUp() {
			_, err = ctl.Call(u.sock, ctl.Request{Cmd: "autostart-set", Args: map[string]any{"enabled": want}})
		}
		if err != nil {
			if want {
				_, err = autostart.InstallXDG("", nil)
			} else {
				_, err = autostart.RemoveXDG()
			}
		}
		fyne.Do(func() {
			if err != nil {
				// revert the checkbox and show the error
				u.autoSync = true
				u.autoOn = !want
				u.autostart.SetChecked(u.autoOn)
				u.autoSync = false
				dialog.ShowError(err, u.win)
				return
			}
			u.autoOn = want
		})
	}()
}

func (u *windowUI) showReport() {
	var b strings.Builder
	if len(u.days) == 0 {
		b.WriteString("nessun dato giornaliero disponibile.")
	} else {
		fmt.Fprintf(&b, "%-10s %8s %8s %7s %7s\n", "data", "kWh", "€", "media", "picco")
		var tot float64
		for _, d := range u.days {
			tot += d.Wh
			fmt.Fprintf(&b, "%-10s %8.3f %8.2f %7.0f %7.0f\n",
				d.Date, d.Wh/1000, d.Costo(u.cfg.EurPerKWh), d.AvgW(), d.MaxW)
		}
		fmt.Fprintf(&b, "%-10s %8.3f %8.2f\n", "TOTALE", tot/1000, tot/1000*u.cfg.EurPerKWh)
	}
	dialog.ShowInformation("Report giorni", b.String(), u.win)
}

// --- data feed (goroutine) ---

func (u *windowUI) start() {
	u.ctx, u.cancel = context.WithCancel(context.Background())
	go u.feed()
	go u.syncAutostartState()
}

func (u *windowUI) stop() {
	if u.cancel != nil {
		u.cancel()
	}
}

// feed maintains the data flow: subscribe streaming if the daemon is up,
// otherwise direct reading from the sensor; reconnection every 2s.
func (u *windowUI) feed() {
	for u.ctx.Err() == nil {
		if !u.sockUp() {
			u.setStatusLine(false, "")
			u.pumpLocal()
			continue
		}
		conn, br, err := ctl.Subscribe(u.sock, ctl.Request{Cmd: "subscribe"})
		if err != nil {
			u.setStatusLine(false, "")
			if !u.sleep(2 * time.Second) {
				return
			}
			continue
		}
		u.setStatusLine(true, "")
		u.pumpStream(conn, br)
		conn.Close()
		if u.ctx.Err() != nil {
			return
		}
		u.setStatusLine(false, "")
		if !u.sleep(2 * time.Second) {
			return
		}
	}
}

// pumpStream reads one JSON line (ctl.Response) at a time from the stream.
func (u *windowUI) pumpStream(conn net.Conn, br *bufio.Reader) {
	var netErr net.Error
	for {
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := br.ReadBytes('\n')
		if err != nil {
			if errors.As(err, &netErr) && netErr.Timeout() && u.ctx.Err() == nil {
				continue // the daemon is not publishing: retry
			}
			return
		}
		var resp ctl.Response
		if json.Unmarshal(line, &resp) != nil || !resp.OK || len(resp.Data) == 0 {
			continue
		}
		var snap status.Snapshot
		if json.Unmarshal(resp.Data, &snap) != nil {
			continue
		}
		u.apply(snap)
	}
}

// pumpLocal is the no-daemon fallback: it reads the sensor at 1s and
// integrates with a local accumulator, until the socket reappears or we exit.
func (u *windowUI) pumpLocal() {
	acc := energy.NewAccumulator(5*time.Second, 720, 2*time.Second)
	var rd *psu.Reader
	noteShown := false
	for u.ctx.Err() == nil {
		if rd == nil {
			if dir, err := psu.Detect(u.cfg.Sensor); err == nil {
				if r2, err2 := psu.New(dir); err2 == nil {
					rd = r2
				}
			} else if !noteShown {
				u.setStatusLine(false, "sensore non disponibile")
				noteShown = true
			}
		}
		if rd != nil {
			if s, err := rd.Read(); err != nil {
				rd = nil
			} else {
				acc.Add(s)
				u.apply(status.Build(s, acc, rd.Dir, false,
					u.cfg.LabelMode, u.cfg.AlertPowerW, u.cfg.EurPerKWh,
					acc.Days(), acc.History()))
			}
		}
		if !u.sleep(time.Second) {
			return
		}
		if u.sockUp() {
			return
		}
	}
}

func (u *windowUI) sockUp() bool {
	_, err := os.Stat(u.sock)
	return err == nil
}

func (u *windowUI) sleep(d time.Duration) bool {
	select {
	case <-u.ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (u *windowUI) setStatusLine(active bool, note string) {
	txt := "senza daemon (lettura diretta)"
	if active {
		txt = "daemon: attivo"
	}
	if note != "" {
		txt += " · " + note
	}
	fyne.Do(func() {
		u.daemon.SetText(txt)
		if active {
			u.pauseBtn.Enable()
		} else {
			u.pauseBtn.Disable()
		}
	})
}

// --- window -> UI registry (for applySnapshot in tests) ---

var uiRegistry = struct {
	sync.Mutex
	by map[fyne.Window]*windowUI
}{by: map[fyne.Window]*windowUI{}}

func registerUI(w fyne.Window, u *windowUI) {
	uiRegistry.Lock()
	defer uiRegistry.Unlock()
	uiRegistry.by[w] = u
}

func uiFor(w fyne.Window) *windowUI {
	uiRegistry.Lock()
	defer uiRegistry.Unlock()
	return uiRegistry.by[w]
}

// --- live power chart ---

// powerChart is a custom widget: it draws the AC power of the last ~30
// minutes as segments (canvas.Line) with textual axes/min-max and an
// optional threshold.
type powerChart struct {
	widget.BaseWidget
	series []energy.Sample
	alertW float64
	span   time.Duration
}

func newPowerChart() *powerChart {
	c := &powerChart{span: chartSpan}
	c.ExtendBaseWidget(c)
	return c
}

// SetSeries replaces the displayed series (from the UI thread).
func (c *powerChart) SetSeries(s []energy.Sample, alertW float64) {
	c.series = append([]energy.Sample(nil), s...)
	c.alertW = alertW
	c.Refresh()
}

// MinSize/Resize/Move/Show/Hide complete the custom widget.
func (c *powerChart) MinSize() fyne.Size { return fyne.NewSize(300, 140) }

func (c *powerChart) Resize(s fyne.Size) {
	c.BaseWidget.Resize(s)
	c.Refresh()
}

func (c *powerChart) Move(p fyne.Position) { c.BaseWidget.Move(p) }

func (c *powerChart) Show() { c.BaseWidget.Show() }

func (c *powerChart) Hide() { c.BaseWidget.Hide() }

func (c *powerChart) CreateRenderer() fyne.WidgetRenderer {
	return newChartRenderer(c)
}

type chartRenderer struct {
	c     *powerChart
	bg    *canvas.Rectangle
	axisX *canvas.Line
	axisY *canvas.Line
	alert *canvas.Line
	lines []*canvas.Line
	tMax  *canvas.Text
	tZero *canvas.Text
	tOld  *canvas.Text
	tNow  *canvas.Text
	tNone *canvas.Text
	objs  []fyne.CanvasObject
}

func newChartRenderer(c *powerChart) *chartRenderer {
	muted := theme.Color(theme.ColorNameDisabled)
	r := &chartRenderer{
		c:     c,
		bg:    canvas.NewRectangle(theme.Color(theme.ColorNameInputBackground)),
		axisX: canvas.NewLine(theme.Color(theme.ColorNameSeparator)),
		axisY: canvas.NewLine(theme.Color(theme.ColorNameSeparator)),
		alert: canvas.NewLine(theme.Color(theme.ColorNameError)),
		tMax:  newChartText("", muted),
		tZero: newChartText("0 W", muted),
		tOld:  newChartText("−30 min", muted),
		tNow:  newChartText("adesso", muted),
		tNone: newChartText("in attesa di campioni…", muted),
	}
	return r
}

// newChartText is a small, unobtrusive text label for the axes.
func newChartText(s string, col color.Color) *canvas.Text {
	t := canvas.NewText(s, col)
	t.TextSize = theme.CaptionTextSize()
	return t
}

func (r *chartRenderer) MinSize() fyne.Size { return r.c.MinSize() }

func (r *chartRenderer) Destroy() {}

func (r *chartRenderer) Refresh() {
	r.Layout(r.c.Size())
}

// Layout rebuilds the segments based on the series and the size.
func (r *chartRenderer) Layout(size fyne.Size) {
	c := r.c
	r.bg.Resize(size)

	padL, padR, padT, padB := float32(48), float32(8), float32(16), float32(14)
	w, h := size.Width, size.Height
	plotW := w - padL - padR
	plotH := h - padT - padB
	if plotW < 10 || plotH < 10 {
		return
	}

	// time window: the last `span` ending at the last sample
	end := time.Now()
	var pts []energy.Sample
	if n := len(c.series); n > 0 {
		end = c.series[n-1].Time
		start := end.Add(-c.span)
		for _, s := range c.series {
			if !s.Time.Before(start) {
				pts = append(pts, s)
			}
		}
		if len(pts) > chartMaxSeg+1 { // downsampling to stay within the segment cap
			step := (len(pts) + chartMaxSeg) / (chartMaxSeg + 1)
			reduced := make([]energy.Sample, 0, chartMaxSeg+2)
			for i := 0; i < len(pts); i += step {
				reduced = append(reduced, pts[i])
			}
			reduced = append(reduced, pts[len(pts)-1])
			pts = reduced
		}
	}

	maxW := 0.0
	for _, s := range pts {
		if s.W > maxW {
			maxW = s.W
		}
	}
	if c.alertW > maxW {
		maxW = c.alertW
	}
	if maxW <= 0 {
		maxW = 100
	}
	top := maxW * 1.15

	xOf := func(t time.Time) float32 {
		f := float64(t.Sub(end)) / float64(c.span)
		if f < 0 {
			f = 0
		}
		return padL + float32(f)*plotW
	}
	yOf := func(v float64) float32 {
		return h - padB - float32(v/top)*plotH
	}

	// axes
	r.axisX.Position1 = fyne.NewPos(padL, h-padB)
	r.axisX.Position2 = fyne.NewPos(w-padR, h-padB)
	r.axisY.Position1 = fyne.NewPos(padL, padT)
	r.axisY.Position2 = fyne.NewPos(padL, h-padB)

	// alert threshold
	if c.alertW > 0 {
		r.alert.StrokeWidth = 1
		r.alert.Position1 = fyne.NewPos(padL, yOf(c.alertW))
		r.alert.Position2 = fyne.NewPos(w-padR, yOf(c.alertW))
	} else {
		r.alert.Position1 = fyne.NewPos(0, 0)
		r.alert.Position2 = fyne.NewPos(0, 0)
	}

	// series segments (reused pool, primary color)
	need := 0
	if len(pts) >= 2 {
		need = len(pts) - 1
	}
	col := theme.Color(theme.ColorNamePrimary)
	for len(r.lines) < need {
		l := canvas.NewLine(col)
		l.StrokeWidth = 2
		r.lines = append(r.lines, l)
	}
	for i := range r.lines {
		l := r.lines[i]
		if i >= need {
			l.Position1 = fyne.NewPos(0, 0)
			l.Position2 = fyne.NewPos(0, 0)
			l.Refresh()
			continue
		}
		l.Position1 = fyne.NewPos(xOf(pts[i].Time), yOf(pts[i].W))
		l.Position2 = fyne.NewPos(xOf(pts[i+1].Time), yOf(pts[i+1].W))
		l.Refresh()
	}

	// texts
	r.tMax.Text = fmt.Sprintf("%.0f W", maxW)
	r.tMax.Move(fyne.NewPos(4, 0))
	r.tZero.Move(fyne.NewPos(4, h-padB+1))
	r.tOld.Move(fyne.NewPos(padL, h-padB+1))
	r.tNow.Text = "adesso"
	r.tNow.Resize(fyne.NewSize(padR+80, 14))
	r.tNow.Move(fyne.NewPos(w-padR-80, h-padB+1))
	r.tNow.Alignment = fyne.TextAlignTrailing
	if len(pts) == 0 {
		r.tNone.Text = "in attesa di campioni…"
		r.tNone.Move(fyne.NewPos(w/2-80, h/2-8))
	} else {
		r.tNone.Text = ""
	}

	// list of objects to draw
	objs := make([]fyne.CanvasObject, 0, len(r.lines)+9)
	objs = append(objs, r.bg, r.axisX, r.axisY, r.alert, r.tMax, r.tZero, r.tOld, r.tNow, r.tNone)
	for _, l := range r.lines {
		objs = append(objs, l)
	}
	r.objs = objs

	for _, o := range []fyne.CanvasObject{r.bg, r.axisX, r.axisY, r.alert, r.tMax, r.tZero, r.tOld, r.tNow, r.tNone} {
		canvas.Refresh(o)
	}
}

func (r *chartRenderer) Objects() []fyne.CanvasObject {
	if len(r.objs) == 0 {
		r.Layout(r.c.Size())
	}
	return r.objs
}
