// Package pgm применяет версионированные SQL-миграции к PostgreSQL.
//
// Миграции хранятся в файлах вида NNNN_name.sql, где NNNN - номер версии
// (не меньше четырёх цифр, нумерация с единицы без пропусков), а name -
// описание строчными латинскими буквами и цифрами, слова разделяются
// знаком подчёркивания. Файл состоит из секции up и необязательной
// секции down:
//
//	-- +up
//	CREATE TABLE users (id serial PRIMARY KEY);
//
//	-- +down
//	DROP TABLE users;
//
// # Использование
//
// Миграции удобно встроить в исполняемый файл через embed.FS и применять
// при запуске приложения. Драйвер базы данных выбирает вызывающий код,
// библиотека принимает готовый *sql.DB:
//
//	//go:embed migrations/*.sql
//	var migrationsFS embed.FS
//
//	m, err := pgm.New(db, migrationsFS, "migrations")
//	if err != nil {
//		return err
//	}
//	return m.Up(ctx)
//
// Набор миграций проверяется в New, до обращения к базе.
//
// # Гарантии
//
// Каждая миграция выполняется в отдельной транзакции вместе с записью в
// служебную таблицу schema_migrations: она применяется целиком или не
// применяется вовсе. Параллельные запуски выстраиваются в очередь с помощью
// advisory-блокировки PostgreSQL. Если файл уже применённой миграции
// изменили, Up и Down возвращают ErrChecksumMismatch и ничего не выполняют.
// Команды, которые нельзя выполнять внутри транзакции (например,
// CREATE INDEX CONCURRENTLY), не поддерживаются.
//
// # Ошибки
//
// Ошибки проверяются через errors.Is с переменными Err...; ошибка
// выполнения конкретной миграции имеет тип *MigrationError.
package pgm

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/inraise/pgm/internal/model"
	"github.com/inraise/pgm/internal/parser"
	"github.com/inraise/pgm/internal/source"
	"github.com/inraise/pgm/internal/store"
)

type Migrator struct {
	mu        sync.Mutex
	store     store.Store
	set       []model.Migration
	byVersion map[int64]model.Migration
	cfg       config
}

func New(db *sql.DB, fsys fs.FS, dir string, opts ...Option) (*Migrator, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: db не задан", ErrInvalidArgument)
	}

	return newMigrator(store.NewPostgres(db), fsys, dir, opts...)
}

func newMigrator(st store.Store, fsys fs.FS, dir string, opts ...Option) (*Migrator, error) {
	if fsys == nil {
		return nil, fmt.Errorf("%w: файловая система миграций не задана", ErrInvalidArgument)
	}

	cfg := config{
		lockTimeout: defaultLockTimeout,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, opt := range opts {
		if err := opt(&cfg); err != nil {
			return nil, err
		}
	}

	files, err := source.Load(fsys, dir)
	if err != nil {
		return nil, err
	}
	set := make([]model.Migration, 0, len(files))
	for _, f := range files {
		mig, err := parser.Parse(f.Name, f.Content)
		if err != nil {
			return nil, err
		}
		set = append(set, mig)
	}

	sort.Slice(set, func(i, j int) bool { return set[i].Version < set[j].Version })
	if err := parser.ValidateSet(set); err != nil {
		return nil, err
	}

	byVersion := make(map[int64]model.Migration, len(set))
	for _, mig := range set {
		byVersion[mig.Version] = mig
	}
	cfg.logger.Debug("миграции загружены", "count", len(set))
	return &Migrator{store: st, set: set, byVersion: byVersion, cfg: cfg}, nil
}

func (m *Migrator) Up(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.withLock(ctx, func() error {
		if err := m.store.EnsureTables(ctx); err != nil {
			return err
		}
		applied, err := m.store.Applied(ctx)
		if err != nil {
			return err
		}
		pending, err := m.inspect(applied)
		if err != nil {
			return err
		}
		m.cfg.logger.Debug("состояние схемы", "applied", len(applied), "pending", len(pending))

		for _, mig := range pending {
			if err := m.run(ctx, mig, DirectionUp); err != nil {
				return err
			}
		}
		return nil
	})
}

func (m *Migrator) Down(ctx context.Context, n int, confirmed bool) error {
	if n < 1 {
		return fmt.Errorf("%w: число откатываемых миграций должно быть не меньше 1, получено %d",
			ErrInvalidArgument, n)
	}
	if !confirmed {
		return ErrNotConfirmed
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.withLock(ctx, func() error {
		applied, err := m.store.Applied(ctx)
		if err != nil {
			return err
		}
		targets, err := m.downTargets(applied, n)
		if err != nil {
			return err
		}
		for _, mig := range targets {
			if err := m.run(ctx, mig, DirectionDown); err != nil {
				return err
			}
		}
		return nil
	})
}

func (m *Migrator) Status(ctx context.Context) ([]StatusItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	applied, err := m.store.Applied(ctx)
	if err != nil {
		return nil, err
	}
	appliedBy := make(map[int64]store.AppliedRow, len(applied))
	for _, row := range applied {
		appliedBy[row.Version] = row
	}

	items := make([]StatusItem, 0, len(m.set))
	for _, mig := range m.set {
		item := StatusItem{Version: mig.Version, Name: mig.Name, State: StatePending}
		if row, ok := appliedBy[mig.Version]; ok {
			item.AppliedAt = row.AppliedAt
			item.State = StateApplied
			if row.Checksum != mig.Checksum {
				item.State = StateChanged
			}
		}
		items = append(items, item)
	}
	for _, row := range applied {
		if _, ok := m.byVersion[row.Version]; !ok {
			items = append(items, StatusItem{
				Version: row.Version, Name: row.Name, State: StateMissing, AppliedAt: row.AppliedAt,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Version < items[j].Version })
	return items, nil
}

func (m *Migrator) Plan(ctx context.Context) ([]Migration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	applied, err := m.store.Applied(ctx)
	if err != nil {
		return nil, err
	}
	pending, err := m.inspect(applied)
	if err != nil {
		return nil, err
	}
	return append([]Migration(nil), pending...), nil
}

func (m *Migrator) PlanDown(ctx context.Context, n int) ([]Migration, error) {
	if n < 1 {
		return nil, fmt.Errorf("%w: число откатываемых миграций должно быть не меньше 1, получено %d",
			ErrInvalidArgument, n)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	applied, err := m.store.Applied(ctx)
	if err != nil {
		return nil, err
	}
	return m.downTargets(applied, n)
}

func (m *Migrator) withLock(ctx context.Context, fn func() error) error {
	unlock, err := m.store.Lock(ctx, m.cfg.lockTimeout)
	if err != nil {
		return err
	}
	m.cfg.logger.Debug("блокировка получена")
	defer func() {
		if err := unlock(); err != nil {
			m.cfg.logger.Warn("не удалось снять блокировку", "error", err)
			return
		}
		m.cfg.logger.Debug("блокировка снята")
	}()
	return fn()
}

func (m *Migrator) inspect(applied []store.AppliedRow) ([]model.Migration, error) {
	for _, row := range applied {
		file, ok := m.byVersion[row.Version]
		if !ok {
			return nil, fmt.Errorf("%w: версия %d (%s) записана в базе, но файла нет в каталоге",
				ErrMissingFile, row.Version, row.Name)
		}
		if file.Checksum != row.Checksum {
			return nil, fmt.Errorf("%w: %s изменён после применения (в базе %s, в файле %s)",
				ErrChecksumMismatch, file.Label(), short(row.Checksum), short(file.Checksum))
		}
	}

	for i, row := range applied {
		if want := int64(i + 1); row.Version != want {
			return nil, fmt.Errorf("%w: применённые версии не образуют непрерывную последовательность (найдена %d вместо %d)",
				ErrInvalidSet, row.Version, want)
		}
	}
	return m.set[len(applied):], nil
}

func (m *Migrator) downTargets(applied []store.AppliedRow, n int) ([]model.Migration, error) {
	if _, err := m.inspect(applied); err != nil {
		return nil, err
	}
	count := min(n, len(applied))
	targets := make([]model.Migration, 0, count)
	for i := len(applied) - 1; i >= len(applied)-count; i-- {
		targets = append(targets, m.set[i])
	}
	for _, t := range targets {
		if t.Down == "" {
			return nil, fmt.Errorf("%w: %s", ErrNoDown, t.Label())
		}
	}

	return targets, nil
}

func (m *Migrator) run(ctx context.Context, mig model.Migration, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fail := func(err error) error { return &MigrationError{Migration: mig, Direction: dir, Err: err} }

	id, err := m.store.LogStart(ctx, mig.Version, dir)
	if err != nil {
		return fail(err)
	}
	m.notify(Event{Kind: EventStart, Direction: dir, Migration: mig})

	start := time.Now()
	if dir == DirectionUp {
		err = m.store.ApplyUp(ctx, mig)
	} else {
		err = m.store.ApplyDown(ctx, mig)
	}
	elapsed := time.Since(start)

	logCtx := context.WithoutCancel(ctx)
	if err != nil {
		if lerr := m.store.LogFinish(logCtx, id, store.StatusFailed, err.Error()); lerr != nil {
			m.cfg.logger.Warn("не удалось записать неудачу в журнал", "error", lerr)
		}
		m.notify(Event{Kind: EventFailed, Direction: dir, Migration: mig, Duration: elapsed, Err: err})
		return fail(err)
	}
	if lerr := m.store.LogFinish(logCtx, id, store.StatusSuccess, ""); lerr != nil {
		m.cfg.logger.Warn("не удалось записать успех в журнал", "error", lerr)
	}

	m.notify(Event{Kind: EventDone, Direction: dir, Migration: mig, Duration: elapsed})
	return nil
}

func (m *Migrator) notify(e Event) {
	if m.cfg.observer != nil {
		m.cfg.observer(e)
	}
}

func short(sum string) string {
	if len(sum) > 8 {
		return sum[:8]
	}
	return sum
}
