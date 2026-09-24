package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/cli"
	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/github"
	"github.com/brymartinez/foreman/internal/render"
	"github.com/brymartinez/foreman/internal/store"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	cmd, err := cli.Parse(argv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n\n%s", err, cli.Usage)
		return 64
	}
	if cmd.Name == cli.Help {
		fmt.Print(cli.Usage)
		return 0
	}
	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	a := app.App{Store: store.Markdown{Dir: cfg.TasksDir}, GitHub: github.New(), Now: func() time.Time { return time.Now().Truncate(time.Second) }}

	switch cmd.Name {
	case cli.Inbox:
		report, err := a.Inbox(ctx)
		if err != nil {
			return fail(err)
		}
		render.Inbox(os.Stdout, report)
	case cli.Status:
		recs, err := a.Status(ctx)
		if err != nil {
			return fail(err)
		}
		render.Status(os.Stdout, recs)
	case cli.Show:
		rec, err := a.Find(ctx, cmd.Repo, cmd.Issue)
		if err != nil {
			return fail(err)
		}
		render.Show(os.Stdout, rec)
	case cli.Start:
		return notYet(cmd.Name, 2)
	case cli.Respond:
		return notYet(cmd.Name, 3)
	}
	return 0
}

func fail(err error) int {
	fmt.Fprintf(os.Stderr, "foreman: %v\n", err)
	return 1
}

func notYet(name cli.Name, stage int) int {
	fmt.Fprintf(os.Stderr, "foreman %s: not implemented yet (Stage %d)\n", name, stage)
	return 2
}
