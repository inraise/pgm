package pgm

import "github.com/inraise/pgm/internal/model"

type Migration = model.Migration

var (
	ErrInvalidSet       = model.ErrInvalidSet
	ErrNoDown           = model.ErrNoDown
	ErrNotConfirmed     = model.ErrNotConfirmed
	ErrMissingFile      = model.ErrMissingFile
	ErrChecksumMismatch = model.ErrChecksumMismatch
	ErrLockTimeout      = model.ErrLockTimeout
)
