package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fffinkel/moneysync/internal/config"
	"github.com/fffinkel/moneysync/internal/layout"
	"github.com/fffinkel/moneysync/internal/model"
)

// Inspect prints what a source returns for a month, grouped by category,
// and — for each target fed by that source — which column each category
// lands in. Use it to write or check the column mapping.
func (a *App) Inspect(ctx context.Context, source string, month time.Time, showTx bool) error {
	txs, err := a.Transactions(ctx, source, month)
	if err != nil {
		return err
	}
	w := a.Out
	fmt.Fprintf(w, "%s — %s: %d transactions\n", source, month.Format("January 2006"), len(txs))

	type agg struct {
		typ, group string
		sum        float64
		n          int
		currencies map[string]bool
	}
	cats := map[string]*agg{}
	tags, accts := map[string]float64{}, map[string]float64{}
	for _, t := range txs {
		k := t.Category
		if k == "" {
			k = "(uncategorised)"
		}
		g := cats[k]
		if g == nil {
			g = &agg{typ: t.CategoryType, group: t.CategoryGroup, currencies: map[string]bool{}}
			cats[k] = g
		}
		g.sum += t.Amount
		g.n++
		if t.Currency != "" {
			g.currencies[t.Currency] = true
		}
		for _, tag := range t.Tags {
			tags[tag] += t.Amount
		}
		accts[t.Account] += t.Amount
	}
	sums := map[string]float64{}
	for k, g := range cats {
		sums[k] = g.sum
	}

	var targets []config.Target
	for _, t := range a.Cfg.Targets {
		if t.Source == source {
			targets = append(targets, t)
		}
	}

	fmt.Fprintf(w, "\n%-32s %-9s %-20s %5s %12s", "CATEGORY", "TYPE", "GROUP", "N", "AMOUNT")
	for _, t := range targets {
		fmt.Fprintf(w, "  → %s", t.Name)
	}
	fmt.Fprintln(w)
	for _, k := range layout.SortedKeys(sums) {
		g := cats[k]
		cur := strings.Join(keys(g.currencies), ",")
		fmt.Fprintf(w, "%-32s %-9s %-20s %5d %12.2f %s", trunc(k, 32), g.typ, trunc(g.group, 20), g.n, g.sum, cur)
		for _, t := range targets {
			fmt.Fprintf(w, "  → %s", destination(t, txs, k))
		}
		fmt.Fprintln(w)
	}
	printMap(w, "TAGS", tags)
	printMap(w, "ACCOUNTS", accts)

	if showTx {
		fmt.Fprintln(w, "\nTRANSACTIONS")
		sort.Slice(txs, func(i, j int) bool { return txs[i].Date.Before(txs[j].Date) })
		for _, t := range txs {
			flags := ""
			if t.Hidden {
				flags += " [hidden]"
			}
			if t.Pending {
				flags += " [pending]"
			}
			fmt.Fprintf(w, "%s %10.2f %-3s %-24s %-20s %-20s %s%s\n", t.Date.Format("2006-01-02"), t.Amount, t.Currency,
				trunc(t.Category, 24), trunc(t.Account, 20), trunc(firstNonEmpty(t.Merchant, t.Description), 20), strings.Join(t.Tags, ","), flags)
		}
	}
	return nil
}

// destination says where the first transaction of a category would go.
// (Rules can also look at tags/accounts, so a category can split.)
func destination(t config.Target, txs []model.Transaction, category string) string {
	dests := map[string]bool{}
	for _, tx := range txs {
		c := tx.Category
		if c == "" {
			c = "(uncategorised)"
		}
		if c != category {
			continue
		}
		dests[destOne(t, tx)] = true
	}
	return strings.Join(keys(dests), " / ")
}

func destOne(t config.Target, tx model.Transaction) string {
	switch {
	case tx.Hidden && !t.IncludeHidden:
		return "(hidden)"
	case tx.Pending && t.SkipPending:
		return "(pending)"
	case !t.Include.Empty() && !layout.Matches(t.Include, tx):
		return "(not included)"
	case layout.Matches(t.Exclude, tx):
		return "(excluded)"
	}
	for _, c := range t.Columns {
		if layout.Matches(&c.Match, tx) {
			return columnName(c)
		}
	}
	if t.Fallback != "" {
		return t.Fallback + " (fallback)"
	}
	return "UNMATCHED"
}

func printMap(w interface{ Write([]byte) (int, error) }, title string, m map[string]float64) {
	if len(m) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s\n", title)
	for _, k := range layout.SortedKeys(m) {
		name := k
		if name == "" {
			name = "(none)"
		}
		fmt.Fprintf(w, "  %-40s %12.2f\n", trunc(name, 40), m[k])
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
