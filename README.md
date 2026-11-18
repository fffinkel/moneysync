# moneysync

Pull transactions from **Monarch Money** and **Toshl Finance**, total them by
month and category, and write the totals into a **Google Sheet** you already
keep by hand.

- **Config-driven layout.** Which tab, which row, which column each category
  goes to: all of it lives in one config file. Columns are found by
  header text, so you can add or reorder columns in the sheet without
  touching the config.
- **Safe by default.** `sync` is a dry run that shows every cell it would
  change (old → new). Nothing is written until you add `--write`. Cells
  that contain formulas are never overwritten, and rows can carry a label
  check so a layout change can't send numbers to the wrong row.
- **No secrets in files.** Tokens and IDs are read from environment
  variables (`"env:NAME"`) or files (`"file:/path"`).
- **Zero dependencies.** Standard-library Go only.

## Install

```sh
go install github.com/fffinkel/moneysync/cmd/moneysync@latest   # Go 1.24+

# or from source
git clone https://github.com/fffinkel/moneysync.git
cd moneysync
go build -o moneysync ./cmd/moneysync
```

## Setup

### 1. Credentials (environment variables)

| Variable | What it is |
|---|---|
| `MONARCH_TOKEN` | Monarch session token (see below) |
| `TOSHL_TOKEN` | Toshl personal token — create one at <https://developer.toshl.com/apps/> |
| `MONEYSYNC_SPREADSHEET_ID` | The long ID in the sheet URL: `docs.google.com/spreadsheets/d/<ID>/edit` |
| `GOOGLE_APPLICATION_CREDENTIALS` | Path to a Google service-account JSON key… |
| `GOOGLE_ACCESS_TOKEN` | …or a short-lived OAuth token instead |

Keep them in a file outside the repo (e.g. `~/.config/moneysync/env`) and
`source` it, or use your OS keychain / a password manager CLI.

**Monarch token.** Monarch has no official API; moneysync uses the same
GraphQL endpoint as the web app. Log in at <https://app.monarch.com>, open
your browser's developer tools → Network, click any request to
`api.monarch.com/graphql`, and copy the `Authorization` request header
value *after* `Token `. Sessions last a long time but do expire; when they
do, moneysync tells you to fetch a new one.

**Google access.** The simplest durable option is a service account:

1. In Google Cloud Console create a project, enable the **Google Sheets API**.
2. Create a service account and download a JSON key. Store it outside the repo.
3. Share your spreadsheet with the service account's email (`…@….iam.gserviceaccount.com`) as **Editor**.

Alternatively, with `gcloud` installed:

```sh
gcloud auth login --enable-gdrive-access
export GOOGLE_ACCESS_TOKEN=$(gcloud auth print-access-token)
```

### 2. Config

```sh
cp config.example.jsonc config.jsonc   # config.jsonc is git-ignored
```

The example is commented end to end. The main ideas:

- A **target** is one row per month to fill in: a `source`, a `sheet`
  (tab), the `header_row`, and a `row` locator. The locator finds the row
  whose `month_column` holds the month (a real date or text like
  "October 2026"), then moves down `offset` rows. That handles layouts
  with several rows per month (e.g. USD / EUR / TOTAL).
- **columns** map transactions to sheet columns by `header`. The first
  column whose `match` fits wins. A match can test `categories`,
  `category_groups`, `category_types` (`expense` / `income` / `transfer`),
  `tags`, `accounts`, `merchants` and `description_contains`. Values are
  case-insensitive and accept `*` wildcards. For conditions that must
  all hold (a transfer *and* a tag), use `"all": [{…}, {…}]`.
- **include** / **exclude** filter transactions before they reach the
  columns (e.g. drop paychecks and credit-card payments).
- **fallback** names a column for everything unmatched. Without one,
  unmatched spending is listed in the output and not written.
- Amounts are flipped so spending is positive and refunds/income are
  negative, which matches a typical expenses sheet. Use `"keep_sign": true`
  on a column to keep the source's sign.
- **currency** converts foreign-currency transactions at the month's
  average rate. **fx** writes that month's average rate into a cell.
  Rates are ECB reference rates via [Frankfurter](https://frankfurter.dev).

## Use

```sh
./moneysync check                              # validate config
./moneysync inspect --source monarch --month 2026-10   # see category names & where they'd go
./moneysync inspect --source toshl --month 2026-10 --tx
./moneysync sync                               # dry run: last month + this month
./moneysync sync --month 2026-01..2026-10      # dry run for a range
./moneysync sync --target us --write           # write one target
```

`inspect` is the tool for building the mapping: it lists every category
with its total and shows which column each one lands in, so you can spot
`UNMATCHED` or `(fallback)` entries and add rules.

A month row must already exist in the sheet; moneysync fills rows, it
doesn't create them. Months with no transactions are left untouched.

### Run it on a schedule

```sh
# crontab -e — 07:30 every day
30 7 * * * cd /path/to/moneysync && . ~/.config/moneysync/env && ./moneysync sync --write >> ~/moneysync.log 2>&1
```

## Extending

- **New source:** implement `model.Source` (one `Transactions(ctx, from, to)`
  method returning `[]model.Transaction`) in a new package under
  `internal/`, then add a `case` for its `type` in `internal/app/app.go`
  and the type name to `validate()` in `internal/config/config.go`.
- **New match field:** add it to `config.Match` and to `layout.Matches`.
- **Different output:** the planner produces a list of cell changes
  (`app.Plan`) before anything is written. A new writer (CSV, another
  sheet, a database) only has to consume that.

Layout:

```
cmd/moneysync      CLI
internal/config    config file (JSONC), env:/file: secret references
internal/model     Transaction + Source interface
internal/monarch   Monarch GraphQL client
internal/toshl     Toshl REST client
internal/fx        monthly average exchange rates
internal/layout    matching rules, aggregation, header/row lookup
internal/sheets    Google Sheets values API + service-account auth
internal/app       plan / apply / inspect
```

## Security notes

- Never commit `config.jsonc`, `.env` files or service-account keys; all are
  in `.gitignore`, and CI runs a secret scan on every push.
- The Monarch token grants full access to your Monarch account. Treat it
  like a password.
- The service account can only see sheets you explicitly share with it.

## Disclaimer

Not affiliated with Monarch Money, Toshl or Google. The Monarch API is
undocumented and may change without notice.

## License

MIT — see [LICENSE](LICENSE).
