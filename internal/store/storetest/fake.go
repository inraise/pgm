package storetest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/inraise/pgm/internal/model"
	"github.com/inraise/pgm/internal/store"
)

type LogEntry struct {
	ID        int64
	Version   int64
	Direction string
	Status    string
	ErrText   string
}

type FakeStore struct {
	mu     sync.Mutex
	locked bool
	rows   []store.AppliedRow
	log    []LogEntry
	calls  []string

	LockErr      error
	ApplyUpErr   map[int64]error
	ApplyDownErr map[int64]error
}

var _ store.Store = (*FakeStore)(nil)

func NewFake(applied ...store.AppliedRow) *FakeStore {
	return &FakeStore{rows: append([]store.AppliedRow(nil), applied...)}
}

func (f *FakeStore) record(call string) { f.calls = append(f.calls, call) }

func (f *FakeStore) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *FakeStore) Log() []LogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]LogEntry(nil), f.log...)
}

func (f *FakeStore) Lock(_ context.Context, _ time.Duration) (func() error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Lock")
	if f.LockErr != nil {
		return nil, f.LockErr
	}
	if f.locked {
		return nil, fmt.Errorf("%w: уже удерживается", model.ErrLockTimeout)
	}
	f.locked = true
	return func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.record("Unlock")
		f.locked = false
		return nil
	}, nil
}

func (f *FakeStore) EnsureTables(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("EnsureTables")
	if !f.locked {
		return store.ErrNotLocked
	}
	return nil
}

func (f *FakeStore) Applied(_ context.Context) ([]store.AppliedRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Applied")
	return append([]store.AppliedRow(nil), f.rows...), nil
}

func (f *FakeStore) ApplyUp(_ context.Context, m model.Migration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("ApplyUp %d", m.Version))
	if !f.locked {
		return store.ErrNotLocked
	}
	if err := f.ApplyUpErr[m.Version]; err != nil {
		return err
	}
	for _, r := range f.rows {
		if r.Version == m.Version {
			return fmt.Errorf("версия %d уже применена", m.Version)
		}
	}
	f.rows = append(f.rows, store.AppliedRow{
		Version: m.Version, Name: m.Name, Checksum: m.Checksum, AppliedAt: time.Now(),
	})
	sort.Slice(f.rows, func(i, j int) bool { return f.rows[i].Version < f.rows[j].Version })
	return nil
}

func (f *FakeStore) ApplyDown(_ context.Context, m model.Migration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("ApplyDown %d", m.Version))
	if m.Down == "" {
		return fmt.Errorf("%w: версия %d", model.ErrNoDown, m.Version)
	}
	if !f.locked {
		return store.ErrNotLocked
	}
	if err := f.ApplyDownErr[m.Version]; err != nil {
		return err
	}
	for i, r := range f.rows {
		if r.Version == m.Version {
			f.rows = append(f.rows[:i], f.rows[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("версия %d не найдена", m.Version)
}

func (f *FakeStore) LogStart(_ context.Context, version int64, direction string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := int64(len(f.log) + 1)
	f.record(fmt.Sprintf("LogStart %d %s", version, direction))
	if !f.locked {
		return 0, store.ErrNotLocked
	}
	f.log = append(f.log, LogEntry{ID: id, Version: version, Direction: direction, Status: store.StatusStarted})
	return id, nil
}

func (f *FakeStore) LogFinish(_ context.Context, id int64, status, errText string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("LogFinish %d %s", id, status))
	if !f.locked {
		return store.ErrNotLocked
	}
	for i := range f.log {
		if f.log[i].ID == id {
			f.log[i].Status = status
			f.log[i].ErrText = errText
			return nil
		}
	}
	return fmt.Errorf("запись журнала %d не найдена", id)
}
