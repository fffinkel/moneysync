// Package model holds the source-neutral types every data source maps into.
package model

import (
	"context"
	"time"
)

// Category types, normalised across sources.
const (
	TypeExpense  = "expense"
	TypeIncome   = "income"
	TypeTransfer = "transfer"
)

// Transaction is one money movement from any source. Amounts follow the
// common convention of both Monarch and Toshl: money out is negative,
// money in is positive.
type Transaction struct {
	Source        string
	ID            string
	Date          time.Time
	Amount        float64
	Currency      string // ISO code; empty when the source doesn't say
	Category      string
	CategoryGroup string
	CategoryType  string // TypeExpense, TypeIncome, TypeTransfer or ""
	Account       string
	Merchant      string
	Description   string
	Tags          []string
	Hidden        bool // excluded from reports in the source app
	Pending       bool
}

// Source is anything that can list transactions for a date range
// (inclusive on both ends, compared by calendar date).
type Source interface {
	Name() string
	Transactions(ctx context.Context, from, to time.Time) ([]Transaction, error)
}
