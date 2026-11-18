package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fffinkel/moneysync/internal/config"
	"github.com/fffinkel/moneysync/internal/layout"
	"github.com/fffinkel/moneysync/internal/sheets"
)

// Change is one proposed cell write.
type Change struct {
	Target string
	Month  time.Time
	Range  string // A1 range with sheet name
	Cell   string // e.g. "E98"
	Header string
	Old    any
	New    float64
	Skip   string // non-empty: why this cell won't be written
	Places int    // decimal places compared when deciding if it changed
}

func (c Change) Changed() bool { return c.Skip == "" && !sameValue(c.Old, c.New, c.Places) }

// Section groups the changes for one target and month, plus notes.
type Section struct {
	Target  string
	Month   time.Time
	Where   string
	Changes []Change
	Notes   []string
	Err     error
}

type Plan struct{ Sections []Section }

// Writes returns every change that will actually be applied.
func (p *Plan) Writes() []Change {
	var out []Change
	for _, s := range p.Sections {
		for _, c := range s.Changes {
			if c.Changed() {
				out = append(out, c)
			}
		}
	}
	return out
}

// Selected returns enabled targets, optionally filtered by name.
func (a *App) Selected(names []string) ([]config.Target, error) {
	want := map[string]bool{}
	for _, n := range names {
		for _, p := range strings.Split(n, ",") {
			if p = strings.TrimSpace(p); p != "" {
				want[strings.ToLower(p)] = true
			}
		}
	}
	var out []config.Target
	for _, t := range a.Cfg.Targets {
		if len(want) > 0 {
			if want[strings.ToLower(t.Name)] {
				out = append(out, t)
				delete(want, strings.ToLower(t.Name))
			}
			continue
		}
		if !t.Disabled {
			out = append(out, t)
		}
	}
	for n := range want {
		return nil, fmt.Errorf("no target named %q", n)
	}
	return out, nil
}

// BuildPlan works out every cell to write, without writing anything.
// Errors in one target/month are recorded on its section so the others
// still run.
func (a *App) BuildPlan(ctx context.Context, targets []config.Target, months []time.Time) (*Plan, error) {
	if err := a.ConnectSheets(); err != nil {
		return nil, err
	}
	p := &Plan{}
	for _, t := range targets {
		for _, m := range months {
			s := a.planOne(ctx, t, m)
			p.Sections = append(p.Sections, s)
		}
	}
	return p, nil
}

func (a *App) planOne(ctx context.Context, t config.Target, month time.Time) (s Section) {
	s = Section{Target: t.Name, Month: month}
	fail := func(err error) Section { s.Err = err; return s }

	txs, err := a.Transactions(ctx, t.Source, month)
	if err != nil {
		return fail(err)
	}
	res, err := layout.Aggregate(t, txs, a.converter(ctx, t, month))
	if err != nil {
		return fail(err)
	}

	var header []any
	if t.HeaderRow > 0 {
		if header, err = a.row(ctx, t.Sheet, t.HeaderRow); err != nil {
			return fail(err)
		}
	}
	cols, err := layout.ResolveColumns(t, header)
	if err != nil {
		return fail(err)
	}
	s.Notes = append(s.Notes, cols.Warnings...)

	monthCol, err := a.grid(ctx, sheets.RenderUnformatted, sheets.A1(t.Sheet, t.Row.MonthColumn+":"+t.Row.MonthColumn))
	if err != nil {
		return fail(err)
	}
	monthRow, err := layout.FindMonthRow(monthCol, month)
	if err != nil {
		return fail(fmt.Errorf("%w in column %s of %q — add the month's row(s) first", err, t.Row.MonthColumn, t.Sheet))
	}
	rowNum := monthRow + t.Row.Offset
	row, err := a.row(ctx, t.Sheet, rowNum)
	if err != nil {
		return fail(err)
	}
	s.Where = fmt.Sprintf("%s row %d", t.Sheet, rowNum)
	if t.Row.Label != "" {
		lc, err := sheets.ColumnIndex(t.Row.LabelColumn)
		if err != nil {
			return fail(err)
		}
		got, _ := sheets.Cell([][]any{row}, 0, lc).(string)
		if norm(got) != norm(t.Row.Label) {
			return fail(fmt.Errorf("row %d of %q: expected label %q in column %s, found %q — layout changed?", rowNum, t.Sheet, t.Row.Label, t.Row.LabelColumn, got))
		}
		s.Where += fmt.Sprintf(" (%s)", t.Row.Label)
	}

	values := map[int]float64{}
	headers := map[int]string{}
	for i, c := range cols.Data {
		values[c] += res.Totals[i]
		headers[c] = columnName(t.Columns[i])
	}
	if cols.Fallback >= 0 {
		values[cols.Fallback] = layout.Round2(values[cols.Fallback] + res.Fallback)
		if _, ok := headers[cols.Fallback]; !ok {
			headers[cols.Fallback] = t.Fallback
		}
	}

	if res.Counts.Used == 0 {
		s.Notes = append(s.Notes, "no transactions for this month — leaving cells untouched")
	} else {
		for _, c := range sortedInts(values) {
			s.Changes = append(s.Changes, makeChange(t, month, rowNum, c, headers[c], sheets.Cell([][]any{row}, 0, c), values[c]))
		}
	}

	if t.FX != nil {
		from, to := t.FX.From, t.FX.To
		rate, err := a.FX.MonthlyAverage(ctx, from, to, month)
		if err != nil {
			s.Notes = append(s.Notes, "fx rate skipped: "+err.Error())
		} else {
			fxRowNum := monthRow + t.FX.Offset
			fxRow, err := a.row(ctx, t.Sheet, fxRowNum)
			if err != nil {
				return fail(err)
			}
			ch := makeChange(t, month, fxRowNum, cols.FX, from+"→"+to, sheets.Cell([][]any{fxRow}, 0, cols.FX), rate)
			ch.Places = 4
			s.Changes = append(s.Changes, ch)
		}
	}

	c := res.Counts
	s.Notes = append(s.Notes, fmt.Sprintf("%d transactions used, %d filtered out, %d hidden, %d unmatched, %d converted",
		c.Used, c.Excluded+c.Pending, c.Hidden, c.Unmatched, c.Converted))
	if len(res.Unmatched) > 0 {
		var parts []string
		for _, k := range layout.SortedKeys(res.Unmatched) {
			parts = append(parts, fmt.Sprintf("%s %.2f", k, res.Unmatched[k]))
		}
		s.Notes = append(s.Notes, "NOT WRITTEN (no matching column): "+strings.Join(parts, ", "))
	}
	if fb := res.Breakdown[layout.FallbackKey]; len(fb) > 0 {
		var parts []string
		for _, k := range layout.SortedKeys(fb) {
			parts = append(parts, fmt.Sprintf("%s %.2f", k, fb[k]))
		}
		s.Notes = append(s.Notes, fmt.Sprintf("sent to %q: %s", t.Fallback, strings.Join(parts, ", ")))
	}
	return s
}

func makeChange(t config.Target, month time.Time, rowNum, col int, header string, old any, v float64) Change {
	cell := fmt.Sprintf("%s%d", sheets.ColumnLetter(col), rowNum)
	ch := Change{Target: t.Name, Month: month, Range: sheets.A1(t.Sheet, cell), Cell: cell, Header: header, Old: old, New: v, Places: 2}
	if s, ok := old.(string); ok && strings.HasPrefix(s, "=") {
		ch.Skip = "cell has a formula"
	}
	return ch
}

func columnName(c config.Column) string {
	if c.Header != "" {
		return c.Header
	}
	return c.Column
}

func norm(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func sortedInts(m map[int]float64) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// Apply writes the plan's changes in one batch.
func (a *App) Apply(ctx context.Context, p *Plan) (int, error) {
	if err := a.ConnectSheets(); err != nil {
		return 0, err
	}
	var ups []sheets.Update
	for _, c := range p.Writes() {
		ups = append(ups, sheets.Update{Range: c.Range, Value: c.New})
	}
	return len(ups), a.Sheets.BatchUpdate(ctx, ups)
}

// Print renders a plan for humans.
func (p *Plan) Print(w interface{ Write([]byte) (int, error) }) {
	for _, s := range p.Sections {
		fmt.Fprintf(w, "\n== %s — %s", s.Target, s.Month.Format("January 2006"))
		if s.Where != "" {
			fmt.Fprintf(w, " → %s", s.Where)
		}
		fmt.Fprintln(w)
		if s.Err != nil {
			fmt.Fprintf(w, "   ERROR: %v\n", s.Err)
			continue
		}
		for _, c := range s.Changes {
			mark := "  "
			switch {
			case c.Skip != "":
				mark = "--"
			case c.Changed():
				mark = "* "
			}
			line := fmt.Sprintf("   %s %-6s %-12s %12s → %10.4g", mark, c.Cell, c.Header, fmtCell(c.Old), c.New)
			if !strings.Contains(c.Header, "→") {
				line = fmt.Sprintf("   %s %-6s %-12s %12s → %10.2f", mark, c.Cell, c.Header, fmtCell(c.Old), c.New)
			}
			if c.Skip != "" {
				line += "   (skipped: " + c.Skip + ")"
			}
			fmt.Fprintln(w, line)
		}
		for _, n := range s.Notes {
			fmt.Fprintf(w, "   · %s\n", n)
		}
	}
}
