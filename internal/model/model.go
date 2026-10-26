package model

import (
	"errors"
	"fmt"
)

type Migration struct {
	Version  int64
	Name     string
	Up       string
	Down     string
	Checksum string
}

var (
	ErrInvalidSet       = errors.New("pgm: некорректный набор миграций")
	ErrNoDown           = errors.New("pgm: у миграции нет секции down")
	ErrNotConfirmed     = errors.New("pgm: откат требует подтверждения")
	ErrMissingFile      = errors.New("pgm: файл применённой миграции не найден")
	ErrChecksumMismatch = errors.New("pgm: контрольная сумма не совпадает")
	ErrLockTimeout      = errors.New("pgm: не удалось получить блокировку")
	ErrSource           = errors.New("pgm: не удалось прочитать каталог миграций")
	ErrInvalidArgument  = errors.New("pgm: недопустимый аргумент")
)

func (m Migration) Label() string {
	return fmt.Sprintf("%04d_%s", m.Version, m.Name)
}
