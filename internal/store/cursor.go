package store

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// deviceCursor identifies a position in the device list's (registered_at,
// id) ordering. id breaks ties between devices registered in the same
// second, which registered_at alone can't guarantee on its own — without it,
// two devices sharing a timestamp could be skipped or repeated across pages.
type deviceCursor struct {
	RegisteredAt time.Time `json:"r"`
	ID           string    `json:"i"`
}

func encodeDeviceCursor(registeredAt time.Time, id string) string {
	raw, _ := json.Marshal(deviceCursor{RegisteredAt: registeredAt.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeDeviceCursor(cursor string) (registeredAt time.Time, id string, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	var c deviceCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return time.Time{}, "", fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	if c.ID == "" || c.RegisteredAt.IsZero() {
		return time.Time{}, "", fmt.Errorf("%w: missing fields", ErrInvalidCursor)
	}
	return c.RegisteredAt, c.ID, nil
}
