package subscription

import (
	"time"

	"github.com/google/uuid"
)

// Event represents an incoming webhook notification event recorded for idempotency and audit.
type Event struct {
	ID          uuid.UUID
	Provider    Provider
	EventID     string
	EventType   string
	Payload     []byte
	ProcessedAt time.Time
}

// NewEvent constructs a new Event with a fresh UUID and UTC timestamp.
func NewEvent(provider Provider, eventID, eventType string, payload []byte) *Event {
	return &Event{
		ID:          uuid.New(),
		Provider:    provider,
		EventID:     eventID,
		EventType:   eventType,
		Payload:     payload,
		ProcessedAt: time.Now().UTC(),
	}
}
