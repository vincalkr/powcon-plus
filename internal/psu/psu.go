// Package psu reads the Corsair PSU (HX/AX "i" series) through the kernel
// hwmon driver corsairpsu, without cgo and without root privileges.
//
// The driver exposes /sys/class/hwmon/hwmonN with name == "corsairpsu". The
// sysfs attributes follow the hwmon convention: millivolts, milliamperes,
// microwatts, millidegrees Celsius.
package psu

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrNotReady indicates that the PSU is not exposed (USB cable, driver not
// loaded, or the device was claimed by another iCUE-like service).
var ErrNotReady = errors.New("corsairpsu sensor unavailable")

// Sample is a complete reading of the PSU.
type Sample struct {
	Time time.Time `json:"time"`

	VIn   float64 `json:"vin"`   // V AC input
	V12   float64 `json:"v12"`   // V
	V5    float64 `json:"v5"`    // V
	V33   float64 `json:"v33"`   // V
	I12   float64 `json:"i12"`   // A
	I5    float64 `json:"i5"`    // A
	I33   float64 `json:"i33"`   // A
	PTot  float64 `json:"ptot"`  // W AC drawn from the wall = workstation consumption
	P12   float64 `json:"p12"`   // W on the +12V rail
	P5    float64 `json:"p5"`    // W on the +5V rail
	P33   float64 `json:"p33"`   // W on the +3.3V rail
	TVRM  float64 `json:"tvrm"`  // °C
	TCase float64 `json:"tcase"` // °C
	Fan   float64 `json:"fan"`   // RPM
	PWM   float64 `json:"pwm"`   // % fan duty cycle
}

// Efficiency is the ratio of estimated useful rail power to AC power.
// Note: it does not cover the 5VSB/other rails, so it is only indicative.
func (s *Sample) Efficiency() float64 {
	if s.PTot <= 0 {
		return 0
	}
	return (s.P12 + s.P5 + s.P33) / s.PTot
}

// Reader reads a single hwmon chip.
type Reader struct {
	Dir     string            // /sys/class/hwmon/hwmonN
	Channel map[string]string // logical key -> *_input file
	Labels  map[string]string // *_label file -> text
}

// logical keys
const (
	kVIn = "vin"
	kV12 = "v12"
	kV5  = "v5"
	kV33 = "v33"
	kI12 = "i12"
	kI5  = "i5"
	kI33 = "i33"
	kPT  = "ptot"
	kP12 = "p12"
	kP5  = "p5"
	kP33 = "p33"
	kTV  = "tvrm"
	kTC  = "tcase"
	kFan = "fan"
)

// Detect locates the hwmon directory of the PSU.
// hint can be a name ("hwmon8"), an absolute path, or "" for auto-detect.
func Detect(hint string) (string, error) {
	if hint != "" {
		p := hint
		if !strings.HasPrefix(p, "/") {
			p = filepath.Join("/sys/class/hwmon", p)
		}
		name, err := os.ReadFile(filepath.Join(p, "name"))
		if err != nil {
			return "", fmt.Errorf("%s: %w", p, err)
		}
		if n := strings.TrimSpace(string(name)); n != Chip {
			return "", fmt.Errorf("%s: chip name %q, expected %q", p, n, Chip)
		}
		return p, nil
	}
	matches, err := filepath.Glob("/sys/class/hwmon/hwmon*")
	if err != nil {
		return "", err
	}
	sort.Strings(matches)
	for _, m := range matches {
		name, err := os.ReadFile(filepath.Join(m, "name"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(name)) == Chip {
			return m, nil
		}
	}
	return "", fmt.Errorf("%w: no hwmon with name=%q", ErrNotReady, Chip)
}

// Chip is the name exposed by the kernel driver.
const Chip = "corsairpsu"

// New builds a Reader on the given hwmon directory, resolving channels by
// driver label (robust against index changes).
func New(dir string) (*Reader, error) {
	r := &Reader{Dir: dir, Channel: map[string]string{}, Labels: map[string]string{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	// first pass: read the labels
	labelFor := map[string]string{} // normalized label -> base file (e.g. "power1")
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, "_label") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		key := normalize(string(b))
		base := strings.TrimSuffix(n, "_label")
		labelFor[key] = base
		r.Labels[base] = strings.TrimSpace(string(b))
	}

	// label-based mapping: holds up if the driver reorders channels
	byLabel := map[string]string{
		"vin":        kVIn,
		"vout+12v":   kV12,
		"vout+5v":    kV5,
		"vout+33v":   kV33,
		"currint":    kI12, // the driver does not expose curr1_input: indicative
		"curr+12v":   kI12,
		"curr+5v":    kI5,
		"curr+33v":   kI33,
		"powertotal": kPT,
		"power+12v":  kP12,
		"power+5v":   kP5,
		"power+33v":  kP33,
		"vrmtemp":    kTV,
		"casetemp":   kTC,
		"psufan":     kFan,
	}
	for label, base := range labelFor {
		if key, ok := byLabel[label]; ok {
			if _, exists := r.Channel[key]; !exists || label != "currint" {
				r.Channel[key] = base
			}
		}
	}

	// fallback for the classic corsairpsu driver indexing
	fallback := map[string]string{
		"in0": kVIn, "in1": kV12, "in2": kV5, "in3": kV33,
		"curr2": kI12, "curr3": kI5, "curr4": kI33,
		"power1": kPT, "power2": kP12, "power3": kP5, "power4": kP33,
		"temp1": kTV, "temp2": kTC,
		"fan1": kFan,
	}
	for base, key := range fallback {
		if _, ok := r.Channel[key]; ok {
			continue
		}
		if fileExists(dir, base+"_input") {
			r.Channel[key] = base
		}
	}
	if _, ok := r.Channel[kPT]; !ok {
		return nil, fmt.Errorf("%w: no power channel in %s", ErrNotReady, dir)
	}
	return r, nil
}

func fileExists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func normalize(s string) string {
	s = strings.ToLower(s)
	for _, r := range []string{" ", "\t", "\n", ".", "(", ")", "-"} {
		s = strings.ReplaceAll(s, r, "")
	}
	return s
}

// Read acquires a sample.
func (r *Reader) Read() (*Sample, error) {
	s := &Sample{Time: time.Now()}
	var firstErr error
	get := func(key string, scale float64, dst *float64) {
		base, ok := r.Channel[key]
		if !ok {
			return
		}
		v, err := readInt(filepath.Join(r.Dir, base+"_input"))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return
		}
		*dst = float64(v) / scale
	}
	get(kVIn, 1e3, &s.VIn)
	get(kV12, 1e3, &s.V12)
	get(kV5, 1e3, &s.V5)
	get(kV33, 1e3, &s.V33)
	get(kI12, 1e3, &s.I12)
	get(kI5, 1e3, &s.I5)
	get(kI33, 1e3, &s.I33)
	get(kPT, 1e6, &s.PTot)
	get(kP12, 1e6, &s.P12)
	get(kP5, 1e6, &s.P5)
	get(kP33, 1e6, &s.P33)
	get(kTV, 1e3, &s.TVRM)
	get(kTC, 1e3, &s.TCase)
	get(kFan, 1, &s.Fan)

	if v, err := readInt(filepath.Join(r.Dir, "pwm1")); err == nil {
		s.PWM = float64(v)
	} else if v, err := readInt(filepath.Join(r.Dir, "pwm1_input")); err == nil {
		s.PWM = float64(v)
	}
	if s.PTot == 0 && firstErr != nil {
		return nil, fmt.Errorf("reading power: %w", firstErr)
	}
	return s, nil
}

func readInt(path string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
}

// Available is a quick check for CLI / diagnostics.
func Available(hint string) (string, bool) {
	p, err := Detect(hint)
	if err != nil {
		return "", false
	}
	return p, true
}
