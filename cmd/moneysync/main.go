// Command moneysync pulls transactions from Monarch Money and Toshl and
// writes monthly category totals into a Google Sheet.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/fffinkel/moneysync/internal/app"
	"github.com/fffinkel/moneysync/internal/config"
)

var version = "dev"

const usage = `moneysync — Monarch Money + Toshl → Google Sheets

Usage:
  moneysync sync    [flags]   show what would be written (add --write to write it)
  moneysync inspect [flags]   list a source's categories/tags/accounts for a month
  moneysync check   [flags]   validate the config file
  moneysync version

Common flags:
  --config PATH     config file (default: $MONEYSYNC_CONFIG or ./config.jsonc)
  --month  YYYY-MM  month(s) to process; repeatable, comma list or range
                    2026-01..2026-10 (default: last month and this month)

sync flags:
  --target NAME     only this target (repeatable); also runs disabled targets
  --write           actually write to the sheet

inspect flags:
  --source NAME     source to inspect (required)
  --tx              also list every transaction
`

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	if cmd == "version" {
		fmt.Println(version)
		return nil
	}

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	cfgPath := fs.String("config", envOr("MONEYSYNC_CONFIG", "config.jsonc"), "")
	var months, targets multi
	fs.Var(&months, "month", "")
	fs.Var(&targets, "target", "")
	write := fs.Bool("write", false, "")
	source := fs.String("source", "", "")
	showTx := fs.Bool("tx", false, "")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	a := app.New(cfg, os.Stdout)
	ms, err := app.Months(months, cfg.Location, time.Now())
	if err != nil {
		return err
	}

	switch cmd {
	case "check":
		fmt.Printf("config OK: %d sources, %d targets\n", len(cfg.Sources), len(cfg.Targets))
		for _, t := range cfg.Targets {
			state := "enabled"
			if t.Disabled {
				state = "disabled"
			}
			fmt.Printf("  %-28s %-8s source=%s sheet=%q\n", t.Name, state, t.Source, t.Sheet)
		}
		return nil

	case "inspect":
		if *source == "" {
			return errors.New("inspect needs --source")
		}
		for _, m := range ms {
			if err := a.Inspect(ctx, *source, m, *showTx); err != nil {
				return err
			}
			fmt.Println()
		}
		return nil

	case "sync":
		sel, err := a.Selected(targets)
		if err != nil {
			return err
		}
		plan, err := a.BuildPlan(ctx, sel, ms)
		if err != nil {
			return err
		}
		plan.Print(os.Stdout)
		failed := 0
		for _, s := range plan.Sections {
			if s.Err != nil {
				failed++
			}
		}
		n := len(plan.Writes())
		fmt.Println()
		if !*write {
			fmt.Printf("dry run: %d cell(s) would change (marked *). Re-run with --write to apply.\n", n)
		} else if n == 0 {
			fmt.Println("nothing to write.")
		} else {
			wrote, err := a.Apply(ctx, plan)
			if err != nil {
				return err
			}
			fmt.Printf("wrote %d cell(s).\n", wrote)
		}
		if failed > 0 {
			return fmt.Errorf("%d target/month section(s) failed; see ERROR lines above", failed)
		}
		return nil
	}
	return fmt.Errorf("unknown command %q (try --help)", cmd)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
