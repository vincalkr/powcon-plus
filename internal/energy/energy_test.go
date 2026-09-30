package energy

import (
	"math"
	"testing"
	"time"

	"powcon-plus/internal/psu"
)

func sampleAt(ts time.Time, w float64) *psu.Sample {
	return &psu.Sample{Time: ts, PTot: w, P12: w * 0.7, P5: w * 0.2, P33: w * 0.1}
}

func TestIntegrationTrapezoid(t *testing.T) {
	a := NewAccumulator(2*time.Second, 100, time.Second)
	t0 := time.Now()
	for i := 0; i < 10; i++ {
		a.Add(sampleAt(t0.Add(time.Duration(i)*time.Second), 100))
	}
	wh, secs, avg, max, _ := a.Session()
	want := 9 * 100.0 / 3600.0 // 9 intervals of 1 s at 100 W
	if math.Abs(wh-want) > 1e-9 {
		t.Errorf("Wh = %v, atteso %v", wh, want)
	}
	if secs != 9 {
		t.Errorf("secondi integrati = %v, atteso 9", secs)
	}
	if math.Abs(avg-100) > 1e-6 {
		t.Errorf("media = %v, atteso 100", avg)
	}
	if max != 100 {
		t.Errorf("picco = %v, atteso 100", max)
	}
	d := a.Today()
	if d.Samples != 9 {
		t.Errorf("campioni del giorno = %d, atteso 9", d.Samples)
	}
	if d.MaxW != 100 || d.MinW != 100 {
		t.Errorf("max/min = %v/%v, atteso 100/100", d.MaxW, d.MinW)
	}
	if math.Abs(d.Wh12-0.7*d.Wh) > 1e-9 || math.Abs(d.Wh5-0.2*d.Wh) > 1e-9 {
		t.Errorf("energie per rail non coerenti: %+v", d)
	}
	if d.Wh12 <= 0 || d.Wh5 <= 0 || d.Wh33 <= 0 {
		t.Errorf("energie per rail = %v/%v/%v, attese > 0", d.Wh12, d.Wh5, d.Wh33)
	}
}

// TestGapNotIntegrated: a gap larger than maxGap must not be integrated
// (PC off, daemon paused, sensor absent).
func TestGapNotIntegrated(t *testing.T) {
	a := NewAccumulator(2*time.Second, 100, time.Second)
	t0 := time.Now()
	a.Add(sampleAt(t0, 100))
	a.Add(sampleAt(t0.Add(30*time.Second), 100))
	wh, _, _, _, _ := a.Session()
	if wh != 0 {
		t.Fatalf("Wh = %v, atteso 0 (buco non integrato)", wh)
	}
	a.Add(sampleAt(t0.Add(31*time.Second), 100))
	wh, _, _, _, _ = a.Session()
	if math.Abs(wh-100.0/3600) > 1e-9 {
		t.Fatalf("Wh = %v, atteso %v", wh, 100.0/3600)
	}
}

func TestDaysSortedDescAndSetDays(t *testing.T) {
	a := NewAccumulator(2*time.Second, 100, time.Second)
	a.SetDays([]Day{
		{Date: "2026-01-01", Wh: 1000},
		{Date: "2026-01-03", Wh: 3000},
		{Date: "2026-01-02", Wh: 2000},
	})
	got := a.Days()
	if len(got) != 3 || got[0].Date != "2026-01-03" || got[2].Date != "2026-01-01" {
		t.Fatalf("ordinamento giorni: %+v", got)
	}
	if got[0].Costo(0.25) != 0.75 {
		t.Errorf("costo = %v, atteso 0.75", got[0].Costo(0.25))
	}
}

func TestHistoryWindow(t *testing.T) {
	a := NewAccumulator(2*time.Second, 3, 0)
	t0 := time.Now()
	for i := 0; i < 10; i++ {
		a.Add(sampleAt(t0.Add(time.Duration(i)*time.Second), float64(100+i)))
	}
	h := a.History()
	if len(h) != 3 {
		t.Fatalf("storico = %d campioni, atteso 3", len(h))
	}
	if h[2].W != 109 {
		t.Errorf("ultimo campione = %v, atteso 109", h[2].W)
	}
}
