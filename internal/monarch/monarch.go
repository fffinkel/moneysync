// Package monarch reads transactions from Monarch Money.
//
// Monarch has no official public API; this uses the same GraphQL endpoint
// as the web app, authenticated with a session token. See README for how
// to obtain one.
package monarch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fffinkel/moneysync/internal/model"
)

const DefaultBaseURL = "https://api.monarch.com"

const pageSize = 500

const transactionsQuery = `query GetTransactionsList($offset: Int, $limit: Int, $filters: TransactionFilterInput, $orderBy: TransactionOrdering) {
  allTransactions(filters: $filters) {
    totalCount
    results(offset: $offset, limit: $limit, orderBy: $orderBy) {
      id
      amount
      pending
      date
      hideFromReports
      plaidName
      notes
      category { id name group { id name type } }
      merchant { id name }
      account { id displayName }
      tags { id name }
    }
  }
}`

type Client struct {
	name    string
	token   string
	baseURL string
	http    *http.Client
}

func New(name, token, baseURL string, hc *http.Client) (*Client, error) {
	if token == "" {
		return nil, fmt.Errorf("monarch source %q: token is empty (set it via env:...)", name)
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

type gqlRequest struct {
	OperationName string         `json:"operationName"`
	Query         string         `json:"query"`
	Variables     map[string]any `json:"variables"`
}

type txResult struct {
	ID              string  `json:"id"`
	Amount          float64 `json:"amount"`
	Pending         bool    `json:"pending"`
	Date            string  `json:"date"`
	HideFromReports bool    `json:"hideFromReports"`
	PlaidName       string  `json:"plaidName"`
	Notes           string  `json:"notes"`
	Category        *struct {
		Name  string `json:"name"`
		Group *struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"group"`
	} `json:"category"`
	Merchant *struct {
		Name string `json:"name"`
	} `json:"merchant"`
	Account *struct {
		DisplayName string `json:"displayName"`
	} `json:"account"`
	Tags []struct {
		Name string `json:"name"`
	} `json:"tags"`
}

type gqlResponse struct {
	Data struct {
		AllTransactions struct {
			TotalCount int        `json:"totalCount"`
			Results    []txResult `json:"results"`
		} `json:"allTransactions"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (c *Client) Transactions(ctx context.Context, from, to time.Time) ([]model.Transaction, error) {
	var out []model.Transaction
	for offset := 0; ; offset += pageSize {
		page, total, err := c.page(ctx, from, to, offset)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) < pageSize || offset+len(page) >= total {
			return out, nil
		}
	}
}

func (c *Client) page(ctx context.Context, from, to time.Time, offset int) ([]model.Transaction, int, error) {
	body, _ := json.Marshal(gqlRequest{
		OperationName: "GetTransactionsList",
		Query:         transactionsQuery,
		Variables: map[string]any{
			"offset":  offset,
			"limit":   pageSize,
			"orderBy": "date",
			"filters": map[string]any{
				"startDate":  from.Format("2006-01-02"),
				"endDate":    to.Format("2006-01-02"),
				"search":     "",
				"categories": []string{},
				"accounts":   []string{},
				"tags":       []string{},
			},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/graphql", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Token "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Client-Platform", "web")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("monarch: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, 0, fmt.Errorf("monarch: HTTP %d — token rejected or expired; get a new one (see README)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("monarch: HTTP %d: %s", resp.StatusCode, truncate(raw))
	}
	var gr gqlResponse
	if err := json.Unmarshal(raw, &gr); err != nil {
		return nil, 0, fmt.Errorf("monarch: decode response: %w", err)
	}
	if len(gr.Errors) > 0 {
		msgs := make([]string, len(gr.Errors))
		for i, e := range gr.Errors {
			msgs[i] = e.Message
		}
		return nil, 0, fmt.Errorf("monarch: %s", strings.Join(msgs, "; "))
	}
	res := gr.Data.AllTransactions
	txs := make([]model.Transaction, 0, len(res.Results))
	for _, r := range res.Results {
		t, err := convert(c.name, r)
		if err != nil {
			return nil, 0, err
		}
		txs = append(txs, t)
	}
	return txs, res.TotalCount, nil
}

func convert(source string, r txResult) (model.Transaction, error) {
	d, err := time.Parse("2006-01-02", r.Date)
	if err != nil {
		return model.Transaction{}, fmt.Errorf("monarch: transaction %s: bad date %q", r.ID, r.Date)
	}
	t := model.Transaction{
		Source:      source,
		ID:          r.ID,
		Date:        d,
		Amount:      r.Amount,
		Description: strings.TrimSpace(strings.Join(nonEmpty(r.PlaidName, r.Notes), " — ")),
		Hidden:      r.HideFromReports,
		Pending:     r.Pending,
	}
	if r.Category != nil {
		t.Category = r.Category.Name
		if r.Category.Group != nil {
			t.CategoryGroup = r.Category.Group.Name
			t.CategoryType = strings.ToLower(r.Category.Group.Type)
		}
	}
	if r.Merchant != nil {
		t.Merchant = r.Merchant.Name
	}
	if r.Account != nil {
		t.Account = r.Account.DisplayName
	}
	for _, tag := range r.Tags {
		t.Tags = append(t.Tags, tag.Name)
	}
	return t, nil
}

func nonEmpty(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func truncate(b []byte) string {
	if len(b) > 300 {
		return string(b[:300]) + "…"
	}
	return string(b)
}
