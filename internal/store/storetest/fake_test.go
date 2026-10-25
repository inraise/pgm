package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inraise/pgm/internal/model"
	"github.com/inraise/pgm/internal/store"
)

var ctx = context.Background()

func lockedFake(t *testing.T, applied ...store.AppliedRow) *FakeStore {
	t.Helper()
	f := NewFake(applied...)
	unlock, err := f.Lock(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unlock() })
	return f
}

func TestAtomicity(t *testing.T) {
	f := lockedFake(t)
	f.ApplyUpErr = map[int64]error{2: errors.New("boom")}

	if err := f.ApplyUp(ctx, model.Migration{Version: 1, Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := f.ApplyUp(ctx, model.Migration{Version: 2, Name: "b"}); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	rows, _ := f.Applied(ctx)
	if len(rows) != 1 || rows[0].Version != 1 {
		t.Errorf("после ошибки состояние изменилось: %+v", rows)
	}
}

func TestApplyUpRejectsDuplicate(t *testing.T) {
	f := lockedFake(t)
	m := model.Migration{Version: 1, Name: "a"}
	if err := f.ApplyUp(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := f.ApplyUp(ctx, m); err == nil {
		t.Error("повторное применение версии должно отклоняться")
	}
	if rows, _ := f.Applied(ctx); len(rows) != 1 {
		t.Errorf("записей %d, ожидалась 1", len(rows))
	}
}

func TestApplyDown(t *testing.T) {
	f := lockedFake(t, store.AppliedRow{Version: 1, Name: "a", Checksum: "x"})

	if err := f.ApplyDown(ctx, model.Migration{Version: 1}); !errors.Is(err, model.ErrNoDown) {
		t.Errorf("без секции down: ожидалась ErrNoDown, получено %v", err)
	}
	if err := f.ApplyDown(ctx, model.Migration{Version: 9, Down: "y"}); err == nil {
		t.Error("откат неприменённой версии должен давать ошибку")
	}

	boom := errors.New("boom")
	f.ApplyDownErr = map[int64]error{1: boom}
	if err := f.ApplyDown(ctx, model.Migration{Version: 1, Down: "y"}); !errors.Is(err, boom) {
		t.Errorf("ожидалась внедрённая ошибка, получено %v", err)
	}
	if rows, _ := f.Applied(ctx); len(rows) != 1 {
		t.Error("после ошибки состояние изменилось")
	}

	f.ApplyDownErr = nil
	if err := f.ApplyDown(ctx, model.Migration{Version: 1, Down: "y"}); err != nil {
		t.Fatal(err)
	}
	if rows, _ := f.Applied(ctx); len(rows) != 0 {
		t.Errorf("остались записи: %+v", rows)
	}
}

func TestLogLifecycle(t *testing.T) {
	f := lockedFake(t)
	id, err := f.LogStart(ctx, 1, store.DirectionUp)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.LogFinish(ctx, id, store.StatusFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	log := f.Log()
	if len(log) != 1 || log[0].Status != store.StatusFailed || log[0].ErrText != "boom" {
		t.Errorf("журнал: %+v", log)
	}
	if err := f.LogFinish(ctx, 99, store.StatusSuccess, ""); err == nil {
		t.Error("неизвестная запись журнала должна давать ошибку")
	}
}

func TestLockContentionAndInjectedError(t *testing.T) {
	f := NewFake()
	if _, err := f.Lock(ctx, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Lock(ctx, time.Second); !errors.Is(err, model.ErrLockTimeout) {
		t.Errorf("ожидалась ErrLockTimeout, получено: %v", err)
	}

	boom := errors.New("boom")
	g := NewFake()
	g.LockErr = boom
	if _, err := g.Lock(ctx, time.Second); !errors.Is(err, boom) {
		t.Errorf("ожидалась внедрённая ошибка, получено %v", err)
	}
}

func TestMutationsRequireLock(t *testing.T) {
	f := NewFake()
	m := model.Migration{Version: 1, Up: "x", Down: "y"}
	checks := map[string]error{
		"EnsureTables": f.EnsureTables(ctx),
		"ApplyUp":      f.ApplyUp(ctx, m),
		"ApplyDown":    f.ApplyDown(ctx, m),
		"LogFinish":    f.LogFinish(ctx, 1, store.StatusSuccess, ""),
	}
	_, checks["LogStart"] = f.LogStart(ctx, 1, store.DirectionUp)
	for name, err := range checks {
		if !errors.Is(err, store.ErrNotLocked) {
			t.Errorf("%s без блокировки: ожидалась ErrNotLocked, получено %v", name, err)
		}
	}
}
