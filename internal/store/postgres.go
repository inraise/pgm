package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/inraise/pgm/internal/model"
)

const lockKey int64 = 0x70676D5F6C6F636B

const (
	defaultPollInterval = 200 * time.Millisecond
	unlockTimeout       = 5 * time.Second
)

const ddlMigrations = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    BIGINT      PRIMARY KEY,
    name       TEXT        NOT NULL,
    checksum   TEXT        NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

const ddlLog = `
CREATE TABLE IF NOT EXISTS schema_migration_log (
    id          BIGSERIAL   PRIMARY KEY,
    version     BIGINT      NOT NULL,
    direction   TEXT        NOT NULL CHECK (direction IN ('up', 'down')),
    status      TEXT        NOT NULL CHECK (status IN ('started', 'success', 'failed')),
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    error_text  TEXT
)`

type executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type PostgresStore struct {
	db   *sql.DB
	poll time.Duration
	conn *sql.Conn
}

var _ Store = (*PostgresStore)(nil)

func NewPostgres(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db, poll: defaultPollInterval}
}

func (s *PostgresStore) session() executor {
	if s.conn != nil {
		return s.conn
	}

	return s.db
}

func (s *PostgresStore) Lock(ctx context.Context, timeout time.Duration) (func() error, error) {
	if s.conn != nil {
		return nil, errAlreadyLocked
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("получение соединения: %w", err)
	}

	deadline := time.Now().Add(timeout)
	for {
		var got bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, lockKey).Scan(&got); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("pg_try_advisory_lock: %w", err)
		}
		if got {
			s.conn = conn
			return s.unlock, nil
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			_ = conn.Close()
			return nil, fmt.Errorf("%w: ожидание дольше %s", model.ErrLockTimeout, timeout)
		}
		timer := time.NewTimer(min(s.poll, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = conn.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *PostgresStore) unlock() error {
	conn := s.conn
	if conn == nil {
		return nil
	}
	s.conn = nil

	ctx, cancel := context.WithTimeout(context.Background(), unlockTimeout)
	defer cancel()

	var released bool
	err := conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock($1)`, lockKey).Scan(&released)
	if err == nil && released {
		return conn.Close()
	}

	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
	if err != nil {
		return fmt.Errorf("pg_advisory_unlock: %w", err)
	}

	return errors.New("pg_advisory_unlock: блокировка не была удержана")
}

func (s *PostgresStore) EnsureTables(ctx context.Context) error {
	if s.conn == nil {
		return ErrNotLocked
	}
	for _, ddl := range []string{ddlMigrations, ddlLog} {
		if _, err := s.conn.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("создание служебных таблиц: %w", err)
		}
	}

	return nil
}

func (s *PostgresStore) Applied(ctx context.Context) ([]AppliedRow, error) {
	q := s.session()

	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, fmt.Errorf("проверка schema_migrations: %w", err)
	}
	if !exists {
		return nil, nil
	}

	rows, err := q.QueryContext(ctx,
		`SELECT version, name, checksum, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("чтение schema_migrations: %w", err)
	}
	defer rows.Close()

	var out []AppliedRow
	for rows.Next() {
		var r AppliedRow
		if err := rows.Scan(&r.Version, &r.Name, &r.Checksum, &r.AppliedAt); err != nil {
			return nil, fmt.Errorf("чтение schema_migrations: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("чтение schema_migrations: %w", err)
	}

	return out, nil
}

func (s *PostgresStore) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if s.conn == nil {
		return ErrNotLocked
	}

	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("начало транзакции: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("фиксация транзакции: %w", err)
	}

	return nil
}

func (s *PostgresStore) ApplyUp(ctx context.Context, m model.Migration) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, m.Up); err != nil {
			return fmt.Errorf("выполнение секции up: %w", err)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`,
			m.Version, m.Name, m.Checksum); err != nil {
			return fmt.Errorf("запись в schema_migrations: %w", err)
		}

		return nil
	})
}

func (s *PostgresStore) ApplyDown(ctx context.Context, m model.Migration) error {
	if m.Down == "" {
		return fmt.Errorf("%w: версия %d", model.ErrNoDown, m.Version)
	}

	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, m.Down); err != nil {
			return fmt.Errorf("выполнение секции down: %w", err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = $1`, m.Version)
		if err != nil {
			return fmt.Errorf("удаление из schema_migrations: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("удаление из schema_migrations: %w", err)
		}
		if n != 1 {
			return fmt.Errorf("версия %d не найдена в schema_migrations", m.Version)
		}

		return nil
	})
}

func (s *PostgresStore) LogStart(ctx context.Context, version int64, direction string) (int64, error) {
	if s.conn == nil {
		return 0, ErrNotLocked
	}

	var id int64
	err := s.conn.QueryRowContext(ctx,
		`INSERT INTO schema_migration_log (version, direction, status)
		 VALUES ($1, $2, 'started') RETURNING id`,
		version, direction).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("запись в журнал: %w", err)
	}

	return id, nil
}

func (s *PostgresStore) LogFinish(ctx context.Context, id int64, status, errText string) error {
	if s.conn == nil {
		return ErrNotLocked
	}

	var text any
	if errText != "" {
		text = errText
	}
	_, err := s.conn.ExecContext(ctx,
		`UPDATE schema_migration_log
		 SET status = $2, finished_at = now(), error_text = $3 WHERE id = $1`,
		id, status, text)
	if err != nil {
		return fmt.Errorf("обновление журнала: %w", err)
	}

	return nil
}
