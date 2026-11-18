package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fffinkel/moneysync/internal/config"
	"github.com/fffinkel/moneysync/internal/sheets"
)

// fakeSheet is a tiny in-memory Google Sheets values API.
type fakeSheet struct {
	mu     sync.Mutex
	tabs   map[string][][]any
	writes map[string]any
}

var a1re = regexp.MustCompile(`^'(.+)'!([A-Z]*)(\d*):?([A-Z]*)(\d*)$`)

func (f *fakeSheet) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "values:batchGet"):
		var out struct {
			ValueRanges []map[string]any `json:"valueRanges"`
		}
		for _, rng := range r.URL.Query()["ranges"] {
			m := a1re.FindStringSubmatch(rng)
			if m == nil {
				http.Error(w, "bad range "+rng, 400)
				return
			}
			grid := f.tabs[m[1]]
			var vals [][]any
			switch {
			case m[3] != "" && m[2] == "": // rows "N:N"
				n, _ := strconv.Atoi(m[3])
				if n-1 < len(grid) {
					vals = [][]any{grid[n-1]}
				}
			case m[3] == "": // column "A:A"
				c, _ := sheets.ColumnIndex(m[2])
				for _, row := range grid {
					if c < len(row) {
						vals = append(vals, []any{row[c]})
					} else {
						vals = append(vals, []any{})
					}
				}
			}
			out.ValueRanges = append(out.ValueRanges, map[string]any{"values": vals})
		}
		json.NewEncoder(w).Encode(out)
	case strings.HasSuffix(r.URL.Path, "values:batchUpdate"):
		var in struct {
			Data []struct {
				Range  string  `json:"range"`
				Values [][]any `json:"values"`
			} `json:"data"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		for _, d := range in.Data {
			f.writes[d.Range] = d.Values[0][0]
		}
		w.Write([]byte(`{}`))
	default:
		http.NotFound(w, r)
	}
}

func serial(y int, m time.Month) float64 {
	return sheets.DateSerial(time.Date(y, m, 1, 0, 0, 0, 0, time.UTC))
}

func newFakeSheet() *fakeSheet {
	header := []any{"", "", "RATE", "Rent", "Food", "Misc", "Transfers", "Total"}
	return &fakeSheet{
		writes: map[string]any{},
		tabs: map[string][][]any{
			"Me": {
				{"https://rates.example"},
				header,
				{serial(2026, 9), "USD", "", 0.0, 1.0, 2.0, "", "=SUM(D3:F3)"},
				{"", "EUR", "0.86", "=D3*C4", "=E3*C4"},
				{"", "PT", "", 1.0},
				{"", "TOTAL"},
				{serial(2026, 10), "USD", "", 0.0, "", "", "", "=SUM(D7:F7)"},
				{"", "EUR", "0.8691", "=D7*C8", "=E7*C8"},
				{"", "PT"},
				{"", "TOTAL"},
			},
			"Household": {
				{},
				{"", "Rent", "Food", "unsorted", "Total"},
				{serial(2026, 9), 1000.0, 50.0, "", "=SUM(B3:D3)"},
				{serial(2026, 10), "", "", "", "=SUM(B4:D4)"},
			},
		},
	}
}

func fakeMonarch(t *testing.T) *httptest.Server {
	type res = map[string]any
	all := []res{
		{"id": "1", "amount": -40.25, "date": "2026-10-03", "category": res{"name": "Groceries", "group": res{"name": "Food & Dining", "type": "expense"}}, "account": res{"displayName": "Checking"}, "tags": []any{}},
		{"id": "2", "amount": -15, "date": "2026-10-04", "category": res{"name": "Shopping", "group": res{"name": "Shopping", "type": "expense"}}, "tags": []any{}},
		{"id": "3", "amount": -500, "date": "2026-10-05", "category": res{"name": "Transfer", "group": res{"name": "Transfers", "type": "transfer"}}, "tags": []any{}},
		{"id": "4", "amount": -9, "date": "2026-10-06", "hideFromReports": true, "category": res{"name": "Groceries", "group": res{"name": "Food & Dining", "type": "expense"}}, "tags": []any{}},
		{"id": "5", "amount": 3000, "date": "2026-10-07", "category": res{"name": "Paychecks", "group": res{"name": "Income", "type": "income"}}, "tags": []any{}},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token mtok" {
			w.WriteHeader(401)
			return
		}
		var req struct {
			Variables struct {
				Offset, Limit int
				Filters       struct{ StartDate, EndDate string }
			}
		}
		json.NewDecoder(r.Body).Decode(&req)
		var page []res
		if req.Variables.Filters.StartDate == "2026-10-01" && req.Variables.Offset == 0 {
			page = all
		}
		json.NewEncoder(w).Encode(res{"data": res{"allTransactions": res{"totalCount": len(page), "results": page}}})
	}))
}

func fakeToshl(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ttok" {
			w.WriteHeader(401)
			return
		}
		page := r.URL.Query().Get("page")
		switch r.URL.Path {
		case "/categories":
			fmt.Fprint(w, `[{"id":"c1","name":"Food & Drinks","type":"expense"},{"id":"c2","name":"Rent","type":"expense"},{"id":"c3","name":"Leisure","type":"expense"}]`)
		case "/tags":
			fmt.Fprint(w, `[{"id":"t1","name":"shared"}]`)
		case "/accounts":
			fmt.Fprint(w, `[{"id":"a1","name":"Joint"},{"id":"a2","name":"Mine"}]`)
		case "/entries":
			if r.URL.Query().Get("from") != "2026-10-01" {
				fmt.Fprint(w, `[]`)
				return
			}
			// two pages, linked
			if page == "0" {
				w.Header().Set("Link", `</entries?page=1>; rel="next"`)
				fmt.Fprint(w, `[{"id":"e1","amount":-1200,"currency":{"code":"EUR"},"date":"2026-10-01","account":"a1","category":"c2","tags":[]},
				               {"id":"e2","amount":-30.5,"currency":{"code":"EUR"},"date":"2026-10-02","account":"a1","category":"c1","tags":["t1"]}]`)
				return
			}
			fmt.Fprint(w, `[{"id":"e3","amount":-10,"currency":{"code":"USD"},"date":"2026-10-03","account":"a1","category":"c3","tags":[]},
			               {"id":"e4","amount":-99,"currency":{"code":"EUR"},"date":"2026-10-03","account":"a2","category":"c1","tags":[]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func fakeFX() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"rates":{"2026-10-01":{"EUR":0.86},"2026-10-02":{"EUR":0.88}}}`)
	}))
}

func testConfig(sheetURL, monarchURL, toshlURL, fxURL string) string {
	return fmt.Sprintf(`{
  "spreadsheet_id": "test",
  "timezone": "UTC",
  "google": {"access_token": "gtok", "base_url": %q},
  "fx": {"base_url": %q},
  "sources": {
    "monarch": {"type": "monarch", "token": "mtok", "base_url": %q},
    "toshl":   {"type": "toshl",   "token": "ttok", "base_url": %q},
  },
  "targets": [
    {
      "name": "me", "source": "monarch", "sheet": "Me", "header_row": 2,
      "row": {"month_column": "A", "offset": 0, "label_column": "B", "label": "USD"},
      "exclude": {"categories": ["Paychecks"]},
      "columns": [
        {"header": "Rent", "match": {"categories": ["Rent"]}},
        {"header": "Food", "match": {"category_groups": ["Food & Dining"]}},
        {"header": "Transfers", "match": {"category_types": ["transfer"]}},
        {"header": "Total", "match": {"categories": ["never"]}},
      ],
      "fallback": "Misc",
      "fx": {"from": "USD", "to": "EUR", "header": "RATE", "offset": 1},
    },
    {
      "name": "household", "source": "toshl", "sheet": "Household", "header_row": 2,
      "row": {"month_column": "A"}, "currency": "EUR",
      "include": {"accounts": ["Joint"]},
      "columns": [
        {"header": "Rent", "match": {"categories": ["Rent"]}},
        {"header": "Food", "match": {"categories": ["Food*"]}},
      ],
      "fallback": "unsorted",
    },
  ],
}`, sheetURL, fxURL, monarchURL, toshlURL)
}

func TestEndToEnd(t *testing.T) {
	fs := newFakeSheet()
	ss := httptest.NewServer(fs)
	defer ss.Close()
	ms, ts, fx := fakeMonarch(t), fakeToshl(t), fakeFX()
	defer ms.Close()
	defer ts.Close()
	defer fx.Close()

	cfg, err := config.Parse([]byte(testConfig(ss.URL, ms.URL, ts.URL, fx.URL)))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a := New(cfg, &out)
	a.FX.Now = func() time.Time { return time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC) }
	targets, _ := a.Selected(nil)
	oct := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	plan, err := a.BuildPlan(t.Context(), targets, []time.Time{oct})
	if err != nil {
		t.Fatal(err)
	}
	plan.Print(&out)
	for _, s := range plan.Sections {
		if s.Err != nil {
			t.Fatalf("%s: %v\n%s", s.Target, s.Err, out.String())
		}
	}
	if _, err := a.Apply(t.Context(), plan); err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"'Me'!E7":        40.25,  // food, hidden one ignored
		"'Me'!F7":        15.0,   // shopping → Misc fallback
		"'Me'!G7":        500.0,  // transfer
		"'Me'!C8":        0.87,   // average of 0.86 and 0.88
		"'Household'!B4": 1200.0, // rent
		"'Household'!C4": 30.5,   // food (account a2 entry excluded)
		"'Household'!D4": 8.7,    // 10 USD at 0.87 → unsorted
	}
	for k, v := range want {
		if fs.writes[k] != v {
			t.Errorf("%s = %v, want %v", k, fs.writes[k], v)
		}
	}
	// Rent D7 was already 0 → unchanged, so not written; Total H7 is a formula.
	for _, k := range []string{"'Me'!D7", "'Me'!H7"} {
		if _, ok := fs.writes[k]; ok {
			t.Errorf("%s should not be written", k)
		}
	}
	if !strings.Contains(out.String(), "skipped: cell has a formula") {
		t.Errorf("expected formula skip in output:\n%s", out.String())
	}
	if len(fs.writes) != len(want) {
		t.Errorf("unexpected writes: %v", fs.writes)
	}
}

func TestLabelGuard(t *testing.T) {
	fs := newFakeSheet()
	ss := httptest.NewServer(fs)
	defer ss.Close()
	ms := fakeMonarch(t)
	defer ms.Close()
	raw := strings.Replace(testConfig(ss.URL, ms.URL, "http://unused", "http://unused"), `"label": "USD"`, `"label": "PT"`, 1)
	raw = strings.Replace(raw, `"fx": {"from": "USD", "to": "EUR", "header": "RATE", "offset": 1},`, "", 1)
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	a := New(cfg, &bytes.Buffer{})
	targets, _ := a.Selected([]string{"me"})
	plan, err := a.BuildPlan(t.Context(), targets, []time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Sections[0].Err == nil || !strings.Contains(plan.Sections[0].Err.Error(), "expected label") {
		t.Errorf("want label error, got %v", plan.Sections[0].Err)
	}
	if len(plan.Writes()) != 0 {
		t.Error("no writes expected")
	}
}

func TestMonths(t *testing.T) {
	now := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	ms, _ := Months(nil, time.UTC, now)
	if ms[0].Format("2006-01") != "2025-12" || ms[1].Format("2006-01") != "2026-01" {
		t.Errorf("default months %v", ms)
	}
	ms, err := Months([]string{"2026-01..2026-03,2026-06"}, time.UTC, now)
	if err != nil || len(ms) != 4 || ms[3].Month() != time.June {
		t.Errorf("range: %v %v", ms, err)
	}
}
