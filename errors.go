package pgm

import (
	"fmt"

	"github.com/inraise/pgm/internal/model"
)

type Migration = model.Migration

var (
	ErrInvalidSet       = model.ErrInvalidSet
	ErrInvalidArgument  = model.ErrInvalidArgument
	ErrSource           = model.ErrSource
	ErrNoDown           = model.ErrNoDown
	ErrNotConfirmed     = model.ErrNotConfirmed
	ErrMissingFile      = model.ErrMissingFile
	ErrChecksumMismatch = model.ErrChecksumMismatch
	ErrLockTimeout      = model.ErrLockTimeout
)

type MigrationError struct {
	Migration Migration
	Direction string
	Err       error
}

func (e *MigrationError) Error() string {
	return fmt.Sprintf("миграция %s (%s): %v", e.Migration.Label(), e.Direction, e.Err)
}

func (e *MigrationError) Unwrap() error {
	return e.Err
}
