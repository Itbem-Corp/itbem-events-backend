package main

import (
	"context"
	"events-stocks/configuration"
	clientsController "events-stocks/controllers/clients"
	eventconfigController "events-stocks/controllers/eventconfig"
	eventsController "events-stocks/controllers/events"
	eventsectionController "events-stocks/controllers/eventsection"
	fontsController "events-stocks/controllers/fonts"
	guestsController "events-stocks/controllers/guests"
	invitationsController "events-stocks/controllers/invitations"
	momentsController "events-stocks/controllers/moments"
	"events-stocks/controllers/resources"
	usersController "events-stocks/controllers/users"
	customValidator "events-stocks/middleware/validator"
	authproviderrepository "events-stocks/repositories/authproviderrepository"
	clientrepository "events-stocks/repositories/clientrepository"
	sqsrepository "events-stocks/repositories/sqsrepository"
	clientrolerepository "events-stocks/repositories/clientrolerepository"
	clienttyperepository "events-stocks/repositories/clienttyperepository"
	eventconfigrepository "events-stocks/repositories/eventconfigrepository"
	eventsrepository "events-stocks/repositories/eventsrepository"
	eventsectionrepository "events-stocks/repositories/eventsectionrepository"
	gormrepository "events-stocks/repositories/gormrepository"
	guestrepository "events-stocks/repositories/guestrepository"
	invitationaccesstokenrepository "events-stocks/repositories/invitationaccesstokenrepository"
	invitationlogrepository "events-stocks/repositories/invitationlogrepository"
	invitationrepository "events-stocks/repositories/invitationrepository"
	momentrepository "events-stocks/repositories/momentrepository"
	redisrepository "events-stocks/repositories/redisrepository"
	userrepository "events-stocks/repositories/userrepository"
	"events-stocks/routes"
	clientsService "events-stocks/services/clients"
	eventsService "events-stocks/services/events"
	guestsService "events-stocks/services/guests"
	invitationsService "events-stocks/services/invitations"
	momentsService "events-stocks/services/moments"
	resourcesService "events-stocks/services/resources"
	usersService "events-stocks/services/users"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg := configuration.LoadConfig()
	configuration.InicializarRedis(cfg)
	configuration.InicializarPostgreSQL(cfg)
	configuration.MigrarModelos()
	configuration.SeedBaseData()
	configuration.InitAwsServices(cfg)
	sqsrepository.Init(cfg.AwsRegion, cfg.S3ClientId, cfg.S3ClientSecret,
		cfg.SQSImageQueueURL, cfg.SQSVideoQueueURL)

	e := echo.New()
	e.HideBanner = true

	// ── Request ID ────────────────────────────────────────────────────────────
	e.Use(middleware.RequestIDWithConfig(middleware.RequestIDConfig{
		Generator: func() string {
			return uuid.Must(uuid.NewV4()).String()
		},
	}))

	// ── Request logger (slog) ─────────────────────────────────────────────────
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)

			status := c.Response().Status
			if err != nil {
				if he, ok := err.(*echo.HTTPError); ok {
					status = he.Code
				} else {
					status = http.StatusInternalServerError
				}
			}

			slog.Info("request",
				"id", c.Response().Header().Get(echo.HeaderXRequestID),
				"method", c.Request().Method,
				"uri", c.Request().RequestURI,
				"status", status,
				"latency_ms", time.Since(start).Milliseconds(),
				"ip", c.RealIP(),
			)
			return err
		}
	})

	// ── Seguridad ──────────────────────────────────────────────────────────────
	e.Use(middleware.Recover())
	e.Use(middleware.SecureWithConfig(middleware.SecureConfig{
		XSSProtection:         "1; mode=block",
		ContentTypeNosniff:    "nosniff",
		XFrameOptions:         "SAMEORIGIN",
		HSTSMaxAge:            31536000,
		HSTSExcludeSubdomains: false,
		HSTSPreloadEnabled:    true,
		ContentSecurityPolicy: "default-src 'self'; frame-ancestors 'self' https://dashboard.eventiapp.com.mx",
	}))
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			h := c.Response().Header()
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
			h.Set("X-Permitted-Cross-Domain-Policies", "none")
			return next(c)
		}
	})
	e.Use(configuration.GetCORSConfig(cfg))

	// ── Body limits se aplican por grupo en routes.go (público=2M, uploads=225M) ──

	// ── Timeout global ────────────────────────────────────────────────────────
	// 5 min para acomodar uploads de video de hasta 200 MB en conexiones lentas.
	e.Use(middleware.TimeoutWithConfig(middleware.TimeoutConfig{
		Timeout: 5 * time.Minute,
	}))

	// ── Compresión ────────────────────────────────────────────────────────────
	e.Use(middleware.GzipWithConfig(middleware.GzipConfig{
		Level:     5,
		MinLength: 1024, // Solo comprime respuestas > 1 KB
	}))

	// ── Validación global ─────────────────────────────────────────────────────
	e.Validator = customValidator.New()

	// ── Composition Root ──────────────────────────────────────────────────────

	// Shared infrastructure
	redisRepo  := redisrepository.NewRedisRepo()
	transactor := gormrepository.NewGormTransactor()

	// Repository instances
	eventsRepo       := eventsrepository.NewEventsRepo()
	eventConfigRepo  := eventconfigrepository.NewEventConfigRepo()
	eventSectionRepo := eventsectionrepository.NewEventSectionRepo()
	guestRepo        := guestrepository.NewGuestRepo()
	invitationRepo   := invitationrepository.NewInvitationRepo()
	accessTokenRepo  := invitationaccesstokenrepository.NewAccessTokenRepo()
	invLogRepo       := invitationlogrepository.NewInvitationLogRepo()
	momentRepo       := momentrepository.NewMomentRepo()
	userRepo         := userrepository.NewUserRepo()
	clientRepo       := clientrepository.NewClientRepo()
	clientRoleRepo   := clientrolerepository.NewClientRoleRepo()
	clientTypeRepo   := clienttyperepository.NewClientTypeRepo()
	authProviderRepo := authproviderrepository.NewAuthProviderRepo()

	// Service instances
	resourceSvc    := resourcesService.NewResourceService(cfg)
	eventSvc       := eventsService.NewEventService(eventsRepo, redisRepo)
	eventConfigSvc := eventsService.NewEventConfigService(eventConfigRepo, redisRepo)
	eventSectSvc   := eventsService.NewEventSectionService(eventSectionRepo, redisRepo)
	guestSvc       := guestsService.NewGuestService(guestRepo, accessTokenRepo, redisRepo, transactor)
	invitationSvc  := invitationsService.NewInvitationService(invitationRepo, guestRepo, accessTokenRepo, invLogRepo, redisRepo)
	momentSvc      := momentsService.NewMomentService(momentRepo, redisRepo)
	userSvc        := usersService.NewUserService(userRepo, authProviderRepo, cfg)
	adminSvc       := usersService.NewAdminUserService(userRepo, clientRepo, authProviderRepo)
	clientSvc      := clientsService.NewClientService(clientRepo, clientRoleRepo, clientTypeRepo, resourceSvc, redisRepo, transactor)

	// Controller init
	resources.InitResourceController(cfg)
	fontsController.InitFontsController(cfg)
	eventsController.InitEventsController(eventSvc)
	eventconfigController.InitEventConfigController(eventConfigSvc)
	eventsectionController.InitEventSectionController(eventSectSvc)
	guestsController.InitGuestsController(guestSvc)
	invitationsController.InitInvitationsController(invitationSvc)
	momentsController.InitMomentsController(momentSvc)
	momentsController.InitPublicMomentsController(accessTokenRepo, resourceSvc)
	usersController.InitUsersController(userSvc, adminSvc)
	clientsController.InitClientsController(clientSvc)

	// Wire package-level functions to the DI instances
	eventsService.SetDefaultEventService(eventSvc)
	eventsService.SetDefaultEventConfigService(eventConfigSvc)
	eventsService.SetDefaultEventSectionService(eventSectSvc)
	guestsService.SetDefaultGuestService(guestSvc)
	invitationsService.SetDefaultInvitationService(invitationSvc)
	momentsService.SetDefaultMomentService(momentSvc)
	usersService.SetDefaultUserService(userSvc)
	clientsService.SetDefaultClientService(clientSvc)

	routes.ConfigurarRutas(e, cfg)

	port := cfg.Port
	if port == "" {
		port = "8080"
	}

	go func() {
		slog.Info("server started", "port", port)
		if err := e.Start(":" + port); err != nil {
			slog.Info("server stopped", "reason", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down server")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil {
		slog.Error("server shutdown error", "error", err)
		os.Exit(1)
	}
	slog.Info("server stopped gracefully")
}
