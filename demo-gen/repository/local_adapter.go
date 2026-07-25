package repository

import (
	"context"

	"/tmp/demo-gen/eventstore"
)

// LocalAdapter wraps an embedded eventstore.LocalStore so it satisfies
// the domain.EventRepository contract. It is the sibling of
// EventStoreAdapter (the HTTP-backed implementation); callers cannot
// tell them apart because both implement the same interface.
type LocalAdapter struct {
	store *eventstore.LocalStore
}

// NewLocalAdapter constructs an adapter over the given local store.
func NewLocalAdapter(store *eventstore.LocalStore) *LocalAdapter {
	return &LocalAdapter{store: store}
}

func (a *LocalAdapter) StoreAtomic(ctx context.Context, e eventstore.Event, expectedVersion int64) (eventstore.Event, error) {
	return a.store.StoreAtomic(ctx, e, expectedVersion)
}

func (a *LocalAdapter) Retrieve(ctx context.Context, aggregateID, aggregateName string, afterVersion int64) ([]eventstore.Event, error) {
	return a.store.Retrieve(ctx, aggregateID, aggregateName, afterVersion)
}

func (a *LocalAdapter) FetchAll(ctx context.Context, aggregateNames []string, afterID uint, limit int) ([]eventstore.Event, error) {
	return a.store.FetchAll(ctx, aggregateNames, afterID, limit)
}
