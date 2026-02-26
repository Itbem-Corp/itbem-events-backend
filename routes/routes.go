package routes

import (
	"events-stocks/controllers/cache"
	"events-stocks/controllers/clientroles"
	"events-stocks/controllers/clients"
	"events-stocks/controllers/clienttypes"
	"events-stocks/controllers/designtemplates"
	"events-stocks/controllers/eventconfig"
	"events-stocks/controllers/events"
	"events-stocks/controllers/eventtypes"
	"events-stocks/controllers/eventsection"
	"events-stocks/controllers/fonts"
	"events-stocks/controllers/guests"
	"events-stocks/controllers/health"
	"events-stocks/controllers/invitations"
	"events-stocks/controllers/moments"
	phrasesCtrl "events-stocks/controllers/phrases"
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

// directUploadRateLimiter: for JSON-only direct-upload coordination endpoints.
// A single user uploading 10 files needs up to 20 requests (upload-url + confirm per file).
// Burst=30 covers that with margin; rate=3 req/s allows sustained bulk uploads.
func directUploadRateLimiter() echo.MiddlewareFunc {
	return middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(
			middleware.RateLimiterMemoryStoreConfig{
				Rate:      3,
				Burst:     30,
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
	public.Use(middleware.BodyLimit("2M")) // Protege endpoints públicos no-upload
	public.Use(publicRateLimiter())
	public.Use(redisMiddleware.RetrieveCache)

	// Events (Lectura pública)
	public.GET("/events/page-spec", events.GetPageSpec)                        // SDUI: PageSpec por token
	public.GET("/events/section/:sectionId/attendees", guests.GetAttendees)    // SDUI: Graduados por sección
	public.GET("/events/phrases", phrasesCtrl.GetPhrases)                      // Random phrases by event type
	public.GET("/events/:identifier/page-spec", events.GetPageSpecByIdentifier) // SDUI: PageSpec por identifier (vista previa)
	public.GET("/events/:key", events.GetEvents)
	public.POST("/events/:identifier/view", events.TrackView)                  // Incrementa contador de vistas
	public.POST("/events/:identifier/verify-access", events.VerifyEventAccess) // Verifica contraseña

	// Public moments — view wall (GET only; inherits public 2M limit and Redis cache)
	public.GET("/events/:identifier/moments", moments.ListPublicMoments)

	// Legacy relay upload group — kept only for emergency rollback; the frontend
	// no longer calls these. Remove once direct-upload is confirmed stable in prod.
	// uploadsGroup := e.Group("/api")
	// uploadsGroup.Use(middleware.BodyLimit("225M"))
	// uploadsGroup.Use(sensitiveRateLimiter())
	// uploadsGroup.POST("/events/:identifier/moments", moments.CreatePublicMoment)
	// uploadsGroup.POST("/events/:identifier/moments/shared", moments.CreateSharedMoment)

	// Direct-upload endpoints: browser uploads file bytes directly to S3 (no relay through backend).
	// These are JSON-only (tiny payloads), so 2M body limit is fine.
	// Rate limit is more permissive than sensitiveRateLimiter — a user uploading 10 files
	// needs up to 20 requests (upload-url + confirm × 10). Burst=30, rate=3 req/s.
	directUploadGroup := e.Group("/api")
	directUploadGroup.Use(middleware.BodyLimit("2M"))
	directUploadGroup.Use(directUploadRateLimiter())
	// Shared QR upload (no personal token)
	directUploadGroup.POST("/events/:identifier/moments/shared/upload-url", moments.RequestSharedUploadURL)
	directUploadGroup.POST("/events/:identifier/moments/shared/confirm", moments.ConfirmSharedMoment)
	// Multipart upload coordination (for large videos > 10 MB)
	directUploadGroup.POST("/events/:identifier/moments/shared/multipart/start", moments.RequestMultipartUploadStart)
	directUploadGroup.POST("/events/:identifier/moments/shared/multipart/complete", moments.CompleteMultipartMoment)
	directUploadGroup.POST("/events/:identifier/moments/shared/multipart/abort", moments.AbortMultipartMoment)
	// Personal invitation upload
	directUploadGroup.POST("/events/:identifier/moments/upload-url", moments.RequestPersonalUploadURL)
	directUploadGroup.POST("/events/:identifier/moments/confirm", moments.ConfirmPersonalMoment)

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
	protected.Use(middleware.BodyLimit("25M")) // Permite subir recursos (logos, fuentes, etc.)
	protected.Use(token.Autenticacion(cfg))
	protected.Use(protectedRateLimiter())
	protected.Use(redisMiddleware.RetrieveCache)

	// ── Events ────────────────────────────────
	protected.GET("/events", events.ListEvents)
	protected.POST("/events", events.CreateEvent)
	protected.PUT("/events/:id", events.UpdateEvent)
	protected.DELETE("/events/:id", events.DeleteEvent)
	protected.POST("/events/:id/cover", events.UploadEventCover)
	protected.POST("/events/:id/repair", events.RepairEvent)
	protected.POST("/events/:id/preview-token", moments.CreatePreviewToken)

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
	protected.POST("/guests/batch", guests.CreateGuests)    // Batch con invitaciones automáticas
	protected.PUT("/guests/:id", guests.UpdateGuest)
	protected.DELETE("/guests/bulk", guests.BulkDeleteGuests) // Bulk delete — must be before /:id
	protected.DELETE("/guests/:id", guests.DeleteGuest)

	// ── Moments ───────────────────────────────
	protected.GET("/moments", moments.ListMoments)
	protected.POST("/moments/bulk-approve", moments.BulkApproveRejectMoments) // must be before /:id
	protected.GET("/moments/:id", moments.GetMoment)
	protected.GET("/moments/:id/download", moments.DownloadMomentFile) // proxy S3 file — avoids browser CORS
	protected.POST("/moments", moments.CreateMoment)
	protected.PUT("/moments/:id/requeue", moments.RequeueMoment) // must be before /:id plain PUT
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
	protected.GET("/catalogs/design-templates", designtemplates.ListDesignTemplates)
	protected.GET("/catalogs/design-templates/:id", designtemplates.GetDesignTemplate)
	protected.GET("/catalogs/color-palettes", designtemplates.ListColorPalettes)
	protected.GET("/catalogs/font-sets", designtemplates.ListFontSets)
	protected.GET("/event-types", eventtypes.ListEventTypes)

	// ==========================================
	// 🔑 RUTAS INTERNAS (Lambda callbacks)
	// ==========================================
	// These endpoints are NOT protected by Cognito JWT — they are only
	// accessible with the X-Internal-Secret header validated inside each handler.
	// Keep these at the end to avoid route conflicts.
	internal := e.Group("/api")
	internal.Use(middleware.BodyLimit("2M"))
	internal.PUT("/moments/:id/content", moments.UpdateMomentContent) // Lambda callback: processing done
}
