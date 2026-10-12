// Package report renders the daily summaries for export. It is shared by the
// CLI (`powcon-plus report --csv`) and by the window's Report button, so both
// produce the same columns in the same order.
package report

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/vincalkr/powcon-plus/internal/energy"
)

// Header is the CSV column list, in the order Rows writes them.
var Header = []string{"date", "kwh", "cost", "avg_w", "peak_w", "min_w", "samples"}

// MostRecent returns the n most recent days, newest first. With n <= 0 every
// day is kept. The input order does not matter: the result is always sorted
// from the newest date, which is also what the window's table shows.
func MostRecent(days []energy.Day, n int) []energy.Day {
	out := make([]energy.Day, len(days))
	copy(out, days)
	sort.Slice(out, func(i, j int) bool { return out[i].Date > out[j].Date })
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// MergeNewer overlays the live values over base, matched by date: base is the
// history on disk, live the days carried by the current snapshot (whose
// today's entry is more up to date than the one flushed to disk). Days only
// present in live are appended. base is not modified.
func MergeNewer(base, live []energy.Day) []energy.Day {
	out := make([]energy.Day, len(base))
	copy(out, base)
	at := make(map[string]int, len(out))
	for i, d := range out {
		at[d.Date] = i
	}
	for _, d := range live {
		if i, ok := at[d.Date]; ok {
			out[i] = d
			continue
		}
		at[d.Date] = len(out)
		out = append(out, d)
	}
	return out
}

// Rows renders the days as CSV cells: one line per day, preceded by Header.
// Energy in kWh, cost with the given tariff per kWh, powers in watts.
func Rows(days []energy.Day, price float64) [][]string {
	rows := make([][]string, 0, len(days)+1)
	rows = append(rows, Header)
	for _, d := range days {
		rows = append(rows, []string{
			d.Date,
			fmt.Sprintf("%.3f", d.Wh/1000),
			fmt.Sprintf("%.3f", d.Cost(price)),
			fmt.Sprintf("%.0f", d.AvgW()),
			fmt.Sprintf("%.0f", d.MaxW),
			fmt.Sprintf("%.0f", d.MinW),
			strconv.Itoa(d.Samples),
		})
	}
	return rows
}

// Write writes the days as CSV to w.
func Write(w io.Writer, days []energy.Day, price float64) error {
	cw := csv.NewWriter(w)
	return cw.WriteAll(Rows(days, price))
}
