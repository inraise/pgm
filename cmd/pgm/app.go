package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/inraise/pgm"
)

const (
	exitOK      = 0
	exitFailure = 1 // ошибка выполнения: SQL, соединение
	exitUsage   = 2 // аргументы, конфигурация, файлы миграций
	exitDrift   = 3 // расхождение контрольной суммы
	exitLock    = 4 // блокировка не получена
)

type app struct {
	o      options
	stdout io.Writer
	stderr io.Writer

	done  []pgm.Event
	extra string
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	o, c, err := parseArgs(args, getenv)
	a := &app{o: o, stdout: stdout, stderr: stderr}
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, usageText)
		return exitOK
	}
	if err != nil {
		return a.fail(err)
	}
	if c.name == "version" {
		fmt.Fprintf(stdout, "pgm %s\n", buildVersion())
		return exitOK
	}
	if err := a.execute(ctx, c); err != nil {
		return a.fail(err)
	}
	return exitOK
}

func (a *app) execute(ctx context.Context, c command) error {
	switch c.name {
	case "create":
		return a.runCreate(c.arg)
	case "status":
		return a.runStatus(ctx)
	case "up":
		return a.runUp(ctx)
	case "down":
		return a.runDown(ctx, c.n)
	}
	return usagef("неизвестная команда %q", c.name)
}

func exitCodeFor(err error) int {
	var ue *usageError
	switch {
	case errors.As(err, &ue):
		return exitUsage
	case errors.Is(err, pgm.ErrChecksumMismatch):
		return exitDrift
	case errors.Is(err, pgm.ErrLockTimeout):
		return exitLock
	case errors.Is(err, pgm.ErrInvalidSet),
		errors.Is(err, pgm.ErrInvalidArgument),
		errors.Is(err, pgm.ErrSource),
		errors.Is(err, pgm.ErrNoDown),
		errors.Is(err, pgm.ErrNotConfirmed),
		errors.Is(err, pgm.ErrMissingFile):
		return exitUsage
	}
	return exitFailure
}

func errorMessage(err error) string {
	if errors.Is(err, context.Canceled) {
		return "выполнение прервано"
	}
	var me *pgm.MigrationError
	if errors.As(err, &me) {
		return fmt.Sprintf("миграция %s: %v", me.Migration.Label(), me.Err)
	}
	msg := strings.TrimPrefix(err.Error(), "pgm: ")
	if errors.Is(err, pgm.ErrNotConfirmed) {
		msg += ", добавьте --yes"
	}
	return msg
}

func (a *app) fail(err error) int {
	code := exitCodeFor(err)
	msg := errorMessage(err)
	if a.o.jsonOut {
		_ = a.writeJSONTo(a.stderr, struct {
			Error string `json:"error"`
			Code  int    `json:"code"`
		}{msg, code})
		return code
	}
	fmt.Fprintln(a.stderr, "Ошибка: "+msg)
	switch {
	case errors.Is(err, pgm.ErrChecksumMismatch):
		fmt.Fprintln(a.stderr, "Ничего не выполнено.")
	case errors.Is(err, pgm.ErrLockTimeout):
		fmt.Fprintln(a.stderr, "Возможно, другой экземпляр выполняет миграции.")
	}
	if a.extra != "" {
		fmt.Fprintln(a.stderr, a.extra)
	}
	var ue *usageError
	if errors.As(err, &ue) {
		fmt.Fprintln(a.stderr, "Справка: pgm --help")
	}
	return code
}

func (a *app) open(ctx context.Context) (*pgm.Migrator, func(), error) {
	if a.o.dsn == "" {
		return nil, nil, usagef("не задана строка подключения: укажите --dsn или переменную PGM_DSN")
	}
	db, err := sql.Open("pgx", a.o.dsn)
	if err != nil {
		return nil, nil, usagef("некорректная строка подключения: %v", err)
	}
	closeDB := func() { _ = db.Close() }

	opts := []pgm.Option{
		pgm.WithLockTimeout(time.Duration(a.o.lockTimeout) * time.Second),
		pgm.WithObserver(a.handleEvent),
	}
	if a.o.verbose {
		opts = append(opts, pgm.WithLogger(slog.New(newVerboseHandler(a.stderr))))
	}
	m, err := pgm.New(db, os.DirFS(a.o.dir), ".", opts...)
	if err != nil {
		closeDB()
		return nil, nil, err
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		closeDB()
		return nil, nil, fmt.Errorf("подключение к базе: %w", err)
	}
	return m, closeDB, nil
}

func (a *app) runUp(ctx context.Context) error {
	m, closeDB, err := a.open(ctx)
	if err != nil {
		return err
	}
	defer closeDB()

	if a.o.dryRun {
		plan, err := m.Plan(ctx)
		if err != nil {
			return err
		}
		return a.printPlan(plan, "Будут применены (ничего не изменено):", "Нечего применять.")
	}
	if err := m.Up(ctx); err != nil {
		a.describeFailure(ctx, m, err, "Миграция откачена.")
		return err
	}
	return a.printResult(ctx, m, "applied", "Применено миграций", "Нечего применять.")
}

func (a *app) runDown(ctx context.Context, n int) error {
	if !a.o.dryRun && !a.o.yes {
		return pgm.ErrNotConfirmed
	}
	m, closeDB, err := a.open(ctx)
	if err != nil {
		return err
	}
	defer closeDB()

	if a.o.dryRun {
		plan, err := m.PlanDown(ctx, n)
		if err != nil {
			return err
		}
		return a.printPlan(plan, "Будут откачены (ничего не изменено):", "Нечего откатывать.")
	}
	if err := m.Down(ctx, n, true); err != nil {
		a.describeFailure(ctx, m, err, "Откат миграции отменён.")
		return err
	}
	return a.printResult(ctx, m, "rolled_back", "Откачено миграций", "Нечего откатывать.")
}

func (a *app) runStatus(ctx context.Context) error {
	m, closeDB, err := a.open(ctx)
	if err != nil {
		return err
	}
	defer closeDB()

	items, err := m.Status(ctx)
	if err != nil {
		return err
	}
	return a.printStatus(items)
}

func isApplied(s pgm.State) bool {
	return s == pgm.StateApplied || s == pgm.StateChanged || s == pgm.StateMissing
}

func maxApplied(items []pgm.StatusItem) int64 {
	var v int64
	for _, it := range items {
		if isApplied(it.State) && it.Version > v {
			v = it.Version
		}
	}
	return v
}

func (a *app) schemaVersion(ctx context.Context, m *pgm.Migrator) (int64, bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	items, err := m.Status(ctx)
	if err != nil {
		return 0, false
	}
	return maxApplied(items), true
}

func (a *app) describeFailure(ctx context.Context, m *pgm.Migrator, err error, text string) {
	var me *pgm.MigrationError
	if !errors.As(err, &me) {
		return
	}
	if ver, ok := a.schemaVersion(ctx, m); ok {
		a.extra = fmt.Sprintf("%s Версия схемы: %d.", text, ver)
	}
}

func (a *app) handleEvent(e pgm.Event) {
	label := e.Migration.Label()
	switch e.Kind {
	case pgm.EventStart:
		switch {
		case a.o.verbose:
			a.logf("%s: начало (%s)", label, e.Direction)
		case a.showProgress():
			fmt.Fprintf(a.stdout, "%s %s ... ", progressVerb(e.Direction), label)
		}
	case pgm.EventDone:
		a.done = append(a.done, e)
		switch {
		case a.o.verbose:
			a.logf("%s: готово (%d мс)", label, e.Duration.Milliseconds())
		case a.showProgress():
			fmt.Fprintf(a.stdout, "готово (%d мс)\n", e.Duration.Milliseconds())
		}
	case pgm.EventFailed:
		switch {
		case a.o.verbose:
			a.logf("%s: ошибка", label)
		case a.showProgress():
			fmt.Fprintln(a.stdout, "ошибка")
		}
	}
}

func (a *app) showProgress() bool {
	return !a.o.quiet && !a.o.jsonOut && !a.o.verbose
}

func progressVerb(direction string) string {
	if direction == pgm.DirectionDown {
		return "Откатывается"
	}
	return "Применяется"
}
