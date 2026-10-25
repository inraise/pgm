package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/inraise/pgm/internal/model"
	"github.com/inraise/pgm/internal/testutil"
)

var ctx = context.Background()

func newLocked(t *testing.T) (*PostgresStore, *sql.DB) {
	t.Helper()
	db := testutil.NewDB(t)
	s := NewPostgres(db)
	unlock, err := s.Lock(ctx, time.Second)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	t.Cleanup(func() { _ = unlock() })
	if err := s.EnsureTables(ctx); err != nil {
		t.Fatalf("EnsureTables: %v", err)
	}
	return s, db
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var ok bool
	if err := db.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

const countAdvisory = `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'
	AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`

func TestEnsureTablesIdempotent(t *testing.T) {
	s, db := newLocked(t)
	if err := s.EnsureTables(ctx); err != nil {
		t.Fatalf("повторный вызов: %v", err)
	}
	for _, name := range []string{"schema_migrations", "schema_migration_log"} {
		if !tableExists(t, db, name) {
			t.Errorf("таблица %s не создана", name)
		}
	}
}

func TestApplyUpDownRoundTrip(t *testing.T) {
	s, db := newLocked(t)
	m := model.Migration{Version: 1, Name: "create_t", Checksum: "abc",
		Up: "CREATE TABLE t (id int)", Down: "DROP TABLE t"}

	if err := s.ApplyUp(ctx, m); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Applied(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Version != 1 || rows[0].Name != "create_t" || rows[0].Checksum != "abc" {
		t.Fatalf("Applied: %+v", rows)
	}
	if !tableExists(t, db, "t") {
		t.Fatal("таблица t не создана")
	}

	if err := s.ApplyDown(ctx, m); err != nil {
		t.Fatal(err)
	}
	if rows, _ = s.Applied(ctx); len(rows) != 0 {
		t.Errorf("после отката остались записи: %+v", rows)
	}
	if tableExists(t, db, "t") {
		t.Error("таблица t не удалена")
	}
}

func TestApplyUpRollsBackOnError(t *testing.T) {
	s, db := newLocked(t)
	bad := model.Migration{Version: 1, Name: "broken", Checksum: "x",
		Up: "CREATE TABLE a (id int); CREATE TABLE b (id int); SELECT 1/0;"}

	if err := s.ApplyUp(ctx, bad); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if tableExists(t, db, "a") || tableExists(t, db, "b") {
		t.Error("транзакция не откатилась целиком")
	}
	if rows, _ := s.Applied(ctx); len(rows) != 0 {
		t.Errorf("версия записана несмотря на ошибку: %+v", rows)
	}

	good := model.Migration{Version: 1, Name: "good", Checksum: "y", Up: "CREATE TABLE a (id int)"}
	if err := s.ApplyUp(ctx, good); err != nil {
		t.Fatalf("соединение испорчено после отката: %v", err)
	}
}

func TestApplyErrors(t *testing.T) {
	s, db := newLocked(t)
	m := model.Migration{Version: 1, Name: "a", Checksum: "x", Up: "CREATE TABLE t (id int)"}
	if err := s.ApplyUp(ctx, m); err != nil {
		t.Fatal(err)
	}

	dup := model.Migration{Version: 1, Name: "b", Checksum: "y", Up: "CREATE TABLE u (id int)"}
	if err := s.ApplyUp(ctx, dup); err == nil {
		t.Error("повторная версия должна отклоняться")
	}
	if tableExists(t, db, "u") {
		t.Error("тело повторной миграции не откатилось")
	}

	if err := s.ApplyDown(ctx, model.Migration{Version: 1, Name: "a"}); !errors.Is(err, model.ErrNoDown) {
		t.Errorf("без секции down ожидалась ErrNoDown, получено: %v", err)
	}
	if err := s.ApplyDown(ctx, model.Migration{Version: 5, Down: "SELECT 1"}); err == nil {
		t.Error("откат неприменённой версии должен давать ошибку")
	}
}

func TestAppliedWithoutTables(t *testing.T) {
	db := testutil.NewDB(t)
	s := NewPostgres(db)
	rows, err := s.Applied(ctx)
	if err != nil || len(rows) != 0 {
		t.Fatalf("получено %+v, ошибка %v", rows, err)
	}
	if tableExists(t, db, "schema_migrations") {
		t.Error("Applied не должен создавать таблицы")
	}
}

func TestLog(t *testing.T) {
	s, db := newLocked(t)

	id1, err := s.LogStart(ctx, 1, DirectionUp)
	if err != nil {
		t.Fatal(err)
	}
	var status string
	var finished sql.NullTime
	var errText sql.NullString
	read := func(id int64) {
		t.Helper()
		err := db.QueryRow(`SELECT status, finished_at, error_text FROM schema_migration_log WHERE id = $1`, id).
			Scan(&status, &finished, &errText)
		if err != nil {
			t.Fatal(err)
		}
	}

	read(id1)
	if status != StatusStarted || finished.Valid {
		t.Errorf("после LogStart: %s, finished=%v", status, finished.Valid)
	}
	if err := s.LogFinish(ctx, id1, StatusSuccess, ""); err != nil {
		t.Fatal(err)
	}
	read(id1)
	if status != StatusSuccess || !finished.Valid || errText.Valid {
		t.Errorf("после успеха: %s, finished=%v, error=%v", status, finished.Valid, errText.Valid)
	}

	id2, _ := s.LogStart(ctx, 2, DirectionDown)
	if err := s.LogFinish(ctx, id2, StatusFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	read(id2)
	if status != StatusFailed || errText.String != "boom" {
		t.Errorf("после неудачи: %s, %q", status, errText.String)
	}
}

func TestMutationsRequireLock(t *testing.T) {
	s := NewPostgres(testutil.NewDB(t))
	m := model.Migration{Version: 1, Up: "SELECT 1", Down: "SELECT 1"}
	checks := map[string]error{
		"EnsureTables": s.EnsureTables(ctx),
		"ApplyUp":      s.ApplyUp(ctx, m),
		"ApplyDown":    s.ApplyDown(ctx, m),
		"LogFinish":    s.LogFinish(ctx, 1, StatusSuccess, ""),
	}
	_, checks["LogStart"] = s.LogStart(ctx, 1, DirectionUp)
	for name, err := range checks {
		if !errors.Is(err, ErrNotLocked) {
			t.Errorf("%s без блокировки: ожидалась ErrNotLocked, получено %v", name, err)
		}
	}
}

func TestLockIsExclusive(t *testing.T) {
	db := testutil.NewDB(t)
	s1, s2 := NewPostgres(db), NewPostgres(db)
	s2.poll = 20 * time.Millisecond

	unlock1, err := s1.Lock(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.Lock(ctx, time.Second); !errors.Is(err, errAlreadyLocked) {
		t.Errorf("повторный Lock: ожидалась errAlreadyLocked, получено %v", err)
	}

	start := time.Now()
	_, err = s2.Lock(ctx, 300*time.Millisecond)
	if !errors.Is(err, model.ErrLockTimeout) {
		t.Fatalf("ожидалась ErrLockTimeout, получено: %v", err)
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Error("ожидание закончилось раньше таймаута")
	}

	if err := unlock1(); err != nil {
		t.Fatal(err)
	}
	unlock2, err := s2.Lock(ctx, time.Second)
	if err != nil {
		t.Fatalf("блокировка не освободилась: %v", err)
	}
	_ = unlock2()
}

func TestUnlockReturnsCleanConnectionToPool(t *testing.T) {
	db := testutil.NewDB(t)
	db.SetMaxOpenConns(1)
	s := NewPostgres(db)

	unlock, err := s.Lock(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Applied(ctx); err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err != nil {
		t.Errorf("повторный unlock должен быть безопасным: %v", err)
	}

	var n int
	if err := db.QueryRow(countAdvisory).Scan(&n); err != nil {
		t.Fatalf("соединение не вернулось в пул: %v", err)
	}
	if n != 0 {
		t.Errorf("после unlock осталось advisory-блокировок: %d", n)
	}
}

func TestLockReleasedWhenConnectionDies(t *testing.T) {
	db := testutil.NewDB(t)
	s1, s2 := NewPostgres(db), NewPostgres(db)
	s2.poll = 50 * time.Millisecond

	unlock1, err := s1.Lock(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	var pid int
	err = db.QueryRow(`SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND granted
		AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`SELECT pg_terminate_backend($1)`, pid); err != nil {
		t.Fatal(err)
	}

	unlock2, err := s2.Lock(ctx, 5*time.Second)
	if err != nil {
		t.Fatalf("блокировка не освободилась после обрыва соединения: %v", err)
	}
	_ = unlock2()
	_ = unlock1()
}

func TestLockCanceledWhileWaiting(t *testing.T) {
	db := testutil.NewDB(t)
	s1, s2 := NewPostgres(db), NewPostgres(db)
	s2.poll = 20 * time.Millisecond

	unlock1, err := s1.Lock(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock1()

	waitCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = s2.Lock(waitCtx, 10*time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ожидалась DeadlineExceeded, получено: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("отмена контекста не прервала ожидание блокировки")
	}
}
