// Package layout turns transactions into per-column totals and works out
// which spreadsheet cells those totals belong in.
package layout

import (
	"path"
	"strings"

	"github.com/fffinkel/moneysync/internal/config"
	"github.com/fffinkel/moneysync/internal/model"
)

// Matches reports whether t satisfies m: any of its listed criteria (if
// there are any) and every sub-match in m.All. A nil or empty Match
// matches nothing.
func Matches(m *config.Match, t model.Transaction) bool {
	if m.Empty() {
		return false
	}
	for i := range m.All {
		if !Matches(&m.All[i], t) {
			return false
		}
	}
	return m.AnyEmpty() || matchesAny(m, t)
}

func matchesAny(m *config.Match, t model.Transaction) bool {
	if anyGlob(m.Categories, t.Category) ||
		anyGlob(m.CategoryGroups, t.CategoryGroup) ||
		anyGlob(m.CategoryTypes, t.CategoryType) ||
		anyGlob(m.Accounts, t.Account) ||
		anyGlob(m.Merchants, t.Merchant) {
		return true
	}
	for _, tag := range t.Tags {
		if anyGlob(m.Tags, tag) {
			return true
		}
	}
	desc := strings.ToLower(t.Description)
	for _, s := range m.DescriptionContains {
		if s != "" && strings.Contains(desc, strings.ToLower(s)) {
			return true
		}
	}
	return false
}

func anyGlob(patterns []string, value string) bool {
	if value == "" {
		return false
	}
	v := strings.ToLower(strings.TrimSpace(value))
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == v {
			return true
		}
		if strings.ContainsAny(p, "*?[") {
			if ok, err := path.Match(p, v); err == nil && ok {
				return true
			}
		}
	}
	return false
}
