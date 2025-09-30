package guests

import (
	"context"
	"events-stocks/models"
	"events-stocks/repositories/guestrepository"
	"events-stocks/repositories/invitationaccesstokenrepository"
	"events-stocks/repositories/invitationlogrepository"
	"events-stocks/repositories/invitationrepository"
	"events-stocks/repositories/redisrepository"
	"github.com/gofrs/uuid"
	"time"
)

func CreateGuestsWithInvitations(guests []models.Guest) error {
	if len(guests) == 0 {
		return nil
	}

	now := time.Now()

	// Para controlar unicidad de pretty tokens en memoria
	usedPretty := make(map[string]struct{})

	// 1. Forzar Pending status y preparar invitaciones
	var invitations []models.Invitation
	for i := range guests {
		if guests[i].GuestStatusID == uuid.Nil {
			guests[i].GuestStatusID = guestrepository.GetPendingStatusID()
		}
		if guests[i].Role == "Host" {
			guests[i].IsHost = true
		}

		// Generar ID de invitación y asignarlo al guest
		invID := uuid.Must(uuid.NewV4())
		guests[i].InvitationID = &invID

		invitations = append(invitations, models.Invitation{
			ID:          invID,
			EventID:     guests[i].EventID,
			Type:        "default",
			SubType:     "general",
			EnableEmail: true,
			MaxGuests:   guests[i].MaxGuests,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}

	// 2. Insertar Invitations primero
	if err := invitationrepository.CreateManyInvitations(invitations); err != nil {
		return err
	}

	// 3. Insertar Guests (ya con InvitationID válido)
	if err := guestrepository.CreateGuests(guests); err != nil {
		return err
	}

	// 4. Crear Tokens por cada Invitation
	var tokens []models.InvitationAccessToken
	for _, inv := range invitations {
		// Generar pretty token único
		var pretty string
		for {
			tmp, _ := invitationaccesstokenrepository.GeneratePrettyToken(inv.EventID, 8) // e.g. 6 dígitos
			if _, exists := usedPretty[tmp]; !exists {
				usedPretty[tmp] = struct{}{}
				pretty = tmp
				break
			}
		}

		tokens = append(tokens, models.InvitationAccessToken{
			InvitationID: inv.ID,
			Token:        uuid.Must(uuid.NewV4()).String(),
			PrettyToken:  pretty,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}
	if err := invitationaccesstokenrepository.CreateManyInvitationAccessTokens(tokens); err != nil {
		return err
	}

	// 5. Crear Logs iniciales
	var logs []models.InvitationLog
	for _, inv := range invitations {
		logs = append(logs, models.InvitationLog{
			InvitationID: inv.ID,
			Channel:      "system",
			Action:       "created",
			Status:       "success",
			Response:     "Invitation created automatically with guest",
			Timestamp:    now,
			CreatedAt:    now,
		})
	}
	if err := invitationlogrepository.CreateManyInvitationLogs(logs); err != nil {
		return err
	}

	// 6. Invalidar cache de guests del evento
	eventID := guests[0].EventID
	if eventID != uuid.Nil {
		pattern := "all:" + eventID.String() + ":guests"
		return redisrepository.DeleteKeysByPattern(context.Background(), pattern)
	}

	return nil
}

func CreateGuest(obj *models.Guest) error {
	// Asignar Pending si no viene
	if obj.GuestStatusID == uuid.Nil {
		obj.GuestStatusID = guestrepository.GetPendingStatusID()
	}

	if err := guestrepository.CreateGuest(obj); err != nil {
		return err
	}
	if obj.EventID != uuid.Nil {
		pattern := "all:" + obj.EventID.String() + ":guests"
		return redisrepository.DeleteKeysByPattern(context.Background(), pattern)
	}
	return nil
}

func UpdateGuest(obj *models.Guest) error {
	// Obtener el guest original antes de actualizar
	oldGuest, _ := guestrepository.GetGuestByID(obj.ID)

	// Actualizar el registro
	if err := guestrepository.UpdateGuest(obj); err != nil {
		return err
	}

	// Invalidar cache individual (si lo usas)
	if obj.ID != uuid.Nil {
		_ = redisrepository.Invalidate("guests", obj.ID.String())
	}

	// Invalidar el evento antiguo si cambió de evento
	if oldGuest != nil && oldGuest.EventID != obj.EventID && oldGuest.EventID != uuid.Nil {
		patternOld := "all:" + oldGuest.EventID.String() + ":guests"
		_ = redisrepository.DeleteKeysByPattern(context.Background(), patternOld)
	}

	// Invalidar el evento actual
	if obj.EventID != uuid.Nil {
		patternNew := "all:" + obj.EventID.String() + ":guests"
		_ = redisrepository.DeleteKeysByPattern(context.Background(), patternNew)
	}

	return nil
}

// DELETE /guests/:id/:eventID
func DeleteGuest(id uuid.UUID) error {
	guest, err := guestrepository.GetGuestByID(id)
	if err != nil {
		return err
	}

	if err := guestrepository.DeleteGuest(id); err != nil {
		return err
	}

	if guest.EventID != uuid.Nil {
		pattern := "all:" + guest.EventID.String() + ":guests"
		return redisrepository.DeleteKeysByPattern(context.Background(), pattern)
	}

	return nil
}

func CreateGuests(objs []models.Guest) error {
	if len(objs) == 0 {
		return nil
	}

	// Forzar Pending en todos
	for i := range objs {
		if objs[i].GuestStatusID == uuid.Nil {
			objs[i].GuestStatusID = guestrepository.GetPendingStatusID()
		}
	}

	if err := guestrepository.CreateGuests(objs); err != nil {
		return err
	}

	eventID := objs[0].EventID
	if eventID != uuid.Nil {
		pattern := "all:" + eventID.String() + ":guests"
		return redisrepository.DeleteKeysByPattern(context.Background(), pattern)
	}
	return nil
}
