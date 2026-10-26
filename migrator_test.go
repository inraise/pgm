package pgm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/inraise/pgm/internal/model"
	"github.com/inraise/pgm/internal/store"
	"github.com/inraise/pgm/internal/store/storetest"
)

var bg = context.Background()

func fsOf(n int) fstest.MapFS {
	fsys := fstest.MapFS{}
	for i := 1; i <= n; i++ {
		fsys[fmt.Sprintf("%04d_m%d.sql", i, i)] = &fstest.MapFile{
			Data: []byte(fmt.Sprintf("-- +up\nSELECT %d;\n-- +down\nSELECT -%d;\n", i, i)),
		}
	}
	return fsys
}

func mustMigrator(t *testing.T, st store.Store, fsys fs.FS, opts ...Option) *Migrator {
	t.Helper()
	m, err := newMigrator(st, fsys, ".", opts...)
	if err != nil {
		t.Fatalf("newMigrator: %v", err)
	}
	return m
}

func rowsFor(t *testing.T, fsys fstest.MapFS, versions ...int64) []store.AppliedRow {
	t.Helper()
	probe := mustMigrator(t, storetest.NewFake(), fsys)
	rows := make([]store.AppliedRow, 0, len(versions))
	for _, v := range versions {
		mig, ok := probe.byVersion[v]
		if !ok {
			t.Fatalf("версии %d нет в наборе", v)
		}
		rows = append(rows, store.AppliedRow{
			Version: v, Name: mig.Name, Checksum: mig.Checksum, AppliedAt: time.Now(),
		})
	}
	return rows
}

func setup(t *testing.T, fsys fstest.MapFS, applied int, opts ...Option) (*Migrator, *storetest.FakeStore) {
	t.Helper()
	versions := make([]int64, applied)
	for i := range versions {
		versions[i] = int64(i + 1)
	}
	fake := storetest.NewFake(rowsFor(t, fsys, versions...)...)
	return mustMigrator(t, fake, fsys, opts...), fake
}

func only(calls []string, prefix string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func TestNewValidation(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	const sql = "-- +up\nSELECT 1;\n"
	tests := []struct {
		name string
		fsys fs.FS
		dir  string
		want error
	}{
		{"плохое имя файла", fstest.MapFS{"x.sql": file(sql)}, ".", ErrInvalidSet},
		{"пропуск версии", fstest.MapFS{"0001_a.sql": file(sql), "0003_c.sql": file(sql)}, ".", ErrInvalidSet},
		{"дубль версии", fstest.MapFS{"0001_a.sql": file(sql), "0001_b.sql": file(sql)}, ".", ErrInvalidSet},
		{"нет каталога", fstest.MapFS{}, "missing", ErrSource},
		{"fs не задана", nil, ".", ErrInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newMigrator(storetest.NewFake(), tt.fsys, tt.dir)
			if !errors.Is(err, tt.want) {
				t.Errorf("ожидалась %v, получено: %v", tt.want, err)
			}
		})
	}
}

func TestNewSortsByVersionNotByName(t *testing.T) {
	fsys := fstest.MapFS{}
	for i := 1; i <= 10000; i++ {
		fsys[fmt.Sprintf("%04d_m.sql", i)] = &fstest.MapFile{Data: []byte("-- +up\nSELECT 1;\n")}
	}
	m := mustMigrator(t, storetest.NewFake(), fsys)
	if m.set[9998].Version != 9999 || m.set[9999].Version != 10000 {
		t.Error("набор не отсортирован по версии")
	}
}

func TestOptionsValidation(t *testing.T) {
	for name, opt := range map[string]Option{
		"таймаут 0":  WithLockTimeout(0),
		"логгер nil": WithLogger(nil),
	} {
		_, err := newMigrator(storetest.NewFake(), fsOf(1), ".", opt)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%s: ожидалась ErrInvalidArgument, получено %v", name, err)
		}
	}
}

func TestUpAppliesPendingInOrder(t *testing.T) {
	m, fake := setup(t, fsOf(2), 0)
	if err := m.Up(bg); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Lock", "EnsureTables", "Applied",
		"LogStart 1 up", "ApplyUp 1", "LogFinish 1 success",
		"LogStart 2 up", "ApplyUp 2", "LogFinish 2 success",
		"Unlock",
	}
	if got := fake.Calls(); !slices.Equal(got, want) {
		t.Errorf("порядок вызовов:\nполучено   %v\nожидалось %v", got, want)
	}
}

func TestUpIsIdempotent(t *testing.T) {
	m, fake := setup(t, fsOf(2), 0)
	for i := 0; i < 2; i++ {
		if err := m.Up(bg); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(only(fake.Calls(), "ApplyUp")); n != 2 {
		t.Errorf("ApplyUp вызван %d раз, ожидалось 2", n)
	}
}

func TestUpContinuesAfterPartialState(t *testing.T) {
	m, fake := setup(t, fsOf(3), 1)
	if err := m.Up(bg); err != nil {
		t.Fatal(err)
	}
	if got := only(fake.Calls(), "ApplyUp"); !slices.Equal(got, []string{"ApplyUp 2", "ApplyUp 3"}) {
		t.Errorf("применены: %v", got)
	}
}

func TestUpStopsOnError(t *testing.T) {
	m, fake := setup(t, fsOf(3), 0)
	boom := errors.New("boom")
	fake.ApplyUpErr = map[int64]error{2: boom}

	err := m.Up(bg)
	calls := fake.Calls()

	var me *MigrationError
	if !errors.As(err, &me) || me.Migration.Version != 2 || me.Direction != DirectionUp {
		t.Fatalf("ожидалась *MigrationError для версии 2, получено: %v", err)
	}
	if !errors.Is(err, boom) {
		t.Error("исходная причина потеряна")
	}
	if n := len(only(calls, "ApplyUp")); n != 2 {
		t.Errorf("ApplyUp вызван %d раз: версия 3 не должна запускаться", n)
	}
	if calls[len(calls)-1] != "Unlock" {
		t.Error("блокировка не снята после ошибки")
	}
	log := fake.Log()
	if len(log) != 2 || log[0].Status != store.StatusSuccess ||
		log[1].Status != store.StatusFailed || !strings.Contains(log[1].ErrText, "boom") {
		t.Errorf("журнал: %+v", log)
	}
	if rows, _ := fake.Applied(bg); len(rows) != 1 {
		t.Errorf("применённых %d, ожидалась 1", len(rows))
	}
}

func TestUpRejectsDrift(t *testing.T) {
	fake := storetest.NewFake(store.AppliedRow{Version: 1, Name: "m1", Checksum: "deadbeef"})
	m := mustMigrator(t, fake, fsOf(2))

	if err := m.Up(bg); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("ожидалась ErrChecksumMismatch, получено: %v", err)
	}
	calls := fake.Calls()
	if len(only(calls, "ApplyUp")) != 0 || len(only(calls, "LogStart")) != 0 {
		t.Errorf("при дрейфе ничего не должно выполняться: %v", calls)
	}
	if calls[len(calls)-1] != "Unlock" {
		t.Error("блокировка не снята")
	}
}

func TestUpRejectsMissingFile(t *testing.T) {
	fake := storetest.NewFake(store.AppliedRow{Version: 5, Name: "ghost", Checksum: "x"})
	m := mustMigrator(t, fake, fsOf(2))
	if err := m.Up(bg); !errors.Is(err, ErrMissingFile) {
		t.Fatalf("ожидалась ErrMissingFile, получено: %v", err)
	}
}

func TestUpRejectsGapInApplied(t *testing.T) {
	fsys := fsOf(3)
	fake := storetest.NewFake(rowsFor(t, fsys, 1, 3)...)
	m := mustMigrator(t, fake, fsys)
	if err := m.Up(bg); !errors.Is(err, ErrInvalidSet) {
		t.Fatalf("ожидалась ErrInvalidSet, получено: %v", err)
	}
}

func TestUpLockTimeout(t *testing.T) {
	m, fake := setup(t, fsOf(1), 0)
	fake.LockErr = fmt.Errorf("%w: тест", model.ErrLockTimeout)
	if err := m.Up(bg); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("ожидалась ErrLockTimeout, получено: %v", err)
	}
	if got := fake.Calls(); !slices.Equal(got, []string{"Lock"}) {
		t.Errorf("после неудачной блокировки не должно быть других вызовов: %v", got)
	}
}

func TestUpCanceledContext(t *testing.T) {
	m, fake := setup(t, fsOf(2), 0)
	ctx, cancel := context.WithCancel(bg)
	cancel()

	if err := m.Up(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ожидалась context.Canceled, получено: %v", err)
	}
	calls := fake.Calls()
	if len(only(calls, "ApplyUp")) != 0 {
		t.Errorf("при отмене миграции не запускаются: %v", calls)
	}
	if calls[len(calls)-1] != "Unlock" {
		t.Error("блокировка не снята")
	}
}

func TestDownValidation(t *testing.T) {
	m, fake := setup(t, fsOf(2), 2)
	if err := m.Down(bg, 0, true); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("n=0: ожидалась ErrInvalidArgument, получено %v", err)
	}
	if err := m.Down(bg, 1, false); !errors.Is(err, ErrNotConfirmed) {
		t.Errorf("без подтверждения: ожидалась ErrNotConfirmed, получено %v", err)
	}
	if calls := fake.Calls(); len(calls) != 0 {
		t.Errorf("до проверки аргументов база не должна затрагиваться: %v", calls)
	}
}

func TestDownReverseOrder(t *testing.T) {
	m, fake := setup(t, fsOf(3), 3)
	if err := m.Down(bg, 2, true); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if got := only(calls, "ApplyDown"); !slices.Equal(got, []string{"ApplyDown 3", "ApplyDown 2"}) {
		t.Errorf("порядок отката: %v", got)
	}
	if calls[len(calls)-1] != "Unlock" {
		t.Error("блокировка не снята")
	}
	if rows, _ := fake.Applied(bg); len(rows) != 1 || rows[0].Version != 1 {
		t.Errorf("остались: %+v", rows)
	}
}

func TestDownChecksAllDownSectionsFirst(t *testing.T) {
	fsys := fsOf(3)
	fsys["0002_m2.sql"] = &fstest.MapFile{Data: []byte("-- +up\nSELECT 2;\n")} // без down
	m, fake := setup(t, fsys, 3)

	if err := m.Down(bg, 2, true); !errors.Is(err, ErrNoDown) {
		t.Fatalf("ожидалась ErrNoDown, получено: %v", err)
	}
	if n := len(only(fake.Calls(), "ApplyDown")); n != 0 {
		t.Errorf("версия 3 откатилась, хотя 2 откатить нельзя (ApplyDown вызван %d раз)", n)
	}
}

func TestDownMoreThanApplied(t *testing.T) {
	m, fake := setup(t, fsOf(3), 2)
	if err := m.Down(bg, 10, true); err != nil {
		t.Fatal(err)
	}
	if rows, _ := fake.Applied(bg); len(rows) != 0 {
		t.Errorf("остались: %+v", rows)
	}
}

func TestDownNothingApplied(t *testing.T) {
	m, fake := setup(t, fsOf(2), 0)
	if err := m.Down(bg, 1, true); err != nil {
		t.Fatal(err)
	}
	if n := len(only(fake.Calls(), "ApplyDown")); n != 0 {
		t.Errorf("откатывать нечего, но ApplyDown вызван %d раз", n)
	}
}

func TestDownRejectsDrift(t *testing.T) {
	fake := storetest.NewFake(store.AppliedRow{Version: 1, Name: "m1", Checksum: "deadbeef"})
	m := mustMigrator(t, fake, fsOf(1))
	if err := m.Down(bg, 1, true); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("ожидалась ErrChecksumMismatch, получено: %v", err)
	}
	if n := len(only(fake.Calls(), "ApplyDown")); n != 0 {
		t.Error("откат по изменённому файлу выполнил бы не тот SQL")
	}
}

func TestStatus(t *testing.T) {
	fsys := fsOf(4)
	rows := rowsFor(t, fsys, 1, 2)
	rows[1].Checksum = "changed"
	rows = append(rows, store.AppliedRow{Version: 9, Name: "ghost", Checksum: "x", AppliedAt: time.Now()})
	fake := storetest.NewFake(rows...)
	m := mustMigrator(t, fake, fsys)

	items, err := m.Status(bg)
	if err != nil {
		t.Fatal(err)
	}
	wantStates := []State{StateApplied, StateChanged, StatePending, StatePending, StateMissing}
	wantVersions := []int64{1, 2, 3, 4, 9}
	if len(items) != len(wantStates) {
		t.Fatalf("строк %d, ожидалось %d: %+v", len(items), len(wantStates), items)
	}
	for i, it := range items {
		if it.Version != wantVersions[i] || it.State != wantStates[i] {
			t.Errorf("строка %d: %+v", i, it)
		}
	}
	if items[0].AppliedAt.IsZero() || !items[2].AppliedAt.IsZero() {
		t.Error("AppliedAt должна быть задана у применённых и пуста у ожидающих")
	}
	if calls := fake.Calls(); !slices.Equal(calls, []string{"Applied"}) {
		t.Errorf("Status не должен брать блокировку и менять данные: %v", calls)
	}
}

func TestPlan(t *testing.T) {
	m, fake := setup(t, fsOf(3), 1)

	plan, err := m.Plan(bg)
	if err != nil || len(plan) != 2 || plan[0].Version != 2 || plan[1].Version != 3 {
		t.Fatalf("Plan: %+v, %v", plan, err)
	}
	down, err := m.PlanDown(bg, 5)
	if err != nil || len(down) != 1 || down[0].Version != 1 {
		t.Fatalf("PlanDown: %+v, %v", down, err)
	}
	for _, c := range fake.Calls() {
		if c != "Applied" {
			t.Errorf("dry-run изменил состояние: вызов %q", c)
		}
	}
}

func TestObserverEvents(t *testing.T) {
	var events []Event
	m, fake := setup(t, fsOf(2), 0, WithObserver(func(e Event) { events = append(events, e) }))
	fake.ApplyUpErr = map[int64]error{2: errors.New("boom")}

	_ = m.Up(bg)

	type ev struct {
		kind    EventKind
		version int64
	}
	var got []ev
	for _, e := range events {
		got = append(got, ev{e.Kind, e.Migration.Version})
	}
	want := []ev{{EventStart, 1}, {EventDone, 1}, {EventStart, 2}, {EventFailed, 2}}
	if !slices.Equal(got, want) {
		t.Errorf("события: %v, ожидалось %v", got, want)
	}
	if events[3].Err == nil {
		t.Error("EventFailed без ошибки")
	}
}
