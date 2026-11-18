// Package fx fetches monthly average exchange rates from a
// Frankfurter-compatible API (ECB reference rates, no key needed).
package fx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Provider struct {
	BaseURL string
	HTTP    *http.Client
	Now     func() time.Time

	mu    sync.Mutex
	cache map[string]float64
}

func New(baseURL string, hc *http.Client) *Provider {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Provider{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: hc, Now: time.Now, cache: map[string]float64{}}
}

// MonthlyAverage returns the mean daily rate (1 unit of from in to) over
// the month containing m, up to today for the current month. Rounded to
// 4 decimals, matching how rates are usually typed into a sheet.
func (p *Provider) MonthlyAverage(ctx context.Context, from, to string, m time.Time) (float64, error) {
	from, to = strings.ToUpper(from), strings.ToUpper(to)
	if from == to {
		return 1, nil
	}
	start := time.Date(m.Year(), m.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, -1)
	now := p.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if start.After(today) {
		return 0, fmt.Errorf("fx: %s is in the future", start.Format("2006-01"))
	}
	if end.After(today) {
		end = today
	}
	key := from + to + start.Format("2006-01") + end.Format("02")
	p.mu.Lock()
	if v, ok := p.cache[key]; ok {
		p.mu.Unlock()
		return v, nil
	}
	p.mu.Unlock()

	q := url.Values{"base": {from}, "symbols": {to}}
	u := fmt.Sprintf("%s/%s..%s?%s", p.BaseURL, start.Format("2006-01-02"), end.Format("2006-01-02"), q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fx: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("fx: HTTP %d: %s", resp.StatusCode, raw)
	}
	var body struct {
		Rates map[string]map[string]float64 `json:"rates"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return 0, fmt.Errorf("fx: %w", err)
	}
	sum, n := 0.0, 0
	for _, day := range body.Rates {
		if v, ok := day[to]; ok {
			sum += v
			n++
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("fx: no %s→%s rates for %s", from, to, start.Format("2006-01"))
	}
	avg := math.Round(sum/float64(n)*10000) / 10000
	p.mu.Lock()
	p.cache[key] = avg
	p.mu.Unlock()
	return avg, nil
}
