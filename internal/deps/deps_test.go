package deps

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseGoVersion(t *testing.T) {
	cases := []struct {
		in           string
		major, minor int
		ok           bool
	}{
		{"go version go1.26.0 linux/amd64", 1, 26, true},
		{"go version go1.24 linux/amd64", 1, 24, true},
		{"go version go1.9.2 linux/amd64", 1, 9, true},
		{"gcc (Ubuntu) 13.2.0", 0, 0, false},
	}
	for _, c := range cases {
		major, minor, ok := parseGoVersion(c.in)
		if ok != c.ok || major != c.major || minor != c.minor {
			t.Errorf("parseGoVersion(%q) = %d.%d ok=%v, want %d.%d ok=%v",
				c.in, major, minor, ok, c.major, c.minor, c.ok)
		}
	}
}

func TestMissingEssential(t *testing.T) {
	fs := []Finding{
		{Name: "psu sensor", Status: StatusMissing, Essential: true},
		{Name: "tray host", Status: StatusMissing, Essential: false},
		{Name: "session bus", Status: StatusOK, Essential: true},
		{Name: "gui libraries", Status: StatusWarn, Essential: false},
		{Name: "skipped thing", Status: StatusSkip, Essential: true},
	}
	got := MissingEssential(fs)
	if len(got) != 1 || got[0].Name != "psu sensor" {
		t.Errorf("MissingEssential = %+v, want only psu sensor", got)
	}
}

func TestReportShowsFixesAndEssentialMark(t *testing.T) {
	fs := []Finding{
		{Name: "psu sensor", Status: StatusMissing, Detail: "no hwmon", Fix: "connect the cable", Essential: true},
		{Name: "tray host", Status: StatusOK, Detail: "enabled"},
		{Name: "gui libraries", Status: StatusSkip, Detail: "nothing to inspect", Fix: "not shown"},
	}
	var buf bytes.Buffer
	Report(&buf, fs)
	out := buf.String()

	for _, want := range []string{"missing*", "connect the cable", "no hwmon"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	// A fix attached to an ok/skip finding must not be printed.
	if strings.Contains(out, "not shown") {
		t.Errorf("report printed a fix for a skipped finding:\n%s", out)
	}
	// Every data line starts with two spaces and a status column.
	for _, line := range strings.Split(out, "\n") {
		if line == "" || strings.HasPrefix(line, "\n") {
			continue
		}
		if !strings.HasPrefix(line, "  ") {
			t.Errorf("report line not indented: %q", line)
		}
	}
}

// Check must cover the requirements documented in the README.
func TestCheckCoversDocumentedRequirements(t *testing.T) {
	want := []string{"psu sensor", "session bus", "tray host", "gui libraries"}
	got := Check(Options{})
	if len(got) != len(want) {
		t.Fatalf("Check returned %d findings, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("finding %d = %q, want %q", i, got[i].Name, name)
		}
	}
	if withBuild := Check(Options{Build: true}); len(withBuild) <= len(got) {
		t.Errorf("Options{Build:true} returned %d findings, want more than %d",
			len(withBuild), len(got))
	}
}

func TestPkgManagerIsConsistent(t *testing.T) {
	name, install := pkgManager()
	if (name == "") != (install == "") {
		t.Errorf("pkgManager = (%q, %q): name and command must agree", name, install)
	}
}
