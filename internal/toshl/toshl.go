// Package toshl reads entries from the Toshl Finance API
// (https://developer.toshl.com), using a personal access token.
package toshl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fffinkel/moneysync/internal/model"
)

const DefaultBaseURL = "https://api.toshl.com"

const perPage = 500

type Client struct {
	name    string
	token   string
	baseURL string
	http    *http.Client

	// id -> name lookups, loaded once per run
	categories map[string]category
	tags       map[string]string
	accounts   map[string]string
}

type category struct {
	Name string
	Type string
}

func New(name, token, baseURL string, hc *http.Client) (*Client, error) {
	if token == "" {
		return nil, fmt.Errorf("toshl source %q: token is empty (set it via env:...)", name)
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{name: name, token: token, baseURL: strings.TrimRight(baseURL, "/"), http: hc}, nil
}

func (c *Client) Name() string { return c.name }

type entry struct {
	ID       string  `json:"id"`
	Amount   float64 `json:"amount"`
	Currency struct {
		Code string `json:"code"`
	} `json:"currency"`
	Date        string          `json:"date"`
	Desc        string          `json:"desc"`
	Account     string          `json:"account"`
	Category    string          `json:"category"`
	Tags        []string        `json:"tags"`
	Transaction json.RawMessage `json:"transaction"` // present on transfers
	Completed   *bool           `json:"completed"`
	Deleted     bool            `json:"deleted"`
}

func (c *Client) Transactions(ctx context.Context, from, to time.Time) ([]model.Transaction, error) {
	if err := c.loadLookups(ctx); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("from", from.Format("2006-01-02"))
	q.Set("to", to.Format("2006-01-02"))
	var entries []entry
	if err := c.list(ctx, "/entries", q, &entries); err != nil {
		return nil, err
	}
	out := make([]model.Transaction, 0, len(entries))
	for _, e := range entries {
		if e.Deleted {
			continue
		}
		d, err := time.Parse("2006-01-02", e.Date)
		if err != nil {
			return nil, fmt.Errorf("toshl: entry %s: bad date %q", e.ID, e.Date)
		}
		cat := c.categories[e.Category]
		t := model.Transaction{
			Source:      c.name,
			ID:          e.ID,
			Date:        d,
			Amount:      e.Amount,
			Currency:    strings.ToUpper(e.Currency.Code),
			Category:    cat.Name,
			Account:     c.accounts[e.Account],
			Description: e.Desc,
			Pending:     e.Completed != nil && !*e.Completed,
		}
		switch {
		case len(e.Transaction) > 0 && string(e.Transaction) != "null":
			t.CategoryType = model.TypeTransfer
		case cat.Type == "expense" || cat.Type == "income":
			t.CategoryType = cat.Type
		case e.Amount < 0:
			t.CategoryType = model.TypeExpense
		default:
			t.CategoryType = model.TypeIncome
		}
		for _, id := range e.Tags {
			if n, ok := c.tags[id]; ok {
				t.Tags = append(t.Tags, n)
			}
		}
		out = append(out, t)
	}
	return out, nil
}

func (c *Client) loadLookups(ctx context.Context) error {
	if c.categories != nil {
		return nil
	}
	var cats []struct {
		ID, Name, Type string
	}
	var tags, accts []struct {
		ID, Name string
	}
	q := url.Values{"include_deleted": {"true"}}
	if err := c.list(ctx, "/categories", q, &cats); err != nil {
		return err
	}
	if err := c.list(ctx, "/tags", q, &tags); err != nil {
		return err
	}
	if err := c.list(ctx, "/accounts", q, &accts); err != nil {
		return err
	}
	c.categories = map[string]category{}
	for _, x := range cats {
		c.categories[x.ID] = category{Name: x.Name, Type: x.Type}
	}
	c.tags = map[string]string{}
	for _, x := range tags {
		c.tags[x.ID] = x.Name
	}
	c.accounts = map[string]string{}
	for _, x := range accts {
		c.accounts[x.ID] = x.Name
	}
	return nil
}

// list fetches every page of a list endpoint into dst (a pointer to a slice).
func (c *Client) list(ctx context.Context, path string, q url.Values, dst any) error {
	var all []json.RawMessage
	for page := 0; ; page++ {
		pq := url.Values{}
		for k, v := range q {
			pq[k] = v
		}
		pq.Set("per_page", strconv.Itoa(perPage))
		pq.Set("page", strconv.Itoa(page))
		items, hasNext, err := c.get(ctx, path+"?"+pq.Encode())
		if err != nil {
			return err
		}
		all = append(all, items...)
		// Follow the Link header, but also keep going on a full page in
		// case a proxy strips headers.
		if len(items) == 0 || (!hasNext && len(items) < perPage) {
			break
		}
	}
	joined, _ := json.Marshal(all)
	return json.Unmarshal(joined, dst)
}

func (c *Client) get(ctx context.Context, pathAndQuery string) ([]json.RawMessage, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+pathAndQuery, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("toshl: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, false, fmt.Errorf("toshl: HTTP 401 — token rejected; create a new one at developer.toshl.com")
	}
	if resp.StatusCode != http.StatusOK {
		msg := string(raw)
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return nil, false, fmt.Errorf("toshl: GET %s: HTTP %d: %s", strings.SplitN(pathAndQuery, "?", 2)[0], resp.StatusCode, msg)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false, fmt.Errorf("toshl: decode: %w", err)
	}
	return items, strings.Contains(resp.Header.Get("Link"), `rel="next"`), nil
}
