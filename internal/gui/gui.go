// Package gui implements the native detail window (Fyne).
// The window is a client of the daemon: it subscribes to the control socket
// ("subscribe" command, one JSON snapshot every second). Without a daemon it
// falls back to reading the hwmon sensor directly with a local accumulator,
// so it stays usable even with the daemon off.
//
// The window is localized: the language comes from the configuration
// ("language"), then from the locale, then from English. It can be switched
// live from the "language" select, which persists the choice through the
// daemon (or directly to the configuration when the daemon is down).
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
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/vincalkr/powcon-plus/internal/autostart"
	"github.com/vincalkr/powcon-plus/internal/config"
	"github.com/vincalkr/powcon-plus/internal/ctl"
	"github.com/vincalkr/powcon-plus/internal/currency"
	"github.com/vincalkr/powcon-plus/internal/energy"
	"github.com/vincalkr/powcon-plus/internal/loc"
	"github.com/vincalkr/powcon-plus/internal/psu"
	"github.com/vincalkr/powcon-plus/internal/report"
	"github.com/vincalkr/powcon-plus/internal/status"
	"github.com/vincalkr/powcon-plus/internal/store"
	"github.com/vincalkr/powcon-plus/internal/version"
)

// Options for opening the window.
type Options struct {
	ConfigPath string // configuration file ("" = config.Path())
	DataDir    string // daemon data dir ("" = the one from the configuration)
}

const (
	chartSpan   = 30 * time.Minute // time window of the live chart
	chartMaxSeg = 240              // cap on the segments drawn
	tableDays   = 14               // days in the table
	sockName    = "control.sock"
)

// modeKeys are the raw label modes on the wire; the select shows the
// translated labels in the same order.
var modeKeys = []string{
	string(config.ModePower), string(config.ModeEnergy),
	string(config.ModeBoth), string(config.ModeNone),
}

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
	w := a.NewWindow("")
	u := newUI(a, w, cfg, filepath.Join(dataDir, sockName))
	w.SetTitle(u.cat.Get("ui.window.title"))
	w.SetContent(u.root)
	// Tall enough for the chart, the sensor grid and the days table;
	// the user can still resize (Fyne persists the size per window).
	w.Resize(fyne.NewSize(780, 740))
	w.SetOnClosed(u.stop)
	u.start()
	w.Show() // map the window before entering the event loop (Fyne does not auto-show)
	a.Run()  // blocks until the last window is closed
	u.stop()
	return nil
}

// windowUI is the window content with the widgets to update.
type windowUI struct {
	app  fyne.App
	win  fyne.Window
	cfg  config.Config
	sock string

	cat      *loc.Catalog
	langCode string

	root            *fyne.Container
	power           *widget.Label
	summary         *widget.RichText
	daemon          *widget.RichText
	chart           *powerChart
	cells           map[string]*widget.Label
	gridCaps        map[string]*widget.RichText
	table           *widget.Table
	pauseBtn        *widget.Button
	modeSel         *widget.Select
	langSel         *widget.Select
	autostart       *widget.Check
	priceEntry      *widget.Entry
	priceEntryBox   *wideEntry
	currencySel     *widget.Select
	labelCaption    *widget.RichText
	langCaption     *widget.RichText
	priceCaption    *widget.RichText
	currencyCaption *widget.RichText
	aboutBtn        *widget.Button
	reportBtn       *widget.Button
	quitBtn         *widget.Button

	days         []energy.Day
	localSeries  []energy.Sample // used when the snapshot carries no history
	lastSnap     status.Snapshot
	haveSnap     bool
	lastMode     config.LabelMode
	paused       bool
	price        float64 // last known tariff (per kWh, in `currency`)
	currency     string  // last known display currency
	syncingMode  bool    // avoids a loop when syncing the Select from the snapshot
	syncingLang  bool    // avoids a loop when syncing the language select
	syncingPrice bool    // avoids a loop when syncing the price widgets
	priceSynced  bool    // the price/currency widgets got their first snapshot value
	autoSync     bool    // avoids a loop when syncing the autostart checkbox
	autoOn       bool    // last known autostart state

	priceMu    sync.Mutex
	priceTimer *time.Timer // debounces the tariff entry before committing

	ctx    context.Context
	cancel context.CancelFunc
}

func newUI(a fyne.App, w fyne.Window, cfg config.Config, sock string) *windowUI {
	u := &windowUI{app: a, win: w, cfg: cfg, sock: sock}

	// Language resolution: explicit config, then locale, then English.
	lang := cfg.Language
	if lang == "" {
		lang = loc.Auto()
	}
	if !loc.IsValid(lang) {
		lang = "en"
	}
	u.langCode = lang
	u.cat = loc.Load(lang)

	u.cells = map[string]*widget.Label{}
	for _, k := range []string{"vin", "v12", "v5", "v33", "i12", "i5", "i33",
		"p12", "p5", "p33", "tvrm", "tcase", "fan"} {
		u.cells[k] = widget.NewLabel("—")
	}
	u.gridCaps = map[string]*widget.RichText{}
	for _, k := range []string{"voltage", "current", "power", "vin", "tvrm", "tcase", "fan"} {
		u.gridCaps[k] = newCaption(u.cat.Get("ui.grid." + k))
	}

	// command bar
	u.pauseBtn = widget.NewButton(u.cat.Get("ui.pause"), u.togglePause)
	u.lastMode = cfg.LabelMode
	u.modeSel = widget.NewSelect(u.modeOptions(), u.setMode)
	u.syncingMode = true // initialization must not command the daemon
	u.modeSel.SetSelected(u.cat.Get("ui.mode." + string(cfg.LabelMode)))
	u.syncingMode = false

	// language select (options are the native language names)
	langNames := make([]string, len(loc.Tags))
	for i, tag := range loc.Tags {
		langNames[i] = tag.Name
	}
	u.langSel = widget.NewSelect(langNames, u.setLang)
	u.syncingLang = true
	u.langSel.SetSelected(loc.NameOf(u.langCode))
	u.syncingLang = false

	u.aboutBtn = widget.NewButton(u.cat.Get("ui.bar.about"), u.showAbout)
	u.reportBtn = widget.NewButton(u.cat.Get("ui.bar.report"), u.showReport)
	u.quitBtn = widget.NewButton(u.cat.Get("ui.bar.quit"), func() { u.win.Close() })
	u.autostart = widget.NewCheck(u.cat.Get("ui.bar.autostart"), u.toggleAutostart)
	u.autoSync = true // initial sync does not command the daemon
	u.autostart.SetChecked(false)
	u.autoSync = false

	// The mode select controls the text shown next to the tray icon; the
	// caption is intentionally fixed to "Tray icon" in every language.
	u.labelCaption = newCaption("Tray icon")
	u.langCaption = newCaption(u.cat.Get("ui.bar.language"))
	bar := container.NewHBox(u.pauseBtn, u.labelCaption, u.modeSel,
		u.autostart, u.langCaption, u.langSel, u.aboutBtn, u.reportBtn, u.quitBtn)

	// cost settings: tariff per kWh (in the display currency) and currency
	u.price = cfg.PricePerKWh
	u.currency = cfg.Currency
	u.priceCaption = newCaption(u.cat.Get("ui.bar.price"))
	u.currencyCaption = newCaption(u.cat.Get("ui.bar.currency"))
	u.priceEntry = widget.NewEntry()
	u.priceEntry.PlaceHolder = "0.25"
	u.priceEntry.OnChanged = u.onPriceEdited
	// wrap the entry so it keeps a readable width even with short values
	u.priceEntryBox = newWideEntry(u.priceEntry, 90)
	u.currencySel = widget.NewSelect(currency.Supported, u.onCurrencyChanged)
	u.syncingPrice = true // initialization must not command the daemon
	u.priceEntry.SetText(fmt.Sprintf("%g", cfg.PricePerKWh))
	u.currencySel.SetSelected(cfg.Currency)
	u.syncingPrice = false
	bar2 := container.NewHBox(u.priceCaption, u.priceEntryBox,
		u.currencyCaption, u.currencySel)

	// daemon status display (on the right in the header)
	u.daemon = newCaption("…")

	// header: large power + summary + daemon state
	u.power = widget.NewLabel("—")
	u.power.SizeName = theme.SizeNameHeadingText
	u.power.Importance = widget.HighImportance
	u.summary = newCaption(u.cat.Get("ui.header.waiting"))
	header := container.NewBorder(nil, nil, nil, u.daemon,
		container.NewVBox(u.power, u.summary))

	// live chart
	u.chart = newPowerChart()
	u.chart.SetCatalog(u.cat)

	// rails grid +12V / +5V / +3.3V
	railGrid := container.NewGridWithColumns(4,
		newCaption(""), newCaption("+12 V"), newCaption("+5 V"), newCaption("+3.3 V"),
		u.gridCaps["voltage"], u.cells["v12"], u.cells["v5"], u.cells["v33"],
		u.gridCaps["current"], u.cells["i12"], u.cells["i5"], u.cells["i33"],
		u.gridCaps["power"], u.cells["p12"], u.cells["p5"], u.cells["p33"])

	// AC input, temperatures, fan
	envGrid := container.NewGridWithColumns(4,
		u.gridCaps["vin"], u.gridCaps["tvrm"], u.gridCaps["tcase"], u.gridCaps["fan"],
		u.cells["vin"], u.cells["tvrm"], u.cells["tcase"], u.cells["fan"])

	// last days table (row 0 is the header; the cost column header is the
	// currency symbol, which comes from the configuration, not the catalog)
	colKeys := []string{"date", "kwh", "", "avg", "peak"}
	u.table = widget.NewTable(
		func() (int, int) { return len(u.days) + 1, len(colKeys) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.TableCellID, o fyne.CanvasObject) {
			l, ok := o.(*widget.Label)
			if !ok {
				return
			}
			if id.Row == 0 {
				if id.Col == 2 {
					l.SetText(currency.Symbol(u.currency))
					return
				}
				l.SetText(u.cat.Get("ui.table." + colKeys[id.Col]))
				return
			}
			if id.Row-1 >= len(u.days) {
				return
			}
			d := u.days[id.Row-1]
			switch id.Col {
			case 0:
				l.SetText(d.Date)
			case 1:
				l.SetText(fmt.Sprintf("%.3f", d.Wh/1000))
			case 2:
				l.SetText(fmt.Sprintf("%.2f", d.Cost(u.price)))
			case 3:
				l.SetText(fmt.Sprintf("%.0f", d.AvgW()))
			case 4:
				l.SetText(fmt.Sprintf("%.0f", d.MaxW))
			}
		})
	for i, wd := range []float32{110, 90, 80, 90, 90} {
		u.table.SetColumnWidth(i, wd)
	}

	top := container.NewVBox(bar, bar2, header, u.chart, railGrid, envGrid)
	u.root = container.NewBorder(top, nil, nil, nil, u.table)
	registerUI(w, u)
	return u
}

// modeOptions returns the translated mode labels, in modeKeys order.
func (u *windowUI) modeOptions() []string {
	opts := make([]string, len(modeKeys))
	for i, k := range modeKeys {
		opts[i] = u.cat.Get("ui.mode." + k)
	}
	return opts
}

// newCaption returns a muted caption label (placeholder color, caption
// size). The placeholder color stays readable in both the light and the
// dark theme, unlike the "disabled" color used by widget.LowImportance,
// which is almost invisible on dark backgrounds.
func newCaption(text string) *widget.RichText {
	rt := widget.NewRichTextWithText(text)
	seg := rt.Segments[0].(*widget.TextSegment)
	seg.Style.ColorName = theme.ColorNamePlaceHolder
	seg.Style.SizeName = theme.SizeNameCaptionText
	return rt
}

// setCaption updates the text of a caption created by newCaption.
func setCaption(rt *widget.RichText, text string) {
	rt.Segments[0].(*widget.TextSegment).Text = text
	rt.Refresh()
}

// captionText reads the text of a caption (used by the headless tests).
func captionText(rt *widget.RichText) string {
	return rt.Segments[0].(*widget.TextSegment).Text
}

// wideEntry wraps an entry to give it a minimum width: the stock entry
// sizes itself on its content, which is too narrow for short values such
// as "0.25".
type wideEntry struct {
	widget.BaseWidget
	entry *widget.Entry
	width float32
}

func newWideEntry(entry *widget.Entry, minW float32) *wideEntry {
	w := &wideEntry{entry: entry, width: minW}
	w.ExtendBaseWidget(w)
	return w
}

// fyne.Widget
func (w *wideEntry) CreateRenderer() fyne.WidgetRenderer {
	return w.entry.CreateRenderer()
}

func (w *wideEntry) MinSize() fyne.Size {
	min := w.entry.MinSize()
	if min.Width < w.width {
		min.Width = w.width
	}
	return min
}

func (w *wideEntry) Resize(size fyne.Size) {
	w.BaseWidget.Resize(size)
	w.entry.Resize(size)
}

func (w *wideEntry) Move(pos fyne.Position) {
	w.BaseWidget.Move(pos)
	w.entry.Move(pos)
}

func (w *wideEntry) Show() {
	w.BaseWidget.Show()
	w.entry.Show()
}

func (w *wideEntry) Hide() {
	w.BaseWidget.Hide()
	w.entry.Hide()
}

// fyne.Focusable / desktop input: everything is forwarded to the entry.
func (w *wideEntry) AcceptsFocus() bool { return true }
func (w *wideEntry) AcceptsTab() bool   { return w.entry.AcceptsTab() }
func (w *wideEntry) FocusGained()       { w.entry.FocusGained() }
func (w *wideEntry) FocusLost()         { w.entry.FocusLost() }
func (w *wideEntry) Cursor() desktop.Cursor {
	return w.entry.Cursor()
}

func (w *wideEntry) KeyDown(key *fyne.KeyEvent) { w.entry.KeyDown(key) }
func (w *wideEntry) KeyUp(key *fyne.KeyEvent)   { w.entry.KeyUp(key) }
func (w *wideEntry) TypedRune(r rune)           { w.entry.TypedRune(r) }
func (w *wideEntry) TypedKey(key *fyne.KeyEvent) {
	w.entry.TypedKey(key)
}

func (w *wideEntry) Tapped(event *fyne.PointEvent)          { w.entry.Tapped(event) }
func (w *wideEntry) TappedSecondary(event *fyne.PointEvent) { w.entry.TappedSecondary(event) }
func (w *wideEntry) DoubleTapped(event *fyne.PointEvent)    { w.entry.DoubleTapped(event) }
func (w *wideEntry) Dragged(event *fyne.DragEvent)          { w.entry.Dragged(event) }
func (w *wideEntry) DragEnd()                               { w.entry.DragEnd() }

// retranslate re-renders every static label with the current catalog. It
// runs from the UI thread (the language select callback).
func (u *windowUI) retranslate() {
	u.win.SetTitle(u.cat.Get("ui.window.title"))
	setCaption(u.labelCaption, "Tray icon") // fixed in every language
	setCaption(u.langCaption, u.cat.Get("ui.bar.language"))
	setCaption(u.priceCaption, u.cat.Get("ui.bar.price"))
	setCaption(u.currencyCaption, u.cat.Get("ui.bar.currency"))
	u.aboutBtn.SetText(u.cat.Get("ui.bar.about"))
	u.reportBtn.SetText(u.cat.Get("ui.bar.report"))
	u.quitBtn.SetText(u.cat.Get("ui.bar.quit"))
	u.autostart.SetText(u.cat.Get("ui.bar.autostart"))
	if u.paused {
		u.pauseBtn.SetText(u.cat.Get("ui.resume"))
	} else {
		u.pauseBtn.SetText(u.cat.Get("ui.pause"))
	}
	u.syncingMode = true
	u.modeSel.Options = u.modeOptions()
	u.modeSel.SetSelected(u.cat.Get("ui.mode." + string(u.lastMode)))
	u.syncingMode = false
	u.modeSel.Refresh()
	for key, l := range u.gridCaps {
		setCaption(l, u.cat.Get("ui.grid."+key))
	}
	u.chart.SetCatalog(u.cat)
	if u.haveSnap {
		setCaption(u.summary, u.cat.Sprintf("ui.summary.today",
			u.lastSnap.TodayWh/1000, u.lastSnap.TodayCost, currency.Symbol(u.lastSnap.Currency),
			u.lastSnap.TodayAvgW, u.lastSnap.TodayMaxW))
	} else {
		setCaption(u.summary, u.cat.Get("ui.header.waiting"))
	}
	u.table.Refresh()
	u.syncingLang = true
	u.langSel.SetSelected(loc.NameOf(u.langCode))
	u.syncingLang = false
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
	u.lastSnap = s
	u.haveSnap = true
	u.lastMode = s.LabelMode
	u.price = s.Price
	u.currency = s.Currency
	u.power.SetText(s.WLabel(u.cfg.FormatW))
	setCaption(u.summary, u.cat.Sprintf("ui.summary.today",
		s.TodayWh/1000, s.TodayCost, currency.Symbol(s.Currency), s.TodayAvgW, s.TodayMaxW))

	// keep the price widgets in sync with the daemon
	if !u.priceSynced {
		u.priceSynced = true
		u.syncingPrice = true
		u.priceEntry.SetText(fmt.Sprintf("%g", s.Price))
		u.currencySel.SetSelected(s.Currency)
		u.syncingPrice = false
	} else if u.currencySel.Selected != s.Currency {
		u.syncingPrice = true
		u.currencySel.SetSelected(s.Currency)
		u.syncingPrice = false
	}

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

	label := u.cat.Get("ui.pause")
	if s.Paused {
		label = u.cat.Get("ui.resume")
	}
	u.pauseBtn.SetText(label)
	want := u.cat.Get("ui.mode." + string(s.LabelMode))
	if u.modeSel.Selected != want {
		u.syncingMode = true
		u.modeSel.SetSelected(want)
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
				u.pauseBtn.SetText(u.cat.Get("ui.resume"))
			} else {
				u.pauseBtn.SetText(u.cat.Get("ui.pause"))
			}
		})
	}()
}

// setMode sends the label mode to the daemon. The select shows translated
// labels, so the raw key is resolved by position.
func (u *windowUI) setMode(_ string) {
	if u.syncingMode {
		return
	}
	idx := u.modeSel.SelectedIndex()
	if idx < 0 || idx >= len(modeKeys) {
		return
	}
	mode := modeKeys[idx]
	sock := u.sock
	go func() {
		ctl.Call(sock, ctl.Request{Cmd: "label-mode", Args: map[string]any{"mode": mode}})
	}()
}

// setLang switches the UI language (window now, tray/notifications through
// the daemon) and persists the choice in the configuration.
func (u *windowUI) setLang(name string) {
	if u.syncingLang {
		return
	}
	code := loc.CodeOf(name)
	if code == "" || code == u.langCode {
		return
	}
	u.langCode = code
	u.cat = loc.Load(code)
	u.retranslate()
	go func() {
		if u.sockUp() {
			ctl.Call(u.sock, ctl.Request{Cmd: "ui-language", Args: map[string]any{"language": code}})
			return
		}
		// no daemon: persist the choice directly
		cfg, err := config.Load("")
		if err != nil {
			return
		}
		cfg.Language = code
		_ = config.Save(config.Path(), cfg)
	}()
}

// onPriceEdited debounces the tariff entry and commits the value once the
// user stops typing.
func (u *windowUI) onPriceEdited(s string) {
	if u.syncingPrice {
		return
	}
	price, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || price < 0 {
		return
	}
	u.priceMu.Lock()
	if u.priceTimer != nil {
		u.priceTimer.Stop()
	}
	u.priceTimer = time.AfterFunc(500*time.Millisecond, func() {
		// read the currency on the UI thread, then commit
		fyne.Do(func() {
			go u.sendPrice(price, u.currencySel.Selected)
		})
	})
	u.priceMu.Unlock()
}

// onCurrencyChanged commits the tariff together with the new currency.
func (u *windowUI) onCurrencyChanged(code string) {
	if u.syncingPrice || !currency.IsValid(code) {
		return
	}
	price, err := strconv.ParseFloat(strings.TrimSpace(u.priceEntry.Text), 64)
	if err != nil || price < 0 {
		price = u.cfg.PricePerKWh
	}
	go u.sendPrice(price, code)
}

// sendPrice persists the tariff and the currency (through the daemon when it
// is up, directly to the configuration otherwise).
func (u *windowUI) sendPrice(price float64, cur string) {
	if u.sockUp() {
		ctl.Call(u.sock, ctl.Request{Cmd: "price", Args: map[string]any{"price": price, "currency": cur}})
		return
	}
	cfg, err := config.Load("")
	if err != nil {
		return
	}
	cfg.PricePerKWh = price
	cfg.Currency = cur
	_ = config.Save(config.Path(), cfg)
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

// showAbout shows the version and the developer info.
func (u *windowUI) showAbout() {
	body := u.cat.Sprintf("ui.about.body", version.Version, version.Developer, version.Website)
	dialog.ShowInformation(u.cat.Get("ui.about.title"), body, u.win)
}

// showReport exports the daily summaries as a CSV file instead of showing them
// in a dialog: first how many days to export, then where to save. The columns
// are the same as `powcon-plus report --csv`.
func (u *windowUI) showReport() {
	if len(u.days) == 0 {
		dialog.ShowInformation(u.cat.Get("ui.report.title"), u.cat.Get("ui.report.empty"), u.win)
		return
	}
	entry := widget.NewEntry()
	entry.SetPlaceHolder("0")
	form := dialog.NewForm(
		u.cat.Get("ui.report.title"),
		u.cat.Get("ui.report.export"),
		u.cat.Get("ui.report.cancel"),
		[]*widget.FormItem{widget.NewFormItem(u.cat.Get("ui.report.days"), entry)},
		func(ok bool) {
			if ok {
				u.askReportFile(parseDayCount(entry.Text))
			}
		},
		u.win,
	)
	form.Resize(fyne.NewSize(420, 180))
	form.Show()
}

// parseDayCount reads the requested number of days: anything that is not a
// positive number means every day available.
func parseDayCount(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// askReportFile opens the "save as" dialog and writes the CSV into the chosen
// file, then confirms with the path.
func (u *windowUI) askReportFile(n int) {
	days := report.MostRecent(u.reportDays(), n)
	if len(days) == 0 {
		dialog.ShowInformation(u.cat.Get("ui.report.title"), u.cat.Get("ui.report.empty"), u.win)
		return
	}
	save := dialog.NewFileSave(func(wc fyne.URIWriteCloser, err error) {
		if wc == nil { // cancelled, or nothing usable selected
			if err != nil {
				dialog.ShowError(err, u.win)
			}
			return
		}
		defer wc.Close()
		if werr := report.Write(wc, days, u.price); werr != nil {
			dialog.ShowError(werr, u.win)
			return
		}
		dialog.ShowInformation(u.cat.Get("ui.report.title"),
			u.cat.Sprintf("ui.report.saved", wc.URI().Path()), u.win)
	}, u.win)
	save.SetTitleText(u.cat.Get("ui.report.title"))
	save.SetFileName(reportFileName(days))
	save.Show()
}

// reportFileName suggests a name covering the exported range (days are newest
// first): powcon-plus-report-2026-10-12-2026-10-06.csv
func reportFileName(days []energy.Day) string {
	switch len(days) {
	case 0:
		return "powcon-plus-report.csv"
	case 1:
		return "powcon-plus-report-" + days[0].Date + ".csv"
	default:
		return "powcon-plus-report-" + days[0].Date + "-" + days[len(days)-1].Date + ".csv"
	}
}

// reportDays returns the days available for the export: the whole history on
// disk, with the values carried by the live snapshot taking precedence, since
// today's row is flushed to disk only periodically.
func (u *windowUI) reportDays() []energy.Day {
	st, err := store.New(filepath.Dir(u.sock))
	if err != nil {
		return u.days
	}
	all, err := st.LoadDaily()
	if err != nil || len(all) == 0 {
		return u.days
	}
	return report.MergeNewer(all, u.days)
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
				u.setStatusLine(false, u.cat.Get("ui.status.sensor"))
				noteShown = true
			}
		}
		if rd != nil {
			if s, err := rd.Read(); err != nil {
				rd = nil
			} else {
				acc.Add(s)
				u.apply(status.Build(s, acc, rd.Dir, false,
					u.cfg.LabelMode, u.cfg.AlertPowerW, u.cfg.PricePerKWh, u.cfg.Currency,
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
	txt := u.cat.Get("ui.status.nodaemon")
	if active {
		txt = u.cat.Get("ui.status.daemon")
	}
	if note != "" {
		txt += " · " + note
	}
	fyne.Do(func() {
		setCaption(u.daemon, txt)
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
	cat    *loc.Catalog
}

func newPowerChart() *powerChart {
	c := &powerChart{span: chartSpan, cat: loc.Load("en")}
	c.ExtendBaseWidget(c)
	return c
}

// SetCatalog swaps the translation catalog of the chart texts.
func (c *powerChart) SetCatalog(cat *loc.Catalog) {
	if cat != nil {
		c.cat = cat
		c.Refresh()
	}
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
	// Placeholder color (readable in both themes), not the almost-invisible
	// "disabled" color used for captions.
	muted := theme.Color(theme.ColorNamePlaceHolder)
	r := &chartRenderer{
		c:     c,
		bg:    canvas.NewRectangle(theme.Color(theme.ColorNameInputBackground)),
		axisX: canvas.NewLine(theme.Color(theme.ColorNameSeparator)),
		axisY: canvas.NewLine(theme.Color(theme.ColorNameSeparator)),
		alert: canvas.NewLine(theme.Color(theme.ColorNameError)),
		tMax:  newChartText("", muted),
		tZero: newChartText("", muted),
		tOld:  newChartText("", muted),
		tNow:  newChartText("", muted),
		tNone: newChartText("", muted),
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
		// t.Sub(end) is negative (end is the newest sample): it runs from
		// -span at the left edge to 0 at the right edge.
		f := float64(t.Sub(end)) / float64(c.span)
		if f < -1 {
			f = -1
		}
		return padL + float32(1+f)*plotW
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
	r.tZero.Text = c.cat.Get("ui.chart.zero")
	r.tZero.Move(fyne.NewPos(4, h-padB+1))
	r.tOld.Text = c.cat.Get("ui.chart.old")
	r.tOld.Move(fyne.NewPos(padL, h-padB+1))
	r.tNow.Text = c.cat.Get("ui.chart.now")
	r.tNow.Resize(fyne.NewSize(padR+80, 14))
	r.tNow.Move(fyne.NewPos(w-padR-80, h-padB+1))
	r.tNow.Alignment = fyne.TextAlignTrailing
	if len(pts) == 0 {
		r.tNone.Text = c.cat.Get("ui.chart.waiting")
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
