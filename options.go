package pgm

import (
	"fmt"
	"log/slog"
	"time"
)

const defaultLockTimeout = 30 * time.Second

type config struct {
	lockTimeout time.Duration
	logger      *slog.Logger
	observer    func(Event)
}

type Option func(*config) error

func WithLockTimeout(d time.Duration) Option {
	return func(c *config) error {
		if d <= 0 {
			return fmt.Errorf("%w: таймаут блокировки должен быть положительным", ErrInvalidArgument)
		}
		c.lockTimeout = d
		return nil
	}
}

func WithLogger(l *slog.Logger) Option {
	return func(c *config) error {
		if l == nil {
			return fmt.Errorf("%w: логгер не задан", ErrInvalidArgument)
		}
		c.logger = l
		return nil
	}
}

func WithObserver(fn func(Event)) Option {
	return func(c *config) error {
		c.observer = fn
		return nil
	}
}
