// Headless tests of the window: they use the Fyne test driver, no real
// graphical process. They verify population from a synthetic status.Snapshot
// (applySnapshot, without a bus) and the chart's behavior with an empty history.
package gui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"powcon-plus/internal/config"
	"powcon-plus/internal/energy"
	"powcon-plus/internal/status"
)

func testSnapshot() status.Snapshot {
	now := time.Now()
	return status.Snapshot{
		Time: now, Sensor: "/sys/class/hwmon/hwmon0", Online: true,
		VIn: 230.4, V12: 12.1, V5: 5.02, V33: 3.31,
		I12: 8.2, I5: 1.1, I33: 0.6,
		W: 148.2, P12: 98.4, P5: 5.5, P33: 2.0,
		TVRM: 41.2, TCase: 37.5, Fan: 610, PWM: 34,
		TodayWh: 2400, TodayAvgW: 120, TodayMaxW: 312, TodayCost: 0.6,
		LabelMode: config.ModePower, AlertW: 500, Price: 0.25, Currency: "EUR",
		Days: []energy.Day{
			{Date: "2026-09-30", Wh: 2400, DurationS: 7200, MaxW: 312, MinW: 40},
			{Date: "2026-09-29", Wh: 5100, DurationS: 36000, MaxW: 402, MinW: 35},
		},
		History: []energy.Sample{
			{Time: now.Add(-10 * time.Second), W: 100},
			{Time: now.Add(-8 * time.Second), W: 140},
			{Time: now.Add(-6 * time.Second), W: 148},
		},
	}
}

func newTestUI(t *testing.T) *windowUI {
	t.Helper()
	a := test.NewApp()
	t.Cleanup(a.Quit)
	w := a.NewWindow("test")
	cfg := config.Default()
	cfg.Language = "it" // fixed language: the tests must not depend on the locale
	u := newUI(a, w, cfg, filepath.Join(t.TempDir(), "control.sock"))
	w.SetContent(u.root)
	return u
}

func TestApplySnapshotPopulatesWindow(t *testing.T) {
	u := newTestUI(t)
	snap := testSnapshot()

	applySnapshot(u.win, snap)

	if got := u.power.Text; got != "148 W" {
		t.Errorf("power = %q, expected %q", got, "148 W")
	}
	summary := captionText(u.summary)
	if !strings.Contains(summary, "2.40 kWh") ||
		!strings.Contains(summary, "0.60 €") ||
		!strings.Contains(summary, "media 120 W") ||
		!strings.Contains(summary, "picco 312 W") {
		t.Errorf("summary = %q", summary)
	}
	for _, c := range []struct{ key, want string }{
		{"vin", "230.4"}, {"v12", "12.10"}, {"i12", "8.20"},
		{"p12", "98.4"}, {"tvrm", "41.2"}, {"fan", "610 · 34%"},
	} {
		if got := u.cells[c.key].Text; got != c.want {
			t.Errorf("cell %s = %q, expected %q", c.key, got, c.want)
		}
	}
	if u.pauseBtn.Text != "Pausa" {
		t.Errorf("pause button = %q", u.pauseBtn.Text)
	}
	if u.modeSel.Selected != u.cat.Get("ui.mode.power") {
		t.Errorf("label select = %q", u.modeSel.Selected)
	}
	rows, cols := u.table.Length()
	if rows != 3 || cols != 5 { // header row + 2 days
		t.Errorf("table = %dx%d, expected 3x5", rows, cols)
	}
	if len(u.chart.series) != len(snap.History) || u.chart.alertW != 500 {
		t.Errorf("chart: series=%d threshold=%.0f", len(u.chart.series), u.chart.alertW)
	}

	// offline snapshot: the rail values go to "—"
	off := snap
	off.Online = false
	applySnapshot(u.win, off)
	if u.cells["v12"].Text != "—" {
		t.Errorf("offline: v12 = %q, expected %q", u.cells["v12"].Text, "—")
	}
}

func TestWideEntryMinimumWidthAndTyping(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()

	e := widget.NewEntry()
	box := newWideEntry(e, 90)
	if box.MinSize().Width < 90 {
		t.Fatalf("wide entry min width = %v, expected >= 90", box.MinSize().Width)
	}

	w := a.NewWindow("wide")
	w.SetContent(box)
	w.Resize(fyne.NewSize(300, 100))
	test.Type(box, "0.25")
	if e.Text != "0.25" {
		t.Errorf("entry text = %q, expected %q", e.Text, "0.25")
	}
}

func TestSummaryUsesConfiguredCurrency(t *testing.T) {
	u := newTestUI(t)
	snap := testSnapshot()
	snap.Currency = "USD"
	snap.Price = 0.30
	snap.TodayCost = 0.72

	applySnapshot(u.win, snap)

	summary := captionText(u.summary)
	if !strings.Contains(summary, "0.72 $") {
		t.Errorf("summary = %q, expected the USD symbol", summary)
	}
	if u.currencySel.Selected != "USD" {
		t.Errorf("currency select = %q, expected %q", u.currencySel.Selected, "USD")
	}
	if u.priceEntry.Text != "0.3" {
		t.Errorf("price entry = %q, expected %q", u.priceEntry.Text, "0.3")
	}
}

func TestTableLimitedTo14Days(t *testing.T) {
	u := newTestUI(t)
	snap := testSnapshot()
	snap.Days = nil
	for i := 0; i < 20; i++ {
		snap.Days = append(snap.Days, energy.Day{Date: "g", Wh: 1})
	}
	applySnapshot(u.win, snap)
	if rows, _ := u.table.Length(); rows != tableDays+1 { // +1 for the header row
		t.Errorf("table rows = %d, expected %d", rows, tableDays+1)
	}
}

func TestPowerChartEmptyHistoryNoPanic(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()

	c := newPowerChart()
	c.Resize(fyne.NewSize(700, 160))
	c.SetSeries(nil, 0) // no data: it must only show the waiting message

	r := c.CreateRenderer()
	r.Layout(fyne.NewSize(700, 160))
	objs := r.Objects()
	if len(objs) < 5 {
		t.Fatalf("renderer objects = %d, expected >= 5", len(objs))
	}
	if c.MinSize().Width <= 0 || c.MinSize().Height <= 0 {
		t.Errorf("MinSize not positive: %v", c.MinSize())
	}

	// with series + threshold: the segments appear, no panic
	now := time.Now()
	series := make([]energy.Sample, 0, 400)
	for i := 0; i < 400; i++ {
		series = append(series, energy.Sample{Time: now.Add(-time.Duration(i) * time.Second), W: float64(100 + i)})
	}
	c.SetSeries(series, 400)
	r.Refresh()
	if len(r.Objects()) <= len(objs) {
		t.Errorf("expected more segments after SetSeries")
	}
	r.Destroy()
}
