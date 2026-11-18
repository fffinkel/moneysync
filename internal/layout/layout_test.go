package layout

import (
	"testing"
	"time"

	"github.com/fffinkel/moneysync/internal/config"
	"github.com/fffinkel/moneysync/internal/model"
	"github.com/fffinkel/moneysync/internal/sheets"
)

func target() config.Target {
	return config.Target{
		Name: "t", Sheet: "S", HeaderRow: 2,
		Exclude: &config.Match{Categories: []string{"Paychecks"}},
		Columns: []config.Column{
			{Header: "Fun", Match: config.Match{Tags: []string{"fun"}}},
			{Header: "Food", Match: config.Match{CategoryGroups: []string{"Food & Dining"}}},
			{Header: "Transport", Match: config.Match{Categories: []string{"Auto *", "Taxi*"}}},
			{Header: "Transfers", Match: config.Match{CategoryTypes: []string{"transfer"}}},
		},
		Fallback: "Misc",
	}
}

func tx(cat, group string, amt float64, tags ...string) model.Transaction {
	return model.Transaction{Category: cat, CategoryGroup: group, Amount: amt, Tags: tags}
}

func TestAggregate(t *testing.T) {
	txs := []model.Transaction{
		tx("Groceries", "Food & Dining", -50.10),
		tx("Restaurants", "Food & Dining", -20, "FUN"), // tag column wins (listed first)
		tx("Groceries", "Food & Dining", 5.10),         // refund
		tx("Auto Payment", "Auto & Transport", -300),
		tx("Taxi & Ride Shares", "Auto & Transport", -12.5),
		tx("Paychecks", "Income", 4000),
		tx("Shopping", "Shopping", -99.99),
		{Category: "Transfer", CategoryType: "transfer", Amount: -1000},
		{Category: "Groceries", CategoryGroup: "Food & Dining", Amount: -7, Hidden: true},
	}
	r, err := Aggregate(target(), txs, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{20, 45, 312.5, 1000}
	for i, w := range want {
		if r.Totals[i] != w {
			t.Errorf("column %d = %v, want %v", i, r.Totals[i], w)
		}
	}
	if r.Fallback != 99.99 {
		t.Errorf("fallback = %v", r.Fallback)
	}
	if r.Counts.Hidden != 1 || r.Counts.Excluded != 1 || r.Counts.Used != 7 {
		t.Errorf("counts = %+v", r.Counts)
	}
}

func TestAggregateNoFallbackReportsUnmatched(t *testing.T) {
	tg := target()
	tg.Fallback = ""
	r, _ := Aggregate(tg, []model.Transaction{tx("Shopping", "", -10)}, nil)
	if r.Unmatched["Shopping"] != 10 || r.Counts.Unmatched != 1 || r.Counts.Used != 0 {
		t.Errorf("unmatched = %v counts=%+v", r.Unmatched, r.Counts)
	}
}

func TestKeepSignAndInclude(t *testing.T) {
	tg := config.Target{
		Include: &config.Match{Accounts: []string{"Joint*"}},
		Columns: []config.Column{{Header: "In", KeepSign: true, Match: config.Match{CategoryTypes: []string{"income"}}}},
	}
	txs := []model.Transaction{
		{Account: "Joint EUR", CategoryType: "income", Amount: 100},
		{Account: "Personal", CategoryType: "income", Amount: 999},
	}
	r, _ := Aggregate(tg, txs, nil)
	if r.Totals[0] != 100 || r.Counts.Excluded != 1 {
		t.Errorf("totals=%v counts=%+v", r.Totals, r.Counts)
	}
}

func TestResolveColumns(t *testing.T) {
	header := []any{"", "", "RATE", "Rent", "Food", " Misc ", "Transfers"}
	tg := target()
	tg.Columns = tg.Columns[1:2] // Food
	tg.Columns = append(tg.Columns, config.Column{Header: "Rent", Column: "D"})
	tg.FX = &config.FXCell{Header: "rate"}
	c, err := ResolveColumns(tg, header)
	if err != nil {
		t.Fatal(err)
	}
	if c.Data[0] != 4 || c.Data[1] != 3 || c.Fallback != 5 || c.FX != 2 {
		t.Errorf("got %+v", c)
	}
	tg.Columns = append(tg.Columns, config.Column{Header: "Nope"})
	if _, err := ResolveColumns(tg, header); err == nil {
		t.Error("expected missing-header error")
	}
}

func TestFindMonthRow(t *testing.T) {
	oct := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	col := [][]any{
		{"https://example"}, {}, {sheets.DateSerial(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))},
		{}, {}, {}, {sheets.DateSerial(oct)},
	}
	if r, err := FindMonthRow(col, oct); err != nil || r != 7 {
		t.Errorf("row=%d err=%v", r, err)
	}
	if r, err := FindMonthRow([][]any{{"x"}, {"October 2026"}}, oct); err != nil || r != 2 {
		t.Errorf("text month: row=%d err=%v", r, err)
	}
	if _, err := FindMonthRow(col, oct.AddDate(0, 1, 0)); err == nil {
		t.Error("expected not found")
	}
}

func TestColumnLetters(t *testing.T) {
	for i, s := range map[int]string{0: "A", 25: "Z", 26: "AA", 51: "AZ", 702: "AAA"} {
		if got := sheets.ColumnLetter(i); got != s {
			t.Errorf("ColumnLetter(%d)=%s want %s", i, got, s)
		}
		if got, _ := sheets.ColumnIndex(s); got != i {
			t.Errorf("ColumnIndex(%s)=%d want %d", s, got, i)
		}
	}
}

func TestMatchAll(t *testing.T) {
	m := &config.Match{All: []config.Match{
		{CategoryTypes: []string{"transfer"}, Categories: []string{"Transfer*"}},
		{Tags: []string{"Alice"}},
	}}
	cases := []struct {
		tx   model.Transaction
		want bool
	}{
		{model.Transaction{CategoryType: "transfer", Tags: []string{"alice"}}, true},
		{model.Transaction{Category: "Transfers", Tags: []string{"Alice"}}, true},
		{model.Transaction{CategoryType: "transfer", Tags: []string{"Bob"}}, false},
		{model.Transaction{Category: "Food", Tags: []string{"Alice"}}, false},
	}
	for i, c := range cases {
		if got := Matches(m, c.tx); got != c.want {
			t.Errorf("case %d: got %v want %v", i, got, c.want)
		}
	}
	if Matches(&config.Match{All: []config.Match{{}}}, model.Transaction{Category: "x"}) {
		t.Error("an empty sub-match should not match")
	}
}
