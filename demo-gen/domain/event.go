package domain

import (
	"encoding/json"
	"time"

	"/tmp/demo-gen/eventstore"
)

// Type aliases so domain code imports only this package.
type Event = eventstore.Event
type EventRepository = eventstore.EventRepository

// NewEvent constructs an Event with the given fields.
func NewEvent(aggregateName, aggregateID, eventName string, version int64, data interface{}) (*Event, error) {
	if aggregateName == "" {
		return nil, ErrEmptyAggregateName
	}
	if aggregateID == "" {
		return nil, ErrEmptyAggregateID
	}
	if eventName == "" {
		return nil, ErrEmptyEventName
	}
	if version < 1 {
		return nil, ErrInvalidVersion
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	return &Event{
		AggregateName: aggregateName,
		AggregateID:   aggregateID,
		EventName:     eventName,
		Version:       version,
		Data:          json.RawMessage(jsonData),
		TimeMillis:    time.Now().UnixMilli(),
	}, nil
}
