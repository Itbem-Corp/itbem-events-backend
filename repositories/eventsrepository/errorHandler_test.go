package eventsrepository

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateErrorMapsEventUniqueViolations(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		match error
	}{
		{"live name index", errors.New(`ERROR: duplicate key value violates unique constraint "idx_events_client_name_live" (SQLSTATE 23505)`), ErrEventNameAlreadyExists},
		{"legacy name index", errors.New(`ERROR: duplicate key value violates unique constraint "idx_events_name" (SQLSTATE 23505)`), ErrEventNameAlreadyExists},
		{"live identifier index", errors.New(`ERROR: duplicate key value violates unique constraint "idx_events_identifier_live" (SQLSTATE 23505)`), ErrEventIdentifierAlreadyExists},
		{"legacy identifier index", errors.New(`ERROR: duplicate key value violates unique constraint "idx_events_identifier" (SQLSTATE 23505)`), ErrEventIdentifierAlreadyExists},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.ErrorIs(t, ValidateError(tc.err), tc.match)
		})
	}

	other := errors.New(`ERROR: insert or update on table "events" violates foreign key constraint "fk_events_event_type" (SQLSTATE 23503)`)
	assert.Same(t, other, ValidateError(other))
	assert.NoError(t, ValidateError(nil))
}
