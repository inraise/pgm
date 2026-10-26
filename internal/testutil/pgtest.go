package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const envDSN = "PGM_TEST_DSN"

var counter atomic.Int64

func NewDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(envDSN)
	if dsn == "" {
		t.Skipf("%s не задана: интеграционный тест пропущен", envDSN)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("разбор %s: %v", envDSN, err)
	}

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	ctx := context.Background()
	name := fmt.Sprintf("pgm_t_%d_%d", time.Now().UnixNano(), counter.Add(1))
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		_ = admin.Close()
		t.Fatalf("создание базы %s: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		_ = admin.Close()
	})

	u.Path = "/" + name
	return u.String()
}

func NewDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", NewDSN(t))
	if err != nil {
		t.Fatalf("подключение к тестовой базе: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() }) // выполнится раньше удаления базы
	return db
}
