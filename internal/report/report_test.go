package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vincalkr/powcon-plus/internal/energy"
)

func days5() []energy.Day {
	return []energy.Day{
		{Date: "2026-10-01", Wh: 1000, DurationS: 3600},
		{Date: "2026-10-02", Wh: 2000, DurationS: 3600},
		{Date: "2026-10-03", Wh: 3000, DurationS: 3600},
		{Date: "2026-10-04", Wh: 4000, DurationS: 3600},
		{Date: "2026-10-05", Wh: 5000, DurationS: 3600},
	}
}

func datesOf(d []energy.Day) []string {
	out := make([]string, 0, len(d))
	for _, x := range d {
		out = append(out, x.Date)
	}
	return out
}

func TestMostRecentNewestFirst(t *testing.T) {
	got := strings.Join(datesOf(MostRecent(days5(), 3)), ",")
	want := "2026-10-05,2026-10-04,2026-10-03"
	if got != want {
		t.Errorf("MostRecent(5 days, 3) = %s, want %s", got, want)
	}
}

func TestMostRecentKeepsAllWhenNotLimited(t *testing.T) {
	for _, n := range []int{0, -1, 99} {
		got := strings.Join(datesOf(MostRecent(days5(), n)), ",")
		want := "2026-10-05,2026-10-04,2026-10-03,2026-10-02,2026-10-01"
		if got != want {
			t.Errorf("MostRecent(5 days, %d) = %s, want %s", n, got, want)
		}
	}
}

func TestMostRecentIgnoresInputOrder(t *testing.T) {
	asc := days5()
	desc := make([]energy.Day, len(asc))
	for i := range asc {
		desc[i] = asc[len(asc)-1-i]
	}
	if a, b := datesOf(MostRecent(asc, 2)), datesOf(MostRecent(desc, 2)); strings.Join(a, ",") != strings.Join(b, ",") {
		t.Errorf("input order changed the result: %v vs %v", a, b)
	}
}

func TestMostRecentDoesNotTouchInput(t *testing.T) {
	in := days5()
	_ = MostRecent(in, 2)
	if in[0].Date != "2026-10-01" {
		t.Errorf("input reordered: first day is now %s", in[0].Date)
	}
}

func TestMergeNewerPrefersLiveAndAppends(t *testing.T) {
	base := []energy.Day{
		{Date: "2026-10-01", Wh: 1000},
		{Date: "2026-10-02", Wh: 2000}, // stale today on disk
	}
	live := []energy.Day{
		{Date: "2026-10-02", Wh: 2500}, // fresher today from the snapshot
		{Date: "2026-10-03", Wh: 100},  // a day not on disk yet
	}
	got := MergeNewer(base, live)
	if len(got) != 3 {
		t.Fatalf("MergeNewer kept %d days, want 3", len(got))
	}
	if got[1].Date != "2026-10-02" || got[1].Wh != 2500 {
		t.Errorf("today not refreshed: %+v", got[1])
	}
	if got[2].Date != "2026-10-03" {
		t.Errorf("missing day appended from the snapshot: %+v", got[2])
	}
	if base[1].Wh != 2000 {
		t.Errorf("MergeNewer modified its input: base[1].Wh = %v", base[1].Wh)
	}
}

func TestWriteCSV(t *testing.T) {
	var b bytes.Buffer
	days := []energy.Day{
		{Date: "2026-10-02", Wh: 4500, DurationS: 86400, Samples: 8640, MaxW: 402, MinW: 35},
		{Date: "2026-10-01", Wh: 2400, DurationS: 72000, Samples: 100, MaxW: 312, MinW: 40},
	}
	if err := Write(&b, days, 0.25); err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := strings.Join([]string{
		"date,kwh,cost,avg_w,peak_w,min_w,samples",
		"2026-10-02,4.500,1.125,188,402,35,8640",
		"2026-10-01,2.400,0.600,120,312,40,100",
		"",
	}, "\n")
	if b.String() != want {
		t.Errorf("CSV mismatch:\ngot:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestWriteEmptyIsHeaderOnly(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, nil, 0.25); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if b.String() != strings.Join(Header, ",")+"\n" {
		t.Errorf("empty export should carry only the header, got %q", b.String())
	}
}
