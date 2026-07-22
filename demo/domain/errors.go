package domain

import "errors"

var (
	ErrEmptyAggregateName = errors.New("aggregate name cannot be empty")
	ErrEmptyAggregateID   = errors.New("aggregate ID cannot be empty")
	ErrEmptyEventName     = errors.New("event name cannot be empty")
	ErrInvalidVersion     = errors.New("version must be >= 1")
	ErrNotFound           = errors.New("not found")
	ErrConflict           = errors.New("version conflict")
	ErrInvalidInput       = errors.New("invalid input")
)
