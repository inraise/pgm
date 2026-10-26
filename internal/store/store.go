package store

import (
	"context"
	"errors"
	"time"

	"github.com/inraise/pgm/internal/model"
)

const (
	DirectionUp   = "up"
	DirectionDown = "down"

	StatusStarted = "started"
	StatusSuccess = "success"
	StatusFailed  = "failed"
)

var (
	ErrNotLocked     = errors.New("pgm: операция изменения требует удержания блокировки")
	errAlreadyLocked = errors.New("pgm: блокировка уже удерживается этим экземпляром")
)

type AppliedRow struct {
	Version   int64
	Name      string
	Checksum  string
	AppliedAt time.Time
}

type Store interface {
	Lock(ctx context.Context, timeout time.Duration) (unlock func() error, err error)
	EnsureTables(ctx context.Context) error
	Applied(ctx context.Context) ([]AppliedRow, error)
	ApplyUp(ctx context.Context, m model.Migration) error
	ApplyDown(ctx context.Context, m model.Migration) error
	LogStart(ctx context.Context, version int64, direction string) (id int64, err error)
	LogFinish(ctx context.Context, id int64, status, errText string) error
}
