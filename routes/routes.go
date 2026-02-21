package routes

import (
	"events-stocks/controllers/cache"
	"events-stocks/controllers/clientroles"
	"events-stocks/controllers/clients"
	"events-stocks/controllers/clienttypes"
	"events-stocks/controllers/eventconfig"
	"events-stocks/controllers/events"
	"events-stocks/controllers/eventsection"
	"events-stocks/controllers/fonts"
	"events-stocks/controllers/guests"
	"events-stocks/controllers/health"
	"events-stocks/controllers/invitations"
	"events-stocks/controllers/moments"
	"events-stocks/controllers/resources"
	"events-stocks/controllers/users"
	redisMiddleware "events-stocks/middleware/redis"
	"events-stocks/middleware/token"
	"events-stocks/models"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"net/http"
	"time"
)

// publicRateLimiter protege endpoints públicos: 20 req/s por IP, burst 40
func publicRateLimiter() echo.MiddlewareFunc {
	return middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(
			middleware.RateLimiterMemoryStoreConfig{
				Rate:      20,
				Burst:     40,
				ExpiresIn: 3 * time.Minute,
			},
		),
		IdentifierExtractor: func(c echo.Context) (string, error) {
			return c.RealIP(), nil
		},
		DenyHandler: func(c echo.Context, id string, err error) error {
			return c.JSON(http.StatusTooManyRequests, map[string]string{
				"message": "Too many requests, please slow down",
			})
		},
	})
}

// sensitiveRateLimiter: ~10 req/min por IP para endpoints de alto valor (RSVP, invitaciones)
func sensitiveRateLimiter() echo.MiddlewareFunc {
	return middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(
			middleware.RateLimiterMemoryStoreConfig{
				Rate:      0.17,
				Burst:     5,
				ExpiresIn: 5 * time.Minute,
			},
		),
		IdentifierExtractor: func(c echo.Context) (string, error) {
			return c.RealIP(), nil
		},
		DenyHandler: func(c echo.Context, id string, err error) error {
			return c.JSON(http.StatusTooManyRequests, map[string]string{
				"message": "Too many requests, please slow down",
			})
		},
	})
}

// protectedRateLimiter: más permisivo para el dashboard: 60 req/s por IP, burst 100
func protectedRateLimiter() echo.MiddlewareFunc {
	return middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(
			middleware.RateLimiterMemoryStoreConfig{
				Rate:      60,
				Burst:     100,
				ExpiresIn: 3 * time.Minute,
			},
		),
		IdentifierExtractor: func(c echo.Context) (string, error) {
			return c.RealIP(), nil
		},
		DenyHandler: func(c echo.Context, id string, err error) error {
			return c.JSON(http.StatusTooManyRequests, map[string]string{
				"message": "Too many requests, please slow down",
			})
		},
	})
}

func ConfigurarRutas(e *echo.Echo, cfg *models.Config) {

	// Health check real (DB + Redis/Valkey)
	e.GET("/health", health.Check)

	// ==========================================
	// 🌍 RUTAS PÚBLICAS (Invitados / Vista)
	// ==========================================
	public := e.Group("/api")
	public.Use(publicRateLimiter())
	public.Use(redisMiddleware.RetrieveCache)

	// Events (Lectura pública)
	public.GET("/events/page-spec", events.GetPageSpec) // SDUI: PageSpec por token
	public.GET("/events/section/:sectionId/attendees", guests.GetAttendees) // SDUI: Graduados por sección
	public.GET("/events/:key", events.GetEvents)

	// Public moments
	public.GET("/events/:identifier/moments", moments.ListPublicMoments)
	momentsUploadGroup := public.Group("/events/:identifier/moments")
	momentsUploadGroup.Use(sensitiveRateLimiter())
	momentsUploadGroup.POST("", moments.CreatePublicMoment)

	// Resources
	public.GET("/resources/:id", resources.GetResource)
	public.GET("/resources/section/:key", resources.GetResourcesBySectionID)

	// Guests
	public.GET("/guests/:key", guests.GetGuests)

	// Invitations & RSVP
	public.GET("/invitations/ByToken/:token", invitations.GetInvitationByToken)
	rsvpGroup := public.Group("/invitations/rsvp")
	rsvpGroup.Use(sensitiveRateLimiter())
	rsvpGroup.POST("", invitations.ConfirmRSVP)

	// ==========================================
	// 🔒 RUTAS PROTEGIDAS (Dashboard / Admins)
	// ==========================================
	protected := e.Group("/api")
	protected.Use(token.Autenticacion(cfg))
	protected.Use(protectedRateLimiter())
	protected.Use(redisMiddleware.RetrieveCache)

	// ── Events ────────────────────────────────
	protected.POST("/events", events.CreateEvent)
	protected.PUT("/events/:id", events.UpdateEvent)
	protected.DELETE("/events/:id", events.DeleteEvent)

	// ── Event Config (1:1 con Event, mismo ID) ─
	protected.GET("/events/:id/config", eventconfig.GetEventConfig)
	protected.PUT("/events/:id/config", eventconfig.UpdateEventConfig)
	protected.GET("/events/:id/analytics", events.GetEventAnalytics)
	protected.GET("/events/:id/invitations", invitations.ListByEvent)
	protected.POST("/invitations/:id/resend", invitations.ResendInvitation)

	// ── Event Sections ────────────────────────
	protected.GET("/events/:id/sections", eventsection.ListSectionsByEvent)
	protected.POST("/events/:id/sections", eventsection.CreateSection)
	protected.PUT("/sections/:id", eventsection.UpdateSection)
	protected.DELETE("/sections/:id", eventsection.DeleteSection)

	// ── Resources ────────────────────────────
	protected.POST("/resources", resources.CreateResource)
	protected.POST("/resources/multiple", resources.UploadMultipleResources)
	protected.PUT("/resources/:id/content", resources.UpdateFileContent)
	protected.PUT("/resources/:id/replace", resources.ReplaceFile)
	protected.DELETE("/resources/:id", resources.DeleteResource)

	// ── Fonts ─────────────────────────────────
	protected.POST("/fonts/upload", fonts.UploadFonts)

	// ── Guests ────────────────────────────────
	protected.POST("/guests", guests.CreateGuest)
	protected.POST("/guests/batch", guests.CreateGuests) // Batch con invitaciones automáticas
	protected.PUT("/guests/:id", guests.UpdateGuest)
	protected.DELETE("/guests/:id", guests.DeleteGuest)

	// ── Moments ───────────────────────────────
	protected.GET("/moments", moments.ListMoments)
	protected.GET("/moments/:id", moments.GetMoment)
	protected.POST("/moments", moments.CreateMoment)
	protected.PUT("/moments/:id", moments.UpdateMoment)
	protected.DELETE("/moments/:id", moments.DeleteMoment)

	// ── Clients ───────────────────────────────
	protected.POST("/clients", clients.CreateNewClient)
	protected.GET("/clients", clients.ListMyClients)
	protected.GET("/clients/children", clients.GetMySubClients)
	protected.GET("/clients/:id", clients.GetClient)
	protected.DELETE("/clients/:id", clients.DeleteClient)
	protected.POST("/clients/invite", clients.InviteUser)
	protected.POST("/clients/members", clients.CreateClientMember)
	protected.PUT("/clients/:id", clients.UpdateClient)
	protected.GET("/clients/members", clients.ListClientMembers)
	protected.DELETE("/clients/members/:user_id", clients.RemoveMember)
	protected.PUT("/clients/members/:user_id", clients.UpdateMemberRole)

	// ── Users ─────────────────────────────────
	protected.GET("/users", users.GetUser)
	protected.PUT("/users", users.UpdateUser)
	protected.DELETE("/users", users.DeleteUser)
	protected.GET("/users/all", users.ListAllUsers)
	protected.GET("/users/:id", users.GetUserDetail)
	protected.GET("/users/:id/clients", users.ListUserClients)
	protected.PUT("/users/:id/deactivate", users.DeactivateUser)
	protected.PUT("/users/:id/activate", users.ActivateUser)
	inviteGroup := protected.Group("/users/invite")
	inviteGroup.Use(sensitiveRateLimiter())
	inviteGroup.POST("", users.InviteUser)
	protected.POST("/users/avatar", users.UploadAvatar)
	protected.DELETE("/users/avatar", users.DeleteAvatar)

	// ── Cache ─────────────────────────────────
	protected.GET("/cache/flush/:key", cache.FlushKey)
	protected.GET("/cache/flush-all", cache.FlushAll)

	// ── Catálogos ─────────────────────────────
	protected.GET("/catalogs/client-types", clienttypes.ListClientTypes)
	protected.GET("/catalogs/roles", clientroles.ListClientRoles)
}
