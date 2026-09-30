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

	"powcon-plus/internal/config"
	"powcon-plus/internal/energy"
	"powcon-plus/internal/status"
)

func snapshotDiProva() status.Snapshot {
	now := time.Now()
	return status.Snapshot{
		Time: now, Sensor: "/sys/class/hwmon/hwmon0", Online: true,
		VIn: 230.4, V12: 12.1, V5: 5.02, V33: 3.31,
		I12: 8.2, I5: 1.1, I33: 0.6,
		W: 148.2, P12: 98.4, P5: 5.5, P33: 2.0,
		TVRM: 41.2, TCase: 37.5, Fan: 610, PWM: 34,
		TodayWh: 2400, TodayAvgW: 120, TodayMaxW: 312, TodayCostEuro: 0.6,
		LabelMode: config.ModePower, AlertW: 500, Price: 0.25,
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
	u := newUI(a, w, config.Default(), filepath.Join(t.TempDir(), "control.sock"))
	w.SetContent(u.root)
	return u
}

func TestApplySnapshotPopolaFinestra(t *testing.T) {
	u := newTestUI(t)
	snap := snapshotDiProva()

	applySnapshot(u.win, snap)

	if got := u.power.Text; got != "148 W" {
		t.Errorf("potenza = %q, atteso %q", got, "148 W")
	}
	if !strings.Contains(u.summary.Text, "2.40 kWh") ||
		!strings.Contains(u.summary.Text, "0.60 €") ||
		!strings.Contains(u.summary.Text, "media 120 W") ||
		!strings.Contains(u.summary.Text, "picco 312 W") {
		t.Errorf("riepilogo = %q", u.summary.Text)
	}
	for _, c := range []struct{ key, want string }{
		{"vin", "230.4"}, {"v12", "12.10"}, {"i12", "8.20"},
		{"p12", "98.4"}, {"tvrm", "41.2"}, {"fan", "610 · 34%"},
	} {
		if got := u.cells[c.key].Text; got != c.want {
			t.Errorf("cella %s = %q, atteso %q", c.key, got, c.want)
		}
	}
	if u.pauseBtn.Text != "Pausa" {
		t.Errorf("pulsante pausa = %q", u.pauseBtn.Text)
	}
	if u.modeSel.Selected != string(config.ModePower) {
		t.Errorf("select etichetta = %q", u.modeSel.Selected)
	}
	rows, cols := u.table.Length()
	if rows != 2 || cols != 5 {
		t.Errorf("tabella = %dx%d, atteso 2x5", rows, cols)
	}
	if len(u.chart.series) != len(snap.History) || u.chart.alertW != 500 {
		t.Errorf("grafico: serie=%d soglia=%.0f", len(u.chart.series), u.chart.alertW)
	}

	// offline snapshot: the rail values go to "—"
	off := snap
	off.Online = false
	applySnapshot(u.win, off)
	if u.cells["v12"].Text != "—" {
		t.Errorf("offline: v12 = %q, atteso %q", u.cells["v12"].Text, "—")
	}
}

func TestTabellaLimitataA14Giorni(t *testing.T) {
	u := newTestUI(t)
	snap := snapshotDiProva()
	snap.Days = nil
	for i := 0; i < 20; i++ {
		snap.Days = append(snap.Days, energy.Day{Date: "g", Wh: 1})
	}
	applySnapshot(u.win, snap)
	if rows, _ := u.table.Length(); rows != tableDays {
		t.Errorf("righe tabella = %d, atteso %d", rows, tableDays)
	}
}

func TestPowerChartHistoryVuotaNonPanicca(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()

	c := newPowerChart()
	c.Resize(fyne.NewSize(700, 160))
	c.SetSeries(nil, 0) // no data: it must only show the waiting message

	r := c.CreateRenderer()
	r.Layout(fyne.NewSize(700, 160))
	objs := r.Objects()
	if len(objs) < 5 {
		t.Fatalf("oggetti renderer = %d, atteso >= 5", len(objs))
	}
	if c.MinSize().Width <= 0 || c.MinSize().Height <= 0 {
		t.Errorf("MinSize non positiva: %v", c.MinSize())
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
		t.Errorf("attesi segmenti in più dopo SetSeries")
	}
	r.Destroy()
}
