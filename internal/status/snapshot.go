// Package status defines the shared "snapshot" between the daemon, the CLI, and the window.
package status

import (
	"fmt"
	"time"

	"github.com/vincalkr/powcon-plus/internal/config"
	"github.com/vincalkr/powcon-plus/internal/energy"
	"github.com/vincalkr/powcon-plus/internal/psu"
)

// Snapshot is the current state published to the tray, the CLI, and the GUI.
type Snapshot struct {
	Time   time.Time `json:"time"`
	Sensor string    `json:"sensor"`
	Online bool      `json:"online"`

	// latest PSU reading
	VIn   float64 `json:"vin"`
	V12   float64 `json:"v12"`
	V5    float64 `json:"v5"`
	V33   float64 `json:"v33"`
	I12   float64 `json:"i12"`
	I5    float64 `json:"i5"`
	I33   float64 `json:"i33"`
	W     float64 `json:"w"`
	P12   float64 `json:"p12"`
	P5    float64 `json:"p5"`
	P33   float64 `json:"p33"`
	TVRM  float64 `json:"tvrm"`
	TCase float64 `json:"tcase"`
	Fan   float64 `json:"fan"`
	PWM   float64 `json:"pwm"`

	// current daemon session
	SessionWh      float64   `json:"session_wh"`
	SessionSeconds float64   `json:"session_seconds"`
	SessionAvgW    float64   `json:"session_avg_w"`
	SessionMaxW    float64   `json:"session_max_w"`
	SessionMaxAt   time.Time `json:"session_max_at,omitempty"`

	// today
	TodayDate    string  `json:"today_date"`
	TodayWh      float64 `json:"today_wh"`
	TodayMaxW    float64 `json:"today_max_w"`
	TodayMinW    float64 `json:"today_min_w"`
	TodaySamples int     `json:"today_samples"`
	TodaySeconds float64 `json:"today_seconds"`
	TodayAvgW    float64 `json:"today_avg_w"`
	TodayCost    float64 `json:"today_cost"`

	// effective configuration
	Paused    bool             `json:"paused"`
	LabelMode config.LabelMode `json:"label_mode"`
	AlertW    float64          `json:"alert_w"`
	Price     float64          `json:"price_per_kwh"`
	Currency  string           `json:"currency"`

	Days    []energy.Day    `json:"days,omitempty"`
	History []energy.Sample `json:"history,omitempty"`
}

// Build assembles a snapshot from the daemon state.
func Build(s *psu.Sample, acc *energy.Accumulator, sensor string, paused bool,
	mode config.LabelMode, alertW, price float64, currency string,
	days []energy.Day, hist []energy.Sample) Snapshot {
	snap := Snapshot{
		Time:      time.Now(),
		Sensor:    sensor,
		Paused:    paused,
		LabelMode: mode,
		AlertW:    alertW,
		Price:     price,
		Currency:  currency,
		Days:      days,
		History:   hist,
	}
	if s != nil {
		snap.Online = true
		snap.VIn, snap.V12, snap.V5, snap.V33 = s.VIn, s.V12, s.V5, s.V33
		snap.I12, snap.I5, snap.I33 = s.I12, s.I5, s.I33
		snap.W, snap.P12, snap.P5, snap.P33 = s.PTot, s.P12, s.P5, s.P33
		snap.TVRM, snap.TCase, snap.Fan, snap.PWM = s.TVRM, s.TCase, s.Fan, s.PWM
	}
	wh, secs, avg, max, maxAt := acc.Session()
	snap.SessionWh, snap.SessionSeconds, snap.SessionAvgW, snap.SessionMaxW, snap.SessionMaxAt = wh, secs, avg, max, maxAt
	d := acc.Today()
	snap.TodayDate = d.Date
	snap.TodayWh = d.Wh
	snap.TodayMaxW = d.MaxW
	snap.TodayMinW = d.MinW
	snap.TodaySamples = d.Samples
	snap.TodaySeconds = d.DurationS
	snap.TodayAvgW = d.AvgW()
	snap.TodayCost = d.Cost(price)
	return snap
}

// Label renders the label to show next to the icon.
func (s Snapshot) Label(c config.Config) string {
	switch s.LabelMode {
	case config.ModeNone:
		return ""
	case config.ModePower:
		return s.WLabel(c.FormatW)
	case config.ModeEnergy:
		return s.ELabel(c)
	default:
		return fmt.Sprintf("%s · %s", s.WLabel(c.FormatW), s.ELabel(c))
	}
}

// WLabel is the formatted instantaneous power.
func (s Snapshot) WLabel(format string) string {
	if format == "" {
		format = "%.0f W"
	}
	return fmt.Sprintf(format, s.W)
}

// ELabel is today's energy (Wh below 1 kWh, otherwise kWh).
func (s Snapshot) ELabel(c config.Config) string {
	wh := s.TodayWh
	if wh < 1000 {
		return fmt.Sprintf("%.0f Wh", wh)
	}
	if c.FormatWh == "" {
		return fmt.Sprintf("%.2f kWh", wh/1000)
	}
	return fmt.Sprintf(c.FormatWh, wh/1000)
}
