// Package energy integrates power over time to obtain Wh/kWh and produces
// daily summaries.
package energy

import (
	"math"
	"time"

	"powcon-plus/internal/psu"
)

// Day is the summary of one day (local timezone).
type Day struct {
	Date      string    `json:"date"`
	Wh        float64   `json:"wh"`   // AC energy drawn from the wall
	Wh12      float64   `json:"wh12"` // estimated energy on the +12V rail
	Wh5       float64   `json:"wh5"`
	Wh33      float64   `json:"wh33"`
	Samples   int       `json:"samples"`
	MaxW      float64   `json:"max_w"`
	MinW      float64   `json:"min_w"`
	DurationS float64   `json:"duration_s"` // interval covered by the measurements
	Updated   time.Time `json:"updated"`
}

// kWh is the energy in kilowatt-hours.
func (d Day) kWh() float64 { return d.Wh / 1000 }

// AvgW is the average power over the covered interval.
func (d Day) AvgW() float64 {
	if d.DurationS <= 0 {
		return 0
	}
	return d.Wh * 3600 / d.DurationS
}

// Costo estimates the expense at the given tariff (€/kWh).
func (d Day) Costo(eurPerKWh float64) float64 { return d.kWh() * eurPerKWh }

// kWh is the energy in kilowatt-hours.
func (s *Sample) kWh() float64 { return s.Wh / 1000 }

// Sample is an aggregated sample for the live chart.
type Sample struct {
	Time time.Time `json:"time"`
	W    float64   `json:"w"`
	Wh   float64   `json:"wh"` // Wh since session start
}

// Accumulator integrates samples. Not thread-safe: used by the collection loop.
type Accumulator struct {
	maxGap time.Duration

	prev   *psu.Sample
	prevAt time.Time

	start    time.Time
	session  float64 // Wh since program start
	integS   float64 // seconds actually integrated
	days     map[string]*Day
	maxW     float64
	minW     float64
	maxWAt   time.Time
	last     Sample
	maxHist  int
	hist     []Sample
	histStep time.Duration
	lastHist time.Time
}

// NewAccumulator creates an integrator. maxGap is the gap beyond which no
// integration happens (the PC was off, the daemon paused, the sensor absent).
func NewAccumulator(maxGap time.Duration, historySize int, historyStep time.Duration) *Accumulator {
	if maxGap <= 0 {
		maxGap = 5 * time.Second
	}
	return &Accumulator{
		maxGap:   maxGap,
		days:     map[string]*Day{},
		minW:     math.Inf(1),
		maxHist:  historySize,
		histStep: historyStep,
	}
}

// Add integrates one sample using the trapezoidal rule.
func (a *Accumulator) Add(s *psu.Sample) {
	now := s.Time
	if a.start.IsZero() {
		a.start = now
	}
	day := a.dayFor(now)
	if a.prev != nil {
		dt := now.Sub(a.prevAt)
		// A gap wider than maxGap means the machine was off, the daemon was
		// paused or the sensor was gone: no energy is credited for it.
		if dt > 0 && dt <= a.maxGap {
			h := dt.Seconds() / 3600.0
			a.session += (s.PTot + a.prev.PTot) / 2 * h
			a.integS += dt.Seconds()

			day.Wh += (s.PTot + a.prev.PTot) / 2 * h
			day.Wh12 += (s.P12 + a.prev.P12) / 2 * h
			day.Wh5 += (s.P5 + a.prev.P5) / 2 * h
			day.Wh33 += (s.P33 + a.prev.P33) / 2 * h
			day.Samples++
			day.DurationS += dt.Seconds()
		}
	}
	day.Updated = now
	if s.PTot > day.MaxW {
		day.MaxW = s.PTot
	}
	if day.MinW == 0 || s.PTot < day.MinW {
		day.MinW = s.PTot
	}
	if s.PTot > a.maxW {
		a.maxW, a.maxWAt = s.PTot, now
	}
	if s.PTot < a.minW {
		a.minW = s.PTot
	}
	a.prev = s
	a.prevAt = now

	a.last = Sample{Time: now, W: s.PTot, Wh: a.session}
	if a.maxHist > 0 && (a.histStep <= 0 || now.Sub(a.lastHist) >= a.histStep) {
		a.hist = append(a.hist, a.last)
		if len(a.hist) > a.maxHist {
			a.hist = append(a.hist[:0], a.hist[len(a.hist)-a.maxHist:]...)
		}
		a.lastHist = now
	}
}

func (a *Accumulator) dayFor(t time.Time) *Day {
	key := t.Local().Format("2006-01-02")
	d, ok := a.days[key]
	if !ok {
		d = &Day{Date: key, MinW: math.Inf(1)}
		a.days[key] = d
	}
	if d.MinW == math.Inf(1) {
		d.MinW = 0
	}
	return d
}

// Today returns the summary of the current day.
func (a *Accumulator) Today() Day {
	key := time.Now().Local().Format("2006-01-02")
	d, ok := a.days[key]
	if !ok {
		return Day{Date: key}
	}
	out := *d
	if out.MinW == math.Inf(1) {
		out.MinW = 0
	}
	return out
}

// Days returns the summaries from the most recent to the oldest.
func (a *Accumulator) Days() []Day {
	out := make([]Day, 0, len(a.days))
	for _, d := range a.days {
		c := *d
		if c.MinW == math.Inf(1) {
			c.MinW = 0
		}
		out = append(out, c)
	}
	sortDays(out)
	return out
}

// Session returns Wh, integrated seconds, and the average/peak of the current session.
func (a *Accumulator) Session() (wh, seconds, avgW, maxW float64, maxWAt time.Time) {
	wh = a.session
	seconds = a.integS
	if seconds > 0 {
		avgW = wh * 3600 / seconds
	}
	return wh, seconds, avgW, a.maxW, a.maxWAt
}

// Last returns the last aggregated sample.
func (a *Accumulator) Last() Sample { return a.last }

// History returns the history for the live chart.
func (a *Accumulator) History() []Sample {
	out := make([]Sample, len(a.hist))
	copy(out, a.hist)
	return out
}

// SetDays restores the summaries loaded from the store (on daemon restart).
func (a *Accumulator) SetDays(days []Day) {
	for i := range days {
		d := days[i]
		if _, ok := a.days[d.Date]; !ok {
			a.days[d.Date] = &d
		}
	}
}

func sortDays(d []Day) {
	for i := 1; i < len(d); i++ {
		for j := i; j > 0 && d[j].Date > d[j-1].Date; j-- {
			d[j], d[j-1] = d[j-1], d[j]
		}
	}
}
