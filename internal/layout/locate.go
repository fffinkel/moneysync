package layout

import (
	"fmt"
	"strings"
	"time"

	"github.com/fffinkel/moneysync/internal/config"
	"github.com/fffinkel/moneysync/internal/sheets"
)

// Columns holds resolved 0-based column indexes for a target.
type Columns struct {
	Data     []int // parallel to Target.Columns
	Fallback int   // -1 if none
	FX       int   // -1 if none
	Warnings []string
}

// ResolveColumns maps every configured column to a sheet column, by
// explicit letter or by finding its header text in the header row.
func ResolveColumns(t config.Target, headerRow []any) (Columns, error) {
	idx := map[string][]int{}
	for i, v := range headerRow {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			k := norm(s)
			idx[k] = append(idx[k], i)
		}
	}
	find := func(header, letter string) (int, error) {
		if letter != "" {
			c, err := sheets.ColumnIndex(letter)
			if err != nil {
				return 0, err
			}
			return c, nil
		}
		hits := idx[norm(header)]
		switch len(hits) {
		case 0:
			return 0, fmt.Errorf("header %q not found in row %d of %q", header, t.HeaderRow, t.Sheet)
		case 1:
			return hits[0], nil
		default:
			return 0, fmt.Errorf("header %q appears %d times in row %d of %q; set an explicit column", header, len(hits), t.HeaderRow, t.Sheet)
		}
	}
	out := Columns{Fallback: -1, FX: -1}
	seen := map[int]string{}
	claim := func(c int, who string) error {
		if prev, ok := seen[c]; ok {
			return fmt.Errorf("%q and %q both map to column %s", prev, who, sheets.ColumnLetter(c))
		}
		seen[c] = who
		return nil
	}
	for _, col := range t.Columns {
		c, err := find(col.Header, col.Column)
		if err != nil {
			return out, err
		}
		if col.Column != "" && col.Header != "" && len(headerRow) > 0 {
			if got, _ := sheets.Cell([][]any{headerRow}, 0, c).(string); norm(got) != norm(col.Header) {
				out.Warnings = append(out.Warnings, fmt.Sprintf("column %s header is %q, config expects %q", col.Column, got, col.Header))
			}
		}
		if err := claim(c, columnKey(col)); err != nil {
			return out, err
		}
		out.Data = append(out.Data, c)
	}
	if t.Fallback != "" {
		c, err := find(t.Fallback, "")
		if err != nil {
			return out, fmt.Errorf("fallback: %w", err)
		}
		// The fallback may also be a regular column; totals are then summed.
		out.Fallback = c
	}
	if t.FX != nil {
		c, err := find(t.FX.Header, t.FX.Column)
		if err != nil {
			return out, fmt.Errorf("fx: %w", err)
		}
		if err := claim(c, "fx rate"); err != nil {
			return out, err
		}
		out.FX = c
	}
	return out, nil
}

func norm(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

var monthLayouts = []string{"January 2006", "Jan 2006", "2006-01", "1/2006", "01/2006", "1/2/2006", "2006-01-02"}

// CellMonth interprets a cell as a month, if it looks like one.
func CellMonth(v any) (time.Time, bool) {
	switch x := v.(type) {
	case float64:
		if x > 0 {
			d := sheets.SerialDate(x)
			return time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC), true
		}
	case string:
		s := strings.TrimSpace(x)
		for _, l := range monthLayouts {
			if d, err := time.Parse(l, s); err == nil {
				return time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC), true
			}
		}
	}
	return time.Time{}, false
}

// FindMonthRow returns the 1-based row whose cell (in a single-column
// grid starting at row 1) holds month m.
func FindMonthRow(column [][]any, m time.Time) (int, error) {
	for i, row := range column {
		if len(row) == 0 {
			continue
		}
		if d, ok := CellMonth(row[0]); ok && d.Year() == m.Year() && d.Month() == m.Month() {
			return i + 1, nil
		}
	}
	return 0, fmt.Errorf("no row for %s", m.Format("January 2006"))
}
