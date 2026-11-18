package layout

import (
	"math"
	"sort"
	"strings"

	"github.com/fffinkel/moneysync/internal/config"
	"github.com/fffinkel/moneysync/internal/model"
)

// Converter returns a transaction's amount in the target currency.
type Converter func(t model.Transaction) (float64, error)

// Result is the outcome of sorting one month of transactions into a
// target's columns. Totals already carry the sheet sign (spending > 0).
type Result struct {
	Totals    []float64 // parallel to Target.Columns
	Fallback  float64
	Breakdown map[string]map[string]float64 // column header -> category -> amount
	Unmatched map[string]float64            // category -> amount (only when no fallback)
	Counts    struct {
		Used, Excluded, Hidden, Pending, Unmatched, Converted int
	}
}

// FallbackKey is the Breakdown key used for the fallback column.
const FallbackKey = "(fallback)"

func Aggregate(t config.Target, txs []model.Transaction, conv Converter) (Result, error) {
	r := Result{
		Totals:    make([]float64, len(t.Columns)),
		Breakdown: map[string]map[string]float64{},
		Unmatched: map[string]float64{},
	}
	add := func(key, cat string, v float64) {
		if r.Breakdown[key] == nil {
			r.Breakdown[key] = map[string]float64{}
		}
		r.Breakdown[key][label(cat)] += v
	}
	for _, tx := range txs {
		switch {
		case tx.Hidden && !t.IncludeHidden:
			r.Counts.Hidden++
			continue
		case tx.Pending && t.SkipPending:
			r.Counts.Pending++
			continue
		case !t.Include.Empty() && !Matches(t.Include, tx):
			r.Counts.Excluded++
			continue
		case Matches(t.Exclude, tx):
			r.Counts.Excluded++
			continue
		}
		amount := tx.Amount
		if conv != nil {
			a, err := conv(tx)
			if err != nil {
				return r, err
			}
			if a != amount {
				r.Counts.Converted++
			}
			amount = a
		}
		matched := false
		for i, col := range t.Columns {
			if Matches(&col.Match, tx) {
				v := -amount
				if col.KeepSign {
					v = amount
				}
				r.Totals[i] += v
				add(columnKey(col), tx.Category, v)
				matched = true
				break
			}
		}
		r.Counts.Used++
		if matched {
			continue
		}
		if t.Fallback != "" {
			r.Fallback += -amount
			add(FallbackKey, tx.Category, -amount)
		} else {
			r.Counts.Used--
			r.Counts.Unmatched++
			r.Unmatched[label(tx.Category)] += -amount
		}
	}
	for i := range r.Totals {
		r.Totals[i] = Round2(r.Totals[i])
	}
	r.Fallback = Round2(r.Fallback)
	return r, nil
}

func columnKey(c config.Column) string {
	if c.Header != "" {
		return c.Header
	}
	return c.Column
}

func label(cat string) string {
	if strings.TrimSpace(cat) == "" {
		return "(uncategorised)"
	}
	return cat
}

// Round2 rounds to cents, avoiding -0.
func Round2(v float64) float64 {
	v = math.Round(v*100) / 100
	if v == 0 {
		return 0
	}
	return v
}

// SortedKeys returns map keys ordered by absolute amount, largest first.
func SortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := math.Abs(m[keys[i]]), math.Abs(m[keys[j]])
		if a != b {
			return a > b
		}
		return keys[i] < keys[j]
	})
	return keys
}
