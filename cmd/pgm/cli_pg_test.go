package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inraise/pgm/internal/testutil"
)

var twoMigrations = map[string]string{
	"0001_create_users.sql": "-- +up\nCREATE TABLE users (id serial PRIMARY KEY);\n-- +down\nDROP TABLE users;\n",
	"0002_add_name.sql":     "-- +up\nALTER TABLE users ADD COLUMN name text;\n-- +down\nALTER TABLE users DROP COLUMN name;\n",
}

func writeMigrations(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func cliFor(t *testing.T, dir string) func(args ...string) (int, string, string) {
	t.Helper()
	dsn := testutil.NewDSN(t)
	return func(args ...string) (int, string, string) {
		full := append([]string{"--dsn", dsn, "--dir", dir}, args...)
		return runCLI(t, nil, full...)
	}
}

func TestCLIFlow(t *testing.T) {
	cli := cliFor(t, writeMigrations(t, twoMigrations))

	code, out, _ := cli("status")
	if code != exitOK || !strings.Contains(out, "Версия схемы: 0") || !strings.Contains(out, "pending") {
		t.Fatalf("status до up: код %d, вывод %q", code, out)
	}

	code, out, _ = cli("--dry-run", "up")
	if code != exitOK || !strings.Contains(out, "ничего не изменено") || !strings.Contains(out, "0001_create_users") {
		t.Fatalf("dry-run: код %d, вывод %q", code, out)
	}
	if _, out, _ = cli("status"); !strings.Contains(out, "Версия схемы: 0") {
		t.Fatalf("dry-run изменил базу: %q", out)
	}

	code, out, _ = cli("up")
	if code != exitOK ||
		!strings.Contains(out, "Применяется 0001_create_users ... готово") ||
		!strings.Contains(out, "Применено миграций: 2. Версия схемы: 2") {
		t.Fatalf("up: код %d, вывод %q", code, out)
	}

	code, out, _ = cli("up")
	if code != exitOK || !strings.Contains(out, "Нечего применять. Версия схемы: 2") {
		t.Fatalf("повторный up: код %d, вывод %q", code, out)
	}

	code, out, _ = cli("status")
	if code != exitOK || !strings.Contains(out, "applied") || !strings.Contains(out, "Версия схемы: 2") {
		t.Fatalf("status после up: код %d, вывод %q", code, out)
	}

	code, _, errOut := cli("down", "1")
	if code != exitUsage || !strings.Contains(errOut, "--yes") {
		t.Fatalf("down без --yes: код %d, stderr %q", code, errOut)
	}

	code, out, _ = cli("--dry-run", "down", "1")
	if code != exitOK || !strings.Contains(out, "0002_add_name") {
		t.Fatalf("dry-run down: код %d, вывод %q", code, out)
	}

	code, out, _ = cli("down", "1", "--yes")
	if code != exitOK ||
		!strings.Contains(out, "Откатывается 0002_add_name ... готово") ||
		!strings.Contains(out, "Откачено миграций: 1. Версия схемы: 1") {
		t.Fatalf("down: код %d, вывод %q", code, out)
	}

	code, out, _ = cli("--quiet", "up")
	if code != exitOK || out != "" {
		t.Errorf("--quiet up: код %d, вывод %q", code, out)
	}
}

func TestCLIJSON(t *testing.T) {
	cli := cliFor(t, writeMigrations(t, twoMigrations))

	code, out, _ := cli("--json", "up")
	var up struct {
		Applied []struct {
			Version int64 `json:"version"`
		} `json:"applied"`
		Version int64 `json:"version"`
	}
	if err := json.Unmarshal([]byte(out), &up); err != nil {
		t.Fatalf("up --json не является JSON (в stdout попал лишний вывод?): %q", out)
	}
	if code != exitOK || len(up.Applied) != 2 || up.Version != 2 {
		t.Errorf("код %d, JSON %+v", code, up)
	}

	code, out, _ = cli("--json", "status")
	var st struct {
		Version    int64 `json:"version"`
		Migrations []struct {
			Version   int64  `json:"version"`
			State     string `json:"state"`
			AppliedAt string `json:"applied_at"`
		} `json:"migrations"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status --json не является JSON: %q", out)
	}
	if code != exitOK || st.Version != 2 || len(st.Migrations) != 2 ||
		st.Migrations[0].State != "applied" || st.Migrations[0].AppliedAt == "" {
		t.Errorf("код %d, JSON %+v", code, st)
	}
}

func TestCLIVerbose(t *testing.T) {
	cli := cliFor(t, writeMigrations(t, twoMigrations))
	code, out, errOut := cli("--verbose", "up")
	if code != exitOK {
		t.Fatalf("код %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"блокировка получена", "0001_create_users: готово", "блокировка снята"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("в диагностике нет %q:\n%s", want, errOut)
		}
	}
	if !strings.Contains(out, "Применено миграций: 2") || strings.Contains(out, "Применяется") {
		t.Errorf("stdout должен содержать только итог: %q", out)
	}
}

func TestCLIDrift(t *testing.T) {
	dir := writeMigrations(t, twoMigrations)
	cli := cliFor(t, dir)
	if code, _, errOut := cli("up"); code != exitOK {
		t.Fatalf("up: код %d, stderr %q", code, errOut)
	}

	changed := "-- +up\nCREATE TABLE users (id serial PRIMARY KEY, extra int);\n-- +down\nDROP TABLE users;\n"
	if err := os.WriteFile(filepath.Join(dir, "0001_create_users.sql"), []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, errOut := cli("up")
	if code != exitDrift || !strings.Contains(errOut, "контрольная сумма") || !strings.Contains(errOut, "Ничего не выполнено") {
		t.Errorf("up: код %d, stderr %q", code, errOut)
	}
	code, _, _ = cli("down", "1", "--yes")
	if code != exitDrift {
		t.Errorf("down: код %d, ожидался %d", code, exitDrift)
	}
	code, out, _ := cli("status")
	if code != exitOK || !strings.Contains(out, "changed") {
		t.Errorf("status должен показывать changed: код %d, вывод %q", code, out)
	}

	code, _, errOut = cli("--json", "up")
	var e struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal([]byte(errOut), &e); err != nil || e.Code != exitDrift {
		t.Errorf("JSON-ошибка: %q", errOut)
	}
}

func TestCLIFailedMigration(t *testing.T) {
	dir := writeMigrations(t, map[string]string{
		"0001_ok.sql":  "-- +up\nCREATE TABLE a (id int);\n",
		"0002_bad.sql": "-- +up\nSELECT 1/0;\n",
	})
	cli := cliFor(t, dir)

	code, out, errOut := cli("up")
	if code != exitFailure {
		t.Fatalf("код %d, ожидался %d", code, exitFailure)
	}
	if !strings.Contains(out, "Применяется 0001_ok ... готово") || !strings.Contains(out, "ошибка") {
		t.Errorf("stdout: %q", out)
	}
	for _, want := range []string{"миграция 0002_bad", "division by zero", "Версия схемы: 1"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("в stderr нет %q:\n%s", want, errOut)
		}
	}
}

func TestCLILockTimeout(t *testing.T) {
	dsn := testutil.NewDSN(t)
	dir := writeMigrations(t, twoMigrations)

	holder, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	ctx := context.Background()
	conn, err := holder.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(0x70676D5F6C6F636B)); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	code, _, errOut := runCLI(t, nil, "--dsn", dsn, "--dir", dir, "--lock-timeout", "1", "up")
	if code != exitLock {
		t.Fatalf("код %d, ожидался %d; stderr %q", code, exitLock, errOut)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Error("CLI не дождался таймаута блокировки")
	}
	if !strings.Contains(errOut, "другой экземпляр") {
		t.Errorf("stderr: %q", errOut)
	}
}

func TestCLIStatusUnderReadOnlyRole(t *testing.T) {
	dsn := testutil.NewDSN(t)
	dir := writeMigrations(t, twoMigrations)
	if code, _, errOut := runCLI(t, nil, "--dsn", dsn, "--dir", dir, "up"); code != exitOK {
		t.Fatalf("up: код %d, stderr %q", code, errOut)
	}

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	dbName := strings.TrimPrefix(u.Path, "/")
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	role := fmt.Sprintf("pgm_ro_%d", time.Now().UnixNano())
	ctx := context.Background()
	for _, q := range []string{
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'pw'", role),
		fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s", dbName, role),
		fmt.Sprintf("GRANT USAGE ON SCHEMA public TO %s", role),
		fmt.Sprintf("GRANT SELECT ON schema_migrations TO %s", role),
	} {
		if _, err := admin.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(ctx, "DROP OWNED BY "+role)
		_, _ = admin.ExecContext(ctx, "DROP ROLE "+role)
	})

	ro := *u
	ro.User = url.UserPassword(role, "pw")
	roDSN := ro.String()

	code, out, errOut := runCLI(t, nil, "--dsn", roDSN, "--dir", dir, "status")
	if code != exitOK || !strings.Contains(out, "Версия схемы: 2") {
		t.Fatalf("status: код %d, stdout %q, stderr %q", code, out, errOut)
	}

	files := map[string]string{"0003_more.sql": "-- +up\nCREATE TABLE more (id int);\n"}
	for name, content := range twoMigrations {
		files[name] = content
	}
	code, _, errOut = runCLI(t, nil, "--dsn", roDSN, "--dir", writeMigrations(t, files), "up")
	if code != exitFailure || !strings.Contains(errOut, "permission denied") {
		t.Errorf("up под ролью только для чтения: код %d, stderr %q", code, errOut)
	}
	var n int
	if err := admin.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n != 2 {
		t.Errorf("в schema_migrations %d строк (ошибка %v), ожидалось 2", n, err)
	}
}
