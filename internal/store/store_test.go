package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"powcon-plus/internal/energy"
	"powcon-plus/internal/psu"
)

func TestDailyRoundTrip(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	days := []energy.Day{
		{Date: "2026-01-01", Wh: 1500, MaxW: 700, MinW: 40, Samples: 100, DurationS: 3600},
		{Date: "2026-01-02", Wh: 2200, MaxW: 820, MinW: 38, Samples: 200, DurationS: 7200},
	}
	if err := s.SaveDaily(days); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadDaily()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Date != "2026-01-01" || got[1].Wh != 2200 {
		t.Fatalf("round-trip daily: %+v", got)
	}
	// atomic write: no leftover temp file
	if _, err := os.Stat(s.dailyPath() + ".tmp"); !os.IsNotExist(err) {
		t.Error("file temporaneo residuo dopo SaveDaily")
	}
}

func TestSamplesAppendReadPrune(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.AddDate(0, 0, -40)
	for _, ts := range []time.Time{now, now.Add(time.Second), old} {
		if err := s.AppendSample(&psu.Sample{Time: ts, PTot: 123.5, VIn: 230}); err != nil {
			t.Fatal(err)
		}
	}
	lines, err := s.ReadSamples(now.Local().Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].W != 123.5 {
		t.Fatalf("campioni letti: %+v", lines)
	}
	removed, err := s.Prune(31)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("file rimossi = %d, atteso 1", removed)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "samples-"+old.Local().Format("2006-01-02")+".jsonl")); !os.IsNotExist(err) {
		t.Error("il file oltre la retention non è stato rimosso")
	}
	if n, err := s.Prune(0); err != nil || n != 0 {
		t.Errorf("Prune(0) = %d,%v, atteso 0,nil", n, err)
	}
}

func TestLockExclusive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock")
	l1, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(path); err == nil {
		t.Fatal("atteso errore di lock esclusivo")
	}
	l1.Release()
	l2, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("dopo Release: %v", err)
	}
	l2.Release()
}
