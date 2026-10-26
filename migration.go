package pgm

import (
	"time"

	"github.com/inraise/pgm/internal/store"
)

const (
	DirectionUp   = store.DirectionUp
	DirectionDown = store.DirectionDown
)

type State string

const (
	StateApplied State = "applied"
	StatePending State = "pending"
	StateChanged State = "changed"
	StateMissing State = "missing"
)

type StatusItem struct {
	Version   int64
	Name      string
	State     State
	AppliedAt time.Time
}

type EventKind int

const (
	EventStart EventKind = iota + 1
	EventDone
	EventFailed
)

type Event struct {
	Kind      EventKind
	Direction string
	Migration Migration
	Duration  time.Duration
	Err       error
}
