package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inraise/pgm/internal/model"
)

func TestFakeStoreAtomicity(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	f.ApplyUpErr = map[int64]error{2: errors.New("boom")}

	unlock, err := f.Lock(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

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

func TestFakeStoreLockContention(t *testing.T) {
	f := NewFake()
	if _, err := f.Lock(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Lock(context.Background(), time.Second); !errors.Is(err, model.ErrLockTimeout) {
		t.Errorf("ожидалась ErrLockTimeout, получено: %v", err)
	}
}

func TestFakeStoreRequiresLock(t *testing.T) {
	f := NewFake()
	if err := f.ApplyUp(context.Background(), model.Migration{Version: 1}); !errors.Is(err, errNotLocked) {
		t.Errorf("ожидалась errNotLocked, получено: %v", err)
	}
}
