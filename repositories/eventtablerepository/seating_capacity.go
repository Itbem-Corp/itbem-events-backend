package eventtablerepository

import (
	"sort"
	"strings"

	"events-stocks/dtos"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SeatingEffectiveStatusSQL is a guest's RSVP status as the seating and
// check-in screens read it: its own rsvp_status, else its GuestStatus code,
// else pending. It needs guest_statuses LEFT JOINed on guests.guest_status_id.
const SeatingEffectiveStatusSQL = `LOWER(BTRIM(COALESCE(NULLIF(BTRIM(guests.rsvp_status), ''), guest_statuses.code, 'pending')))`

// SeatingPartySizeSQL is the number of seats a guest takes at a table. It is
// the one rule shared by the server capacity check, the SeatingGuest
// projection (guests_count) and the dashboard seating screen
// (getGuestPartySize in the dashboard's lib/guest-utils.ts): a declined guest
// takes 0 seats; otherwise the RSVP party size (rsvp_guest_count) when it is
// set, else the invitation's max_guests, else 1. It needs guest_statuses and
// invitations LEFT JOINed.
const SeatingPartySizeSQL = `CASE
	WHEN ` + SeatingEffectiveStatusSQL + ` = 'declined' THEN 0
	WHEN guests.rsvp_guest_count > 0 THEN guests.rsvp_guest_count
	WHEN invitations.max_guests > 0 THEN invitations.max_guests
	ELSE 1
END`

// SeatingPartySize applies SeatingPartySizeSQL to an in-memory guest whose
// status is rsvpStatus (already resolved), RSVP party size rsvpGuestCount and
// invitation max maxGuests.
func SeatingPartySize(rsvpStatus string, rsvpGuestCount, maxGuests int) int {
	switch {
	case strings.EqualFold(strings.TrimSpace(rsvpStatus), "declined"):
		return 0
	case rsvpGuestCount > 0:
		return rsvpGuestCount
	case maxGuests > 0:
		return maxGuests
	default:
		return 1
	}
}

type tableSeats struct {
	TableID  uuid.UUID
	Name     string
	Capacity int
	Seats    int
}

// lockTables takes a row lock on every listed table of the event, in id order
// so two writers never deadlock. Held until the transaction ends, it
// serializes concurrent seating writes on the same table, so the capacity
// count that follows (a new statement under READ COMMITTED) sees every
// committed assignment.
func lockTables(tx *gorm.DB, eventID uuid.UUID, tableIDs []uuid.UUID) error {
	if len(tableIDs) == 0 {
		return nil
	}
	ids := append([]uuid.UUID(nil), tableIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	var locked []uuid.UUID
	return tx.Model(&models.EventTable{}).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("event_id = ? AND id IN ?", eventID, ids).
		Order("id").
		Pluck("id", &locked).Error
}

// seatsByTable counts the seats taken at each listed live table of the event,
// leaving out excludeGuestIDs.
func seatsByTable(tx *gorm.DB, eventID uuid.UUID, tableIDs []uuid.UUID, excludeGuestIDs ...uuid.UUID) ([]tableSeats, error) {
	if len(tableIDs) == 0 {
		return nil, nil
	}
	guestJoin := "LEFT JOIN guests ON guests.table_id = tables.id AND guests.event_id = tables.event_id AND guests.deleted_at IS NULL"
	var args []interface{}
	if len(excludeGuestIDs) > 0 {
		guestJoin += " AND guests.id NOT IN ?"
		args = append(args, excludeGuestIDs)
	}
	var rows []tableSeats
	err := tx.Table("tables").
		Select("tables.id AS table_id, tables.name, tables.capacity, COALESCE(SUM("+SeatingPartySizeSQL+"), 0) AS seats").
		Joins(guestJoin, args...).
		Joins("LEFT JOIN guest_statuses ON guest_statuses.id = guests.guest_status_id").
		Joins("LEFT JOIN invitations ON invitations.id = guests.invitation_id").
		Where("tables.event_id = ? AND tables.id IN ? AND tables.deleted_at IS NULL", eventID, tableIDs).
		Group("tables.id, tables.name, tables.capacity, tables.sort_order, tables.created_at").
		Order("tables.sort_order ASC, tables.created_at ASC").
		Scan(&rows).Error
	return rows, err
}

// checkTableCapacity returns a *dtos.TableOverCapacityError when any listed
// table now seats more than its capacity. Call it inside the write's
// transaction, after the assignments, on tables lockTables locked.
func checkTableCapacity(tx *gorm.DB, eventID uuid.UUID, tableIDs []uuid.UUID) error {
	rows, err := seatsByTable(tx, eventID, tableIDs)
	if err != nil {
		return err
	}
	var over []dtos.TableOverCapacity
	for _, row := range rows {
		if row.Seats > row.Capacity {
			over = append(over, dtos.TableOverCapacity{TableID: row.TableID, Name: row.Name, Capacity: row.Capacity, Seats: row.Seats})
		}
	}
	if len(over) > 0 {
		return &dtos.TableOverCapacityError{Tables: over}
	}
	return nil
}

// guestTableIDs reads the current table of each listed guest of the event.
func guestTableIDs(tx *gorm.DB, eventID uuid.UUID, guestIDs []uuid.UUID) (map[uuid.UUID]*uuid.UUID, error) {
	type row struct {
		ID      uuid.UUID
		TableID *uuid.UUID
	}
	var rows []row
	if err := tx.Model(&models.Guest{}).
		Select("id, table_id").
		Where("event_id = ? AND id IN ?", eventID, guestIDs).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	current := make(map[uuid.UUID]*uuid.UUID, len(rows))
	for _, r := range rows {
		current[r.ID] = r.TableID
	}
	return current, nil
}

// tablesGainingGuests lists the tables an assignment moves at least one guest
// onto (a guest whose current table is different). Only these tables, and the
// ones whose capacity a plan lowers, are capacity-checked, so a legacy table
// that is already over capacity never blocks an unrelated save.
func tablesGainingGuests(current map[uuid.UUID]*uuid.UUID, next map[uuid.UUID]*uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{})
	var ids []uuid.UUID
	for guestID, tableID := range next {
		if tableID == nil {
			continue
		}
		if previous := current[guestID]; previous != nil && *previous == *tableID {
			continue
		}
		if _, ok := seen[*tableID]; ok {
			continue
		}
		seen[*tableID] = struct{}{}
		ids = append(ids, *tableID)
	}
	return ids
}

// CheckSeatsFit checks whether extraSeats more seats fit at tableID, not
// counting the guests in exclude (guests that are being rewritten, so their
// stored seats are not counted twice). It serves the guest writes outside a
// plan: POST /guests, POST /guests/bulk and PUT /guests/:id with a table_id,
// and PUT /tables/:id lowering a capacity (extraSeats 0, newCapacity set).
// newCapacity <= 0 means "use the stored capacity". It returns a
// *dtos.TableOverCapacityError when the seats do not fit, and nil for a
// table that is not a live table of the event (the caller validated it).
//
// It runs outside the guest write's transaction, so two concurrent
// single-guest writes can still race past it; the plan save and bulk assign
// lock the tables and are exact.
func CheckSeatsFit(db *gorm.DB, eventID, tableID uuid.UUID, exclude []uuid.UUID, extraSeats, newCapacity int) error {
	rows, err := seatsByTable(db, eventID, []uuid.UUID{tableID}, exclude...)
	if err != nil || len(rows) == 0 {
		return err
	}
	row := rows[0]
	capacity := row.Capacity
	if newCapacity > 0 {
		capacity = newCapacity
	}
	if row.Seats+extraSeats > capacity {
		return &dtos.TableOverCapacityError{Tables: []dtos.TableOverCapacity{{
			TableID: row.TableID, Name: row.Name, Capacity: capacity, Seats: row.Seats + extraSeats,
		}}}
	}
	return nil
}
