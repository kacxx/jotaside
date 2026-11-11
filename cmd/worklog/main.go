// Command worklog links a markdown worklog's items to the agent sessions that
// wrote them (kacxx/notebook#35). It is separate from jotaside, and reuses its
// transcript, resume-command and find code.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/kacxx/jotaside/internal/app"
	"github.com/kacxx/jotaside/internal/worklog"
)

func main() { os.Exit(mainCode()) }

func mainCode() int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "worklog:", err)
		return 1
	}
	env := worklog.Env{Getenv: os.Getenv, Home: home}
	// The key fallback reads jotaside's database, only if it's already there:
	// opening it would create one.
	if path, err := app.DBPath(); err == nil {
		if _, err := os.Stat(path); err == nil {
			if svc, err := app.Open(path, app.BusyTimeout()); err == nil {
				defer svc.Close()
				env.Find = svc.Find
				env.Sessions = func(ctx context.Context) ([]app.Session, error) { return svc.Sessions(ctx, 0) }
			}
		}
	}
	return worklog.Run(context.Background(), os.Args[1:], env, os.Stdout, os.Stderr)
}
