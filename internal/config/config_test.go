package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStripJSONC(t *testing.T) {
	in := `{
  // comment
  "url": "https://example.com/a//b", /* block
  comment */ "s": "quote \" // not a comment",
  "list": [1, 2, ],
}`
	var v map[string]any
	if err := json.Unmarshal(StripJSONC([]byte(in)), &v); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, StripJSONC([]byte(in)))
	}
	if v["url"] != "https://example.com/a//b" {
		t.Errorf("url = %v", v["url"])
	}
	if v["s"] != `quote " // not a comment` {
		t.Errorf("s = %v", v["s"])
	}
	if l := v["list"].([]any); len(l) != 2 {
		t.Errorf("list = %v", l)
	}
}

const minimal = `{
  "spreadsheet_id": "env:TEST_SHEET_ID",
  "timezone": "Europe/Berlin",
  "sources": {"m": {"type": "monarch", "token": "env:TEST_TOKEN"}},
  "targets": [{
    "name": "t", "source": "m", "sheet": "S", "header_row": 1,
    "row": {"month_column": "A"},
    "columns": [{"header": "Food", "match": {"categories": ["Groceries"]}}]
  }]
}`

func TestParseResolvesEnv(t *testing.T) {
	t.Setenv("TEST_SHEET_ID", "sheet123")
	t.Setenv("TEST_TOKEN", " tok \n")
	c, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if c.SpreadsheetID != "sheet123" || c.Sources["m"].Token != "tok" {
		t.Errorf("not resolved: %q %q", c.SpreadsheetID, c.Sources["m"].Token)
	}
	if c.Location.String() != "Europe/Berlin" {
		t.Errorf("location %v", c.Location)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"unknown field":  strings.Replace(minimal, `"timezone"`, `"timezon"`, 1),
		"unknown source": strings.Replace(minimal, `"source": "m"`, `"source": "x"`, 1),
		"no header row":  strings.Replace(minimal, `"header_row": 1,`, ``, 1),
		"bad type":       strings.Replace(minimal, `"type": "monarch"`, `"type": "mint"`, 1),
	}
	for name, in := range cases {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
