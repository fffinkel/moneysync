package sheets

import (
	"fmt"
	"strings"
	"time"
)

// ColumnIndex converts "A" -> 0, "Z" -> 25, "AA" -> 26.
func ColumnIndex(col string) (int, error) {
	col = strings.ToUpper(strings.TrimSpace(col))
	if col == "" {
		return 0, fmt.Errorf("empty column")
	}
	n := 0
	for _, r := range col {
		if r < 'A' || r > 'Z' {
			return 0, fmt.Errorf("bad column %q", col)
		}
		n = n*26 + int(r-'A'+1)
	}
	return n - 1, nil
}

// ColumnLetter converts 0 -> "A", 26 -> "AA".
func ColumnLetter(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}

var sheetsEpoch = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)

// DateSerial returns the Sheets serial number for a calendar date.
func DateSerial(t time.Time) float64 {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return float64(d.Sub(sheetsEpoch) / (24 * time.Hour))
}

// SerialDate is the inverse of DateSerial (fractional days ignored).
func SerialDate(serial float64) time.Time {
	return sheetsEpoch.AddDate(0, 0, int(serial))
}

// Cell returns row r, column c of a values grid, or nil if absent.
func Cell(grid [][]any, r, c int) any {
	if r < 0 || r >= len(grid) || c < 0 || c >= len(grid[r]) {
		return nil
	}
	return grid[r][c]
}
