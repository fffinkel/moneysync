// Package app wires sources, layout rules and the spreadsheet together.
package app

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fffinkel/moneysync/internal/config"
	"github.com/fffinkel/moneysync/internal/fx"
	"github.com/fffinkel/moneysync/internal/layout"
	"github.com/fffinkel/moneysync/internal/model"
	"github.com/fffinkel/moneysync/internal/monarch"
	"github.com/fffinkel/moneysync/internal/sheets"
	"github.com/fffinkel/moneysync/internal/toshl"
)

type App struct {
	Cfg    *config.Config
	HTTP   *http.Client
	Sheets *sheets.Client
	FX     *fx.Provider
	Out    io.Writer

	sources map[string]model.Source
	txCache map[string][]model.Transaction
	grids   map[string][][]any
}

func New(cfg *config.Config, out io.Writer) *App {
	hc := &http.Client{Timeout: 60 * time.Second}
	return &App{
		Cfg:     cfg,
		HTTP:    hc,
		FX:      fx.New(cfg.FX.BaseURL, hc),
		Out:     out,
		sources: map[string]model.Source{},
		txCache: map[string][]model.Transaction{},
		grids:   map[string][][]any{},
	}
}

// ConnectSheets sets up the Google Sheets client from config.
func (a *App) ConnectSheets() error {
	if a.Sheets != nil {
		return nil
	}
	if a.Cfg.SpreadsheetID == "" {
		return fmt.Errorf("spreadsheet_id is empty (check the env var it points to)")
	}
	var ts sheets.TokenSource
	switch {
	case a.Cfg.Google.AccessToken != "":
		ts = sheets.StaticToken(a.Cfg.Google.AccessToken)
	case a.Cfg.Google.CredentialsFile != "":
		sa, err := sheets.LoadServiceAccount(a.Cfg.Google.CredentialsFile, a.HTTP)
		if err != nil {
			return err
		}
		ts = sa
	default:
		return fmt.Errorf("google: set credentials_file (service account) or access_token")
	}
	a.Sheets = sheets.New(a.Cfg.SpreadsheetID, ts, a.HTTP)
	if a.Cfg.Google.BaseURL != "" {
		a.Sheets.BaseURL = a.Cfg.Google.BaseURL
	}
	return nil
}

// Source returns (building on first use) the named source.
func (a *App) Source(name string) (model.Source, error) {
	if s, ok := a.sources[name]; ok {
		return s, nil
	}
	sc, ok := a.Cfg.Sources[name]
	if !ok {
		return nil, fmt.Errorf("unknown source %q", name)
	}
	var (
		s   model.Source
		err error
	)
	switch sc.Type {
	case "monarch":
		s, err = monarch.New(name, sc.Token, sc.BaseURL, a.HTTP)
	case "toshl":
		s, err = toshl.New(name, sc.Token, sc.BaseURL, a.HTTP)
	default:
		err = fmt.Errorf("source %q: unknown type %q", name, sc.Type)
	}
	if err != nil {
		return nil, err
	}
	a.sources[name] = s
	return s, nil
}

// Transactions returns a source's transactions for one calendar month.
func (a *App) Transactions(ctx context.Context, source string, month time.Time) ([]model.Transaction, error) {
	key := source + "|" + month.Format("2006-01")
	if txs, ok := a.txCache[key]; ok {
		return txs, nil
	}
	s, err := a.Source(source)
	if err != nil {
		return nil, err
	}
	from := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	txs, err := s.Transactions(ctx, from, from.AddDate(0, 1, -1))
	if err != nil {
		return nil, err
	}
	a.txCache[key] = txs
	return txs, nil
}

func (a *App) converter(ctx context.Context, t config.Target, month time.Time) layout.Converter {
	if t.Currency == "" {
		return nil
	}
	want := strings.ToUpper(t.Currency)
	return func(tx model.Transaction) (float64, error) {
		if tx.Currency == "" || strings.EqualFold(tx.Currency, want) {
			return tx.Amount, nil
		}
		rate, err := a.FX.MonthlyAverage(ctx, tx.Currency, want, month)
		if err != nil {
			return 0, fmt.Errorf("convert %s→%s: %w", tx.Currency, want, err)
		}
		return tx.Amount * rate, nil
	}
}

// grid reads a range once per run (render option is part of the key).
func (a *App) grid(ctx context.Context, render, rng string) ([][]any, error) {
	key := render + "|" + rng
	if g, ok := a.grids[key]; ok {
		return g, nil
	}
	res, err := a.Sheets.BatchGet(ctx, render, rng)
	if err != nil {
		return nil, err
	}
	var g [][]any
	if len(res) > 0 {
		g = res[0]
	}
	a.grids[key] = g
	return g, nil
}

func (a *App) row(ctx context.Context, sheet string, row int) ([]any, error) {
	g, err := a.grid(ctx, sheets.RenderFormula, sheets.A1(sheet, fmt.Sprintf("%d:%d", row, row)))
	if err != nil || len(g) == 0 {
		return nil, err
	}
	return g[0], nil
}

// Months parses "YYYY-MM" values; with none, it returns last month and
// this month in the configured time zone.
func Months(args []string, loc *time.Location, now time.Time) ([]time.Time, error) {
	if len(args) == 0 {
		n := now.In(loc)
		cur := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, time.UTC)
		return []time.Time{cur.AddDate(0, -1, 0), cur}, nil
	}
	var out []time.Time
	for _, s := range args {
		for _, part := range strings.Split(s, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if strings.Contains(part, "..") {
				ends := strings.SplitN(part, "..", 2)
				a, err := time.Parse("2006-01", ends[0])
				if err != nil {
					return nil, fmt.Errorf("bad month %q (want YYYY-MM)", ends[0])
				}
				b, err := time.Parse("2006-01", ends[1])
				if err != nil {
					return nil, fmt.Errorf("bad month %q (want YYYY-MM)", ends[1])
				}
				for m := a; !m.After(b); m = m.AddDate(0, 1, 0) {
					out = append(out, m)
				}
				continue
			}
			m, err := time.Parse("2006-01", part)
			if err != nil {
				return nil, fmt.Errorf("bad month %q (want YYYY-MM)", part)
			}
			out = append(out, m)
		}
	}
	return out, nil
}

// sameValue reports whether an existing cell already holds v, compared
// at the given number of decimal places. Numeric text ("0.8691") counts.
func sameValue(old any, v float64, places int) bool {
	var f float64
	switch x := old.(type) {
	case float64:
		f = x
	case string:
		var err error
		if f, err = strconv.ParseFloat(strings.TrimSpace(x), 64); err != nil {
			return false
		}
	default:
		return false
	}
	p := math.Pow(10, float64(places))
	return math.Round(f*p) == math.Round(v*p)
}

func fmtCell(v any) string {
	switch x := v.(type) {
	case nil:
		return "(empty)"
	case float64:
		return fmt.Sprintf("%.2f", x)
	case string:
		if x == "" {
			return "(empty)"
		}
		return fmt.Sprintf("%q", x)
	default:
		return fmt.Sprint(x)
	}
}
