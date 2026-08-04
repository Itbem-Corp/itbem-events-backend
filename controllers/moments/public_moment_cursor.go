package moments

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"events-stocks/models"
)

// publicMomentCursor is an opaque, stable continuation token for the public
// wall. Order is required so pagination stays deterministic when timestamps
// collide.
type publicMomentCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
	Order     *int      `json:"order,omitempty"`
}

func encodeCursor(moment models.Moment) string {
	order := moment.Order
	payload, _ := json.Marshal(publicMomentCursor{
		CreatedAt: moment.CreatedAt,
		ID:        moment.ID.String(),
		Order:     &order,
	})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeCursor(raw string) (*publicMomentCursor, error) {
	if raw == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	var cursor publicMomentCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return nil, err
	}
	if cursor.ID == "" || cursor.CreatedAt.IsZero() {
		return nil, fmt.Errorf("cursor is missing required fields")
	}
	if cursor.Order == nil {
		return nil, fmt.Errorf("cursor is missing order; restart pagination")
	}
	return &cursor, nil
}
