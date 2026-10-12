// Package eventclients exposes the EventiApp event-client endpoints: a
// manager assigns external accounts to single events, and organization
// Owner/Admin oversee every grant of their organization. All routes are
// behind EVENT_CLIENT_ACCESS and answer 404 while it is off.
package eventclients

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/internal/products"
	"events-stocks/internal/products/eventiapp"
	"events-stocks/models"
	eventclientsService "events-stocks/services/eventclients"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

var service *eventclientsService.Service

// Init wires the service. It is always called; the flag decides availability.
func Init(svc *eventclientsService.Service) {
	service = svc
}

func disabled(c echo.Context) error {
	return utils.Error(c, http.StatusNotFound, "Función no disponible", "event_client_access_disabled")
}

func invalidRequest(c echo.Context) error {
	return respondDomain(c, eventclientsService.ErrInvalidRequest)
}

func respondDomain(c echo.Context, domain *eventclientsService.Error) error {
	if domain.Data != nil {
		return utils.ErrorWithData(c, domain.Status, domain.Message, domain.Code, domain.Data)
	}
	return utils.Error(c, domain.Status, domain.Message, domain.Code)
}

// respond maps service errors: domain errors keep their Spanish message and
// stable code, authorization failures keep the authz shape, and anything else
// is a generic 500 that does not leak internals.
func respond(c echo.Context, err error) error {
	var domain *eventclientsService.Error
	if errors.As(err, &domain) {
		return respondDomain(c, domain)
	}
	if errors.Is(err, eventclientsService.ErrForbidden) {
		return authz.Respond(c, &authz.Failure{Status: http.StatusForbidden, Message: "Access denied", Detail: "Your organization role cannot perform this action"})
	}
	var failure *authz.Failure
	if errors.As(err, &failure) {
		return authz.Respond(c, failure)
	}
	slog.Error("event client request failed", "request_id", requestID(c), "error", err)
	return utils.Error(c, http.StatusInternalServerError, "No se pudo completar la operación.", "event_client_internal_error")
}

func requestID(c echo.Context) string {
	return c.Response().Header().Get(echo.HeaderXRequestID)
}

func serviceRequest(c echo.Context, actor *models.User) eventclientsService.Request {
	tenant, _ := c.Get("tenant_code").(string)
	return eventclientsService.Request{Actor: actor, RequestID: requestID(c), TenantCode: tenant}
}

// requireEventManager runs flag -> UUID -> authz event:clients:manage.
func requireEventManager(c echo.Context) (*models.User, *models.Event, bool, error) {
	if !eventiapp.EventClientAccessEnabled() {
		return nil, nil, false, disabled(c)
	}
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return nil, nil, false, invalidRequest(c)
	}
	user, event, err := authz.RequireEventCapability(c, eventID, authz.CapabilityEventClientsManage)
	if err != nil {
		return nil, nil, false, authz.Respond(c, err)
	}
	if service == nil {
		return nil, nil, false, utils.Error(c, http.StatusInternalServerError, "No se pudo completar la operación.", "event_client_internal_error")
	}
	return user, event, true, nil
}

func pathUserID(c echo.Context) (uuid.UUID, bool) {
	userID, err := uuid.FromString(c.Param("user_id"))
	return userID, err == nil && userID != uuid.Nil
}

func queryInt(c echo.Context, name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(c.QueryParam(name)))
	if err != nil {
		return fallback
	}
	return value
}

// GET /api/events/:id/clients
func List(c echo.Context) error {
	_, event, ok, err := requireEventManager(c)
	if !ok {
		return err
	}
	clients, err := service.List(event.ID)
	if err != nil {
		return respond(c, err)
	}
	return utils.Success(c, http.StatusOK, "Clientes del evento", clients)
}

// GET /api/events/:id/clients/candidates?search=
func Candidates(c echo.Context) error {
	_, event, ok, err := requireEventManager(c)
	if !ok {
		return err
	}
	candidates, err := service.Candidates(event, c.QueryParam("search"))
	if err != nil {
		return respond(c, err)
	}
	return utils.Success(c, http.StatusOK, "Clientes disponibles", candidates)
}

// POST /api/events/:id/clients
func Create(c echo.Context) error {
	user, event, ok, err := requireEventManager(c)
	if !ok {
		return err
	}
	var body dtos.EventClientCreateRequest
	if err := c.Bind(&body); err != nil {
		return invalidRequest(c)
	}
	response, created, err := service.Create(serviceRequest(c, user), event, body)
	if err != nil {
		return respond(c, err)
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return utils.Success(c, status, "Cliente asignado", response)
}

// DELETE /api/events/:id/clients/:user_id
func Revoke(c echo.Context) error {
	user, event, ok, err := requireEventManager(c)
	if !ok {
		return err
	}
	userID, valid := pathUserID(c)
	if !valid {
		return invalidRequest(c)
	}
	if err := service.Revoke(serviceRequest(c, user), event.ID, userID); err != nil {
		return respond(c, err)
	}
	return utils.Success(c, http.StatusOK, "Acceso retirado", map[string]bool{"revoked": true})
}

// POST /api/events/:id/clients/:user_id/resend-invitation
func ResendInvitation(c echo.Context) error {
	user, event, ok, err := requireEventManager(c)
	if !ok {
		return err
	}
	userID, valid := pathUserID(c)
	if !valid {
		return invalidRequest(c)
	}
	if err := service.ResendInvitation(serviceRequest(c, user), event.ID, userID); err != nil {
		return respond(c, err)
	}
	return utils.Success(c, http.StatusOK, "Invitación reenviada", map[string]bool{"resent": true})
}

// GET /api/events/:id/clients/:user_id/assignments
func ListAssignments(c echo.Context) error {
	user, event, ok, err := requireEventManager(c)
	if !ok {
		return err
	}
	userID, valid := pathUserID(c)
	if !valid {
		return invalidRequest(c)
	}
	page, err := service.ListAssignments(serviceRequest(c, user), event, userID, c.QueryParam("search"), queryInt(c, "page", 1), queryInt(c, "page_size", 20))
	if err != nil {
		return respond(c, err)
	}
	return utils.Success(c, http.StatusOK, "Eventos del cliente", page)
}

// PATCH /api/events/:id/clients/:user_id/assignments
func UpdateAssignments(c echo.Context) error {
	user, event, ok, err := requireEventManager(c)
	if !ok {
		return err
	}
	userID, valid := pathUserID(c)
	if !valid {
		return invalidRequest(c)
	}
	var body dtos.EventClientAssignmentsRequest
	if err := c.Bind(&body); err != nil {
		return invalidRequest(c)
	}
	response, err := service.UpdateAssignments(serviceRequest(c, user), event, userID, body)
	if err != nil {
		return respond(c, err)
	}
	return utils.Success(c, http.StatusOK, "Eventos del cliente actualizados", response)
}

// requireOrganizationOversight runs flag -> events product -> UUID ->
// organization members:manage (Owner/Admin incl. inherited, roots).
func requireOrganizationOversight(c echo.Context) (*models.User, uuid.UUID, bool, error) {
	if !eventiapp.EventClientAccessEnabled() {
		return nil, uuid.Nil, false, disabled(c)
	}
	tenant, _ := c.Get("tenant_code").(string)
	if strings.TrimSpace(tenant) != "" && !products.SupportsEventOperations(tenant) {
		return nil, uuid.Nil, false, disabled(c)
	}
	orgID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return nil, uuid.Nil, false, invalidRequest(c)
	}
	user, err := authz.CurrentUser(c)
	if err != nil {
		return nil, uuid.Nil, false, authz.Respond(c, err)
	}
	if err := authz.RequireClientCapability(user, orgID, authz.CapabilityMembersManage); err != nil {
		return nil, uuid.Nil, false, authz.Respond(c, err)
	}
	if service == nil {
		return nil, uuid.Nil, false, utils.Error(c, http.StatusInternalServerError, "No se pudo completar la operación.", "event_client_internal_error")
	}
	return user, orgID, true, nil
}

// GET /api/clients/:id/members/event-clients
func ListOrganizationGrants(c echo.Context) error {
	_, orgID, ok, err := requireOrganizationOversight(c)
	if !ok {
		return err
	}
	page, err := service.ListOrganizationGrants(orgID, queryInt(c, "page", 1), queryInt(c, "page_size", 20))
	if err != nil {
		return respond(c, err)
	}
	return utils.Success(c, http.StatusOK, "Clientes de eventos de la organización", page)
}

// DELETE /api/clients/:id/members/event-clients/:user_id
func RevokeOrganizationUser(c echo.Context) error {
	user, orgID, ok, err := requireOrganizationOversight(c)
	if !ok {
		return err
	}
	userID, valid := pathUserID(c)
	if !valid {
		return invalidRequest(c)
	}
	count, err := service.RevokeOrganizationUser(serviceRequest(c, user), orgID, userID)
	if err != nil {
		return respond(c, err)
	}
	return utils.Success(c, http.StatusOK, "Accesos retirados", map[string]int64{"revoked": count})
}
