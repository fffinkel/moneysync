// Package sheets is a minimal Google Sheets v4 values client.
package sheets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultBaseURL = "https://sheets.googleapis.com/v4"

// Value render options.
const (
	RenderFormula     = "FORMULA"           // formulas as text, e.g. "=SUM(A1:B1)"
	RenderUnformatted = "UNFORMATTED_VALUE" // numbers as numbers, dates as serials
)

type Client struct {
	SpreadsheetID string
	BaseURL       string
	Tokens        TokenSource
	HTTP          *http.Client
}

func New(spreadsheetID string, ts TokenSource, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{SpreadsheetID: spreadsheetID, BaseURL: DefaultBaseURL, Tokens: ts, HTTP: hc}
}

// A1 builds a range string with a safely quoted sheet name.
func A1(sheet, rng string) string {
	return "'" + strings.ReplaceAll(sheet, "'", "''") + "'!" + rng
}

// BatchGet reads several ranges. Each result is rows of cells; cells are
// float64, string or bool depending on content and render option.
func (c *Client) BatchGet(ctx context.Context, render string, ranges ...string) ([][][]any, error) {
	q := url.Values{}
	for _, r := range ranges {
		q.Add("ranges", r)
	}
	q.Set("valueRenderOption", render)
	q.Set("dateTimeRenderOption", "SERIAL_NUMBER")
	q.Set("majorDimension", "ROWS")
	var resp struct {
		ValueRanges []struct {
			Values [][]any `json:"values"`
		} `json:"valueRanges"`
	}
	path := "/spreadsheets/" + url.PathEscape(c.SpreadsheetID) + "/values:batchGet?" + q.Encode()
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	out := make([][][]any, len(resp.ValueRanges))
	for i, vr := range resp.ValueRanges {
		out[i] = vr.Values
	}
	return out, nil
}

// Update is one cell write.
type Update struct {
	Range string
	Value any
}

// BatchUpdate writes cells as if typed by a user (USER_ENTERED).
func (c *Client) BatchUpdate(ctx context.Context, updates []Update) error {
	if len(updates) == 0 {
		return nil
	}
	type vr struct {
		Range  string  `json:"range"`
		Values [][]any `json:"values"`
	}
	body := struct {
		ValueInputOption string `json:"valueInputOption"`
		Data             []vr   `json:"data"`
	}{ValueInputOption: "USER_ENTERED"}
	for _, u := range updates {
		body.Data = append(body.Data, vr{Range: u.Range, Values: [][]any{{u.Value}}})
	}
	path := "/spreadsheets/" + url.PathEscape(c.SpreadsheetID) + "/values:batchUpdate"
	return c.do(ctx, http.MethodPost, path, body, nil)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	tok, err := c.Tokens.Token(ctx)
	if err != nil {
		return err
	}
	var rd io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("sheets: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != http.StatusOK {
		hint := ""
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
			hint = " (is the spreadsheet shared with the service account as an editor?)"
		}
		return fmt.Errorf("sheets: HTTP %d%s: %s", resp.StatusCode, hint, raw)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
