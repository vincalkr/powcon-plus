// Package store persists samples (JSONL) and the daily summaries.
//
// Layout inside DataDir:
//
//	daily.json                     daily summaries (atomic write)
//	samples-2026-09-30.jsonl       per-day sample files
//	lock                           single-instance flock
//	control.sock                 daemon control socket
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vincalkr/powcon-plus/internal/energy"
	"github.com/vincalkr/powcon-plus/internal/psu"
)

// Store is the on-disk repository.
type Store struct {
	Dir string
}

// New creates the data directory if needed.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{Dir: dir}, nil
}

func (s *Store) dailyPath() string { return filepath.Join(s.Dir, "daily.json") }

// LoadDaily reads the daily summaries (oldest -> newest).
func (s *Store) LoadDaily() ([]energy.Day, error) {
	b, err := os.ReadFile(s.dailyPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var days []energy.Day
	if err := json.Unmarshal(b, &days); err != nil {
		return nil, fmt.Errorf("%s: %w", s.dailyPath(), err)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })
	return days, nil
}

// SaveDaily writes the daily summaries atomically.
func (s *Store) SaveDaily(days []energy.Day) error {
	tmp := s.dailyPath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(days); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.dailyPath())
}

// SampleLine is one line of the samples JSONL.
type SampleLine struct {
	TS  time.Time `json:"ts"`
	W   float64   `json:"w"`
	VIn float64   `json:"vin,omitempty"`
	P12 float64   `json:"p12,omitempty"`
	P5  float64   `json:"p5,omitempty"`
	P33 float64   `json:"p33,omitempty"`
	V12 float64   `json:"v12,omitempty"`
	T   float64   `json:"t,omitempty"`
	Fan float64   `json:"fan,omitempty"`
}

// AppendSample appends a sample to the day's file.
func (s *Store) AppendSample(smp *psu.Sample) error {
	line := SampleLine{
		TS:  smp.Time,
		W:   smp.PTot,
		VIn: smp.VIn,
		P12: smp.P12,
		P5:  smp.P5,
		P33: smp.P33,
		V12: smp.V12,
		T:   smp.TVRM,
		Fan: smp.Fan,
	}
	b, err := json.Marshal(line)
	if err != nil {
		return err
	}
	path := filepath.Join(s.Dir, "samples-"+smp.Time.Local().Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// ReadSamples re-reads the samples of one day (for the window's chart).
func (s *Store) ReadSamples(date string) ([]SampleLine, error) {
	path := filepath.Join(s.Dir, "samples-"+date+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []SampleLine
	dec := json.NewDecoder(f)
	for {
		var l SampleLine
		if err := dec.Decode(&l); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return out, err
		}
		out = append(out, l)
	}
	return out, nil
}

// Prune removes sample files older than retentionDays (0 = do nothing).
func (s *Store) Prune(retentionDays int) (int, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	cut := time.Now().AddDate(0, 0, -retentionDays).Format("2006-01-02")
	matches, err := filepath.Glob(filepath.Join(s.Dir, "samples-*.jsonl"))
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, m := range matches {
		base := strings.TrimPrefix(filepath.Base(m), "samples-")
		base = strings.TrimSuffix(base, ".jsonl")
		if base < cut {
			if err := os.Remove(m); err == nil {
				removed++
			}
		}
	}
	return removed, nil
}

// Lock is the single-instance lock.
type Lock struct {
	f *os.File
}

// AcquireLock takes the flock on the given file.
func AcquireLock(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("instance already running (%s): %w", path, err)
	}
	if _, err := f.Write([]byte(strconv.Itoa(os.Getpid()) + "\n")); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Release releases the lock.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
	l.f = nil
}
