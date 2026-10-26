package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inraise/pgm"
)

const fakeDSN = "postgres://u:p@127.0.0.1:1/db"

func runCLI(t *testing.T, env map[string]string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(context.Background(), args, &out, &errb, func(k string) string { return env[k] })
	return code, out.String(), errb.String()
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    command
		check   func(options) bool
		wantErr bool
	}{
		{name: "up", args: []string{"up"}, want: command{name: "up"}},
		{name: "флаг до команды", args: []string{"--dry-run", "up"}, want: command{name: "up"},
			check: func(o options) bool { return o.dryRun }},
		{name: "флаг после команды", args: []string{"down", "1", "--yes"}, want: command{name: "down", n: 1},
			check: func(o options) bool { return o.yes }},
		{name: "флаг между", args: []string{"--yes", "down", "3", "--json"}, want: command{name: "down", n: 3},
			check: func(o options) bool { return o.yes && o.jsonOut }},
		{name: "значение флага", args: []string{"--dsn=x", "--dir", "m", "--lock-timeout", "5", "status"},
			want:  command{name: "status"},
			check: func(o options) bool { return o.dsn == "x" && o.dir == "m" && o.lockTimeout == 5 }},
		{name: "create", args: []string{"create", "add_users"}, want: command{name: "create", arg: "add_users"}},

		{name: "нет команды", args: nil, wantErr: true},
		{name: "неизвестная команда", args: []string{"fly"}, wantErr: true},
		{name: "down без числа", args: []string{"down"}, wantErr: true},
		{name: "down ноль", args: []string{"down", "0"}, wantErr: true},
		{name: "down не число", args: []string{"down", "x"}, wantErr: true},
		{name: "лишний аргумент", args: []string{"up", "extra"}, wantErr: true},
		{name: "create без имени", args: []string{"create"}, wantErr: true},
		{name: "quiet и verbose", args: []string{"--quiet", "--verbose", "up"}, wantErr: true},
		{name: "таймаут ноль", args: []string{"--lock-timeout", "0", "up"}, wantErr: true},
		{name: "dry-run для status", args: []string{"--dry-run", "status"}, wantErr: true},
		{name: "неизвестный флаг", args: []string{"--nope", "up"}, wantErr: true},
		{name: "флаг без значения", args: []string{"up", "--dsn"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, c, err := parseArgs(tt.args, func(string) string { return "" })
			if tt.wantErr {
				var ue *usageError
				if !errors.As(err, &ue) {
					t.Fatalf("ожидалась usageError, получено: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c != tt.want {
				t.Errorf("команда %+v, ожидалась %+v", c, tt.want)
			}
			if tt.check != nil && !tt.check(o) {
				t.Errorf("параметры разобраны неверно: %+v", o)
			}
		})
	}
}

func TestParseArgsEnvDSN(t *testing.T) {
	o, _, err := parseArgs([]string{"up"}, func(k string) string {
		if k == "PGM_DSN" {
			return "from-env"
		}
		return ""
	})
	if err != nil || o.dsn != "from-env" {
		t.Errorf("dsn=%q, ошибка %v", o.dsn, err)
	}
	o, _, _ = parseArgs([]string{"--dsn", "flag", "up"}, func(string) string { return "from-env" })
	if o.dsn != "flag" {
		t.Errorf("флаг должен иметь приоритет над окружением, получено %q", o.dsn)
	}
}

func TestHelpAndVersion(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		code, out, _ := runCLI(t, nil, args...)
		if code != exitOK || !strings.Contains(out, "Команды:") {
			t.Errorf("%v: код %d, вывод %q", args, code, out)
		}
	}
	code, out, _ := runCLI(t, nil, "--version")
	if code != exitOK || !strings.HasPrefix(out, "pgm ") {
		t.Errorf("--version: код %d, вывод %q", code, out)
	}
}

func TestUsageErrors(t *testing.T) {
	code, _, errOut := runCLI(t, nil)
	if code != exitUsage || !strings.Contains(errOut, "Справка: pgm --help") {
		t.Errorf("без команды: код %d, stderr %q", code, errOut)
	}
	code, _, errOut = runCLI(t, nil, "up")
	if code != exitUsage || !strings.Contains(errOut, "PGM_DSN") {
		t.Errorf("без DSN: код %d, stderr %q", code, errOut)
	}
}

func TestDownNeedsConfirmation(t *testing.T) {
	code, out, errOut := runCLI(t, nil, "--dsn", fakeDSN, "down", "1")
	if code != exitUsage || out != "" || !strings.Contains(errOut, "--yes") {
		t.Errorf("код %d, stdout %q, stderr %q", code, out, errOut)
	}

	code, _, errOut = runCLI(t, nil, "--json", "down", "1")
	var e struct {
		Error string `json:"error"`
		Code  int    `json:"code"`
	}
	if err := json.Unmarshal([]byte(errOut), &e); err != nil {
		t.Fatalf("ошибка в режиме --json не является JSON: %q", errOut)
	}
	if code != exitUsage || e.Code != exitUsage || e.Error == "" {
		t.Errorf("код %d, JSON %+v", code, e)
	}
}

func TestBadMigrationDirIsReportedBeforeConnecting(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	code, _, errOut := runCLI(t, nil, "--dsn", fakeDSN, "--dir", missing, "up")
	if code != exitUsage || !strings.Contains(errOut, "каталог") {
		t.Errorf("код %d, stderr %q", code, errOut)
	}
}

func TestFreshlyCreatedFileIsRejectedUntilFilled(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "migrations")
	if code, _, _ := runCLI(t, nil, "--dir", dir, "create", "add_users"); code != exitOK {
		t.Fatal("create не удался")
	}
	code, _, errOut := runCLI(t, nil, "--dsn", fakeDSN, "--dir", dir, "up")
	if code != exitUsage || !strings.Contains(errOut, "секция up пуста") {
		t.Errorf("код %d, stderr %q", code, errOut)
	}
}

func TestCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "migrations") // каталога ещё нет

	code, out, _ := runCLI(t, nil, "--dir", dir, "create", "add_users")
	if code != exitOK || !strings.Contains(out, "Создан файл") || !strings.Contains(out, "0001_add_users.sql") {
		t.Fatalf("код %d, вывод %q", code, out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "0001_add_users.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if text := string(data); !strings.Contains(text, "-- +up") || !strings.Contains(text, "-- +down") {
		t.Errorf("заготовка без секций: %q", text)
	}

	runCLI(t, nil, "--dir", dir, "create", "add_index")
	if _, err := os.Stat(filepath.Join(dir, "0002_add_index.sql")); err != nil {
		t.Errorf("второй файл не создан: %v", err)
	}

	// Следующий номер считается по максимальному, а не по количеству файлов.
	if err := os.WriteFile(filepath.Join(dir, "0007_x.sql"), []byte("-- +up\nSELECT 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCLI(t, nil, "--dir", dir, "create", "next")
	if _, err := os.Stat(filepath.Join(dir, "0008_next.sql")); err != nil {
		t.Errorf("номер после 0007 должен быть 0008: %v", err)
	}
}

func TestCreateModes(t *testing.T) {
	dir := t.TempDir()

	code, out, _ := runCLI(t, nil, "--dir", dir, "--quiet", "create", "a")
	if code != exitOK || out != "" {
		t.Errorf("--quiet: код %d, вывод %q", code, out)
	}
	code, out, _ = runCLI(t, nil, "--dir", dir, "--json", "create", "b")
	var res struct {
		Created string `json:"created"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || !strings.HasSuffix(res.Created, "0002_b.sql") {
		t.Errorf("--json: код %d, вывод %q", code, out)
	}
}

func TestCreateInvalidName(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Add Users", "UPPER", "-x", "a__b", "a-b", "../x", ""} {
		code, _, _ := runCLI(t, nil, "--dir", dir, "create", name)
		if code != exitUsage {
			t.Errorf("имя %q: код %d, ожидался %d", name, code, exitUsage)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("при ошибке файлы создаваться не должны: %v", entries)
	}
}

func TestExitCodeFor(t *testing.T) {
	wrap := func(err error) error { return errors.Join(errors.New("контекст"), err) }
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"дрейф", wrap(pgm.ErrChecksumMismatch), exitDrift},
		{"блокировка", wrap(pgm.ErrLockTimeout), exitLock},
		{"набор", wrap(pgm.ErrInvalidSet), exitUsage},
		{"аргумент", wrap(pgm.ErrInvalidArgument), exitUsage},
		{"каталог", wrap(pgm.ErrSource), exitUsage},
		{"нет down", wrap(pgm.ErrNoDown), exitUsage},
		{"нет подтверждения", pgm.ErrNotConfirmed, exitUsage},
		{"нет файла", wrap(pgm.ErrMissingFile), exitUsage},
		{"использование", usagef("x"), exitUsage},
		{"прочее", errors.New("boom"), exitFailure},
		{"ошибка SQL в миграции", &pgm.MigrationError{Err: errors.New("sql")}, exitFailure},
		{"отмена", context.Canceled, exitFailure},
	}
	for _, tt := range tests {
		if got := exitCodeFor(tt.err); got != tt.want {
			t.Errorf("%s: код %d, ожидался %d", tt.name, got, tt.want)
		}
	}
}

func TestErrorMessage(t *testing.T) {
	me := &pgm.MigrationError{
		Migration: pgm.Migration{Version: 3, Name: "add_x"},
		Direction: pgm.DirectionUp,
		Err:       errors.New("relation does not exist"),
	}
	if got := errorMessage(me); got != "миграция 0003_add_x: relation does not exist" {
		t.Errorf("получено %q", got)
	}
	if got := errorMessage(pgm.ErrNotConfirmed); !strings.Contains(got, "--yes") || strings.HasPrefix(got, "pgm:") {
		t.Errorf("получено %q", got)
	}
	if got := errorMessage(context.Canceled); !strings.Contains(got, "прервано") {
		t.Errorf("получено %q", got)
	}
}
