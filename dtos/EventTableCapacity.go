package dtos

import (
	"fmt"
	"strings"

	"github.com/gofrs/uuid"
)

// TableOverCapacity is one table that a seating write would leave with more
// seats taken than its capacity. Seats counts party sizes the way the
// dashboard seating screen does (see eventtablerepository.SeatingPartySizeSQL).
type TableOverCapacity struct {
	TableID  uuid.UUID `json:"table_id"`
	Name     string    `json:"name"`
	Capacity int       `json:"capacity"`
	Seats    int       `json:"seats"`
}

// TableOverCapacityError refuses a seating write (plan save, bulk assign or a
// single guest's table_id) that would overfill one or more tables. Handlers
// answer it with HTTP 409 and code "table_over_capacity".
type TableOverCapacityError struct {
	Tables []TableOverCapacity
}

func (e *TableOverCapacityError) Error() string {
	parts := make([]string, 0, len(e.Tables))
	for _, table := range e.Tables {
		parts = append(parts, fmt.Sprintf("%s (%d/%d)", table.TableID, table.Seats, table.Capacity))
	}
	return "table over capacity: " + strings.Join(parts, ", ")
}

// TableIDs lists the offending tables.
func (e *TableOverCapacityError) TableIDs() []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(e.Tables))
	for _, table := range e.Tables {
		ids = append(ids, table.TableID)
	}
	return ids
}

// TableOverCapacityCode is the stable machine code of the 409.
const TableOverCapacityCode = "table_over_capacity"

// TableOverCapacityResponse is the body of the 409:
//
//	{"status":409,"code":"table_over_capacity","error":"table_over_capacity",
//	 "message":"<Spanish>","table_ids":[...],
//	 "data":{"table_ids":[...],"tables":[{table_id,name,capacity,seats}]}}
type TableOverCapacityResponse struct {
	Status  int    `json:"status"`
	Code    string `json:"code"`
	Error   string `json:"error"`
	Message string `json:"message"`
	// TableIDs repeats data.table_ids at the top level, where the dashboard
	// reads it first.
	TableIDs []uuid.UUID           `json:"table_ids"`
	Data     TableOverCapacityData `json:"data"`
}

type TableOverCapacityData struct {
	TableIDs []uuid.UUID         `json:"table_ids"`
	Tables   []TableOverCapacity `json:"tables"`
}

// NewTableOverCapacityResponse builds the 409 body with a Spanish message that
// names the tables.
func NewTableOverCapacityResponse(err *TableOverCapacityError) TableOverCapacityResponse {
	names := make([]string, 0, len(err.Tables))
	for _, table := range err.Tables {
		label := strings.TrimSpace(table.Name)
		if label == "" {
			label = "sin nombre"
		}
		names = append(names, fmt.Sprintf("«%s» (%d de %d lugares)", label, table.Seats, table.Capacity))
	}
	message := "La mesa no tiene lugar suficiente."
	if len(names) == 1 {
		message = "La mesa " + names[0] + " no tiene lugar suficiente."
	} else if len(names) > 1 {
		message = "Estas mesas no tienen lugar suficiente: " + strings.Join(names, ", ") + "."
	}
	return TableOverCapacityResponse{
		Status:   409,
		Code:     TableOverCapacityCode,
		Error:    TableOverCapacityCode,
		Message:  message,
		TableIDs: err.TableIDs(),
		Data:     TableOverCapacityData{TableIDs: err.TableIDs(), Tables: err.Tables},
	}
}
