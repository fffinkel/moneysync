// Package config loads the moneysync configuration file.
//
// The file is JSON with comments (JSONC). Any string value that holds a
// secret can be written as "env:NAME" (read from an environment variable)
// or "file:/path" (read from a file), so the config itself never needs to
// contain credentials.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	SpreadsheetID string                  `json:"spreadsheet_id"`
	Timezone      string                  `json:"timezone"`
	Google        Google                  `json:"google"`
	FX            FX                      `json:"fx"`
	Sources       map[string]SourceConfig `json:"sources"`
	Targets       []Target                `json:"targets"`

	Location *time.Location `json:"-"`
}

// Google auth: either a service-account key file, or a ready-made OAuth
// access token (e.g. from `gcloud auth print-access-token`).
type Google struct {
	CredentialsFile string `json:"credentials_file"`
	AccessToken     string `json:"access_token"`
	BaseURL         string `json:"base_url"` // Sheets API root; only for testing
}

type FX struct {
	BaseURL string `json:"base_url"` // Frankfurter-compatible API
}

type SourceConfig struct {
	Type    string `json:"type"` // "monarch" or "toshl"
	Token   string `json:"token"`
	BaseURL string `json:"base_url"`
}

// Target describes one block of cells to fill: which source feeds it,
// which tab and row it lives on, and how transactions map to columns.
type Target struct {
	Name     string `json:"name"`
	Disabled bool   `json:"disabled"`
	Source   string `json:"source"`
	Sheet    string `json:"sheet"`

	// Row (1-based) holding the column headers. Columns are found by
	// header text, so inserting or moving columns in the sheet is safe.
	HeaderRow int `json:"header_row"`

	Row RowLocator `json:"row"`

	// If set, transactions in another currency are converted into this
	// one using the month's average rate.
	Currency string `json:"currency"`

	Include       *Match `json:"include"`
	Exclude       *Match `json:"exclude"`
	IncludeHidden bool   `json:"include_hidden"`
	SkipPending   bool   `json:"skip_pending"`

	Columns []Column `json:"columns"`

	// Header of the column that collects anything no other column
	// matched. Empty means unmatched transactions are reported, not written.
	Fallback string `json:"fallback"`

	FX *FXCell `json:"fx"`
}

// RowLocator finds the row for a month: the row whose MonthColumn holds
// that month (a date or text like "October 2026"), plus Offset. When
// LabelColumn/Label are set the found row must carry that label — a
// guard against writing to the wrong row if the layout changes.
type RowLocator struct {
	MonthColumn string `json:"month_column"`
	Offset      int    `json:"offset"`
	LabelColumn string `json:"label_column"`
	Label       string `json:"label"`
}

type Column struct {
	Header string `json:"header"`
	Column string `json:"column"` // optional explicit letter; overrides header lookup
	Match  Match  `json:"match"`
	// Keep the source sign instead of flipping it. By default amounts are
	// negated so spending shows as positive (refunds/income as negative).
	KeepSign bool `json:"keep_sign"`
}

// Match selects transactions. Within a Match, any listed value matching
// is enough (OR). Comparisons are case-insensitive and accept * and ?
// wildcards, e.g. "Auto *". All adds conditions that must every one hold
// as well, e.g. "a transfer AND tagged Alice".
type Match struct {
	Categories          []string `json:"categories"`
	CategoryGroups      []string `json:"category_groups"`
	CategoryTypes       []string `json:"category_types"`
	Tags                []string `json:"tags"`
	Accounts            []string `json:"accounts"`
	Merchants           []string `json:"merchants"`
	DescriptionContains []string `json:"description_contains"`
	All                 []Match  `json:"all"`
}

// Empty reports whether the Match has no conditions at all.
func (m *Match) Empty() bool { return m == nil || (m.AnyEmpty() && len(m.All) == 0) }

// AnyEmpty reports whether the OR part of the Match has no conditions.
func (m *Match) AnyEmpty() bool {
	return m == nil || (len(m.Categories) == 0 && len(m.CategoryGroups) == 0 &&
		len(m.CategoryTypes) == 0 && len(m.Tags) == 0 && len(m.Accounts) == 0 &&
		len(m.Merchants) == 0 && len(m.DescriptionContains) == 0)
}

// FXCell writes a monthly exchange rate into a cell.
type FXCell struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Header string `json:"header"`
	Column string `json:"column"`
	Offset int    `json:"offset"` // rows below the month row
}

// Load reads, parses, resolves secrets and validates a config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

func Parse(raw []byte) (*Config, error) {
	var c Config
	dec := json.NewDecoder(bytes.NewReader(StripJSONC(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.resolve(); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) resolve() error {
	var err error
	r := func(field string, v *string) {
		if err != nil {
			return
		}
		var e error
		*v, e = Resolve(*v)
		if e != nil {
			err = fmt.Errorf("%s: %w", field, e)
		}
	}
	r("spreadsheet_id", &c.SpreadsheetID)
	r("google.credentials_file", &c.Google.CredentialsFile)
	r("google.access_token", &c.Google.AccessToken)
	for name, s := range c.Sources {
		r("sources."+name+".token", &s.Token)
		c.Sources[name] = s
	}
	if err != nil {
		return err
	}
	if c.Timezone == "" {
		c.Location = time.Local
	} else if c.Location, err = time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("timezone: %w", err)
	}
	if c.FX.BaseURL == "" {
		c.FX.BaseURL = "https://api.frankfurter.dev/v1"
	}
	return nil
}

// Resolve expands "env:NAME" and "file:PATH" references.
func Resolve(v string) (string, error) {
	switch {
	case strings.HasPrefix(v, "env:"):
		name := strings.TrimPrefix(v, "env:")
		val, ok := os.LookupEnv(name)
		if !ok {
			// Missing is allowed here; whoever needs the value reports it.
			return "", nil
		}
		return strings.TrimSpace(val), nil
	case strings.HasPrefix(v, "file:"):
		b, err := os.ReadFile(strings.TrimPrefix(v, "file:"))
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return v, nil
}

func (c *Config) validate() error {
	names := map[string]bool{}
	for i, t := range c.Targets {
		where := fmt.Sprintf("targets[%d] (%s)", i, t.Name)
		if t.Name == "" {
			return fmt.Errorf("targets[%d]: name is required", i)
		}
		if names[t.Name] {
			return fmt.Errorf("%s: duplicate target name", where)
		}
		names[t.Name] = true
		if _, ok := c.Sources[t.Source]; !ok {
			return fmt.Errorf("%s: unknown source %q", where, t.Source)
		}
		if t.Sheet == "" || t.Row.MonthColumn == "" {
			return fmt.Errorf("%s: sheet and row.month_column are required", where)
		}
		if (t.Row.LabelColumn == "") != (t.Row.Label == "") {
			return fmt.Errorf("%s: row.label_column and row.label go together", where)
		}
		hasHeaders := false
		for j, col := range t.Columns {
			if col.Header == "" && col.Column == "" {
				return fmt.Errorf("%s: columns[%d] needs a header or column", where, j)
			}
			if col.Column == "" {
				hasHeaders = true
			}
		}
		if t.FX != nil && t.FX.Column == "" {
			hasHeaders = true
		}
		if (hasHeaders || t.Fallback != "") && t.HeaderRow < 1 {
			return fmt.Errorf("%s: header_row is required when columns are found by header", where)
		}
	}
	for name, s := range c.Sources {
		if s.Type != "monarch" && s.Type != "toshl" {
			return fmt.Errorf("sources.%s: unknown type %q", name, s.Type)
		}
	}
	return nil
}
