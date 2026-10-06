package psu

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeHWMON builds a fake hwmon directory identical to the driver's.
func fakeHWMON(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"name":         "corsairpsu\n",
		"in0_input":    "230000",
		"in0_label":    "v_in\n",
		"in1_input":    "11921",
		"in1_label":    "v_out +12v\n",
		"in2_input":    "5015",
		"in2_label":    "v_out +5v\n",
		"in3_input":    "3312",
		"in3_label":    "v_out +3.3v\n",
		"curr2_input":  "7500",
		"curr2_label":  "curr +12v\n",
		"curr3_input":  "9250",
		"curr3_label":  "curr +5v\n",
		"curr4_input":  "1812",
		"curr4_label":  "curr +3.3v\n",
		"power1_input": "148000000",
		"power1_label": "power total\n",
		"power2_input": "98000000",
		"power2_label": "power +12v\n",
		"power3_input": "46000000",
		"power3_label": "power +5v\n",
		"power4_input": "6000000",
		"power4_label": "power +3.3v\n",
		"temp1_input":  "55500",
		"temp1_label":  "vrm temp\n",
		"temp2_input":  "58250",
		"temp2_label":  "case temp\n",
		"fan1_input":   "0",
		"fan1_label":   "psu fan\n",
		"pwm1":         "34",
	}
	for n, v := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestReadMappingAndScale(t *testing.T) {
	dir := fakeHWMON(t)
	rd, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := rd.Read()
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		got  float64
		want float64
	}{
		{"VIn", s.VIn, 230.0},
		{"V12", s.V12, 11.921},
		{"V5", s.V5, 5.015},
		{"V33", s.V33, 3.312},
		{"I12", s.I12, 7.5},
		{"I5", s.I5, 9.25},
		{"I33", s.I33, 1.812},
		{"PTot", s.PTot, 148.0},
		{"P12", s.P12, 98.0},
		{"P5", s.P5, 46.0},
		{"P33", s.P33, 6.0},
		{"TVRM", s.TVRM, 55.5},
		{"TCase", s.TCase, 58.25},
		{"PWM", s.PWM, 34},
	}
	for _, c := range checks {
		if diff := c.got - c.want; diff > 1e-6 || diff < -1e-6 {
			t.Errorf("%s = %v, expected %v", c.name, c.got, c.want)
		}
	}
	if s.Time.IsZero() {
		t.Error("Time not populated")
	}
}

// TestReadReorderedChannels verifies that the label-based mapping holds up
// when the driver changes the channel indices.
func TestReadReorderedChannels(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "name"), []byte("corsairpsu\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "power7_input"), []byte("250000000"), 0o644)
	os.WriteFile(filepath.Join(dir, "power7_label"), []byte("power total\n"), 0o644)
	rd, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := rd.Read()
	if err != nil {
		t.Fatal(err)
	}
	if s.PTot != 250 {
		t.Fatalf("PTot = %v, expected 250", s.PTot)
	}
}

func TestDetectMissing(t *testing.T) {
	if _, err := Detect("hwmon-non-esistente"); err == nil {
		t.Fatal("expected error for missing hwmon")
	}
}
