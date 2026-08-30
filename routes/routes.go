package routes

import (
	"events-stocks/controllers/accesspolicies"
	"events-stocks/controllers/auditlogs"
	"events-stocks/controllers/automation"
	"events-stocks/controllers/cache"
	"events-stocks/controllers/clientroles"
	"events-stocks/controllers/clients"
	"events-stocks/controllers/clienttypes"
	"events-stocks/controllers/delivery"
	"events-stocks/controllers/designtemplates"
	"events-stocks/controllers/eventconfig"
	"events-stocks/controllers/eventmembers"
	"events-stocks/controllers/events"
	"events-stocks/controllers/eventsection"
	"events-stocks/controllers/eventtables"
	"events-stocks/controllers/eventtypes"
	"events-stocks/controllers/fonts"
	"events-stocks/controllers/guests"
	"events-stocks/controllers/gueststatuses"
	"events-stocks/controllers/health"
	"events-stocks/controllers/invitations"
	"events-stocks/controllers/moments"
	productmetricsController "events-stocks/controllers/productmetrics"
	"events-stocks/controllers/resources"
	"events-stocks/controllers/sessions"
	"events-stocks/controllers/users"
	productmetricshttp "events-stocks/internal/observability/productmetricshttp"
	"events-stocks/middleware/applicationaccess"
	"events-stocks/middleware/audit"
	"events-stocks/middleware/idempotency"
	"events-stocks/middleware/token"
	"events-stocks/models"
	"events-stocks/utils"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"net/http"
	"strings"
	"time"
)

// publicRateLimiter protege endpoints públicos: 20 req/s por IP, burst 40
func publicRateLimiter() echo.MiddlewareFunc {
	fallback := middleware.NewRateLimiterMemoryStoreWithConfig(
		middleware.RateLimiterMemoryStoreConfig{Rate: 20, Burst: 40, ExpiresIn: 3 * time.Minute},
	)
	return middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: newDistributedRateLimitStore("public", 40, 2*time.Second, false, fallback),
		IdentifierExtractor: func(c echo.Context) (string, error) {
			return c.RealIP(), nil
		},
		DenyHandler: func(c echo.Context, id string, err error) error {
			return utils.Error(c, http.StatusTooManyRequests, "Too many requests, please slow down", "")
		},
	})
}

// sensitiveRateLimiter: ~10 req/min por IP para endpoints de alto valor (RSVP, invitaciones)
func sensitiveRateLimiter() echo.MiddlewareFunc {
	fallback := middleware.NewRateLimiterMemoryStoreWithConfig(
		middleware.RateLimiterMemoryStoreConfig{Rate: 0.17, Burst: 5, ExpiresIn: 5 * time.Minute},
	)
	return middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: newDistributedRateLimitStore("sensitive", 5, time.Minute, true, fallback),
		IdentifierExtractor: func(c echo.Context) (string, error) {
			return c.RealIP(), nil
		},
		DenyHandler: func(c echo.Context, id string, err error) error {
			return utils.Error(c, http.StatusTooManyRequests, "Too many requests, please slow down", "")
		},
	})
}

// githubReviewRateLimiter permits normal pull-request activity and brief
// GitHub redelivery bursts, while keeping an internet-facing webhook from
// monopolizing the local review queue before signature/deduplication checks.
// The delivery identifier is attacker-controlled until the handler validates
// its signature, so rate limiting deliberately uses the connection source.
func githubReviewRateLimiter() echo.MiddlewareFunc {
	fallback := middleware.NewRateLimiterMemoryStoreWithConfig(
		middleware.RateLimiterMemoryStoreConfig{Rate: 2, Burst: 20, ExpiresIn: 5 * time.Minute},
	)
	return middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: newDistributedRateLimitStore("github-review", 20, 500*time.Millisecond, false, fallback),
		IdentifierExtractor: func(c echo.Context) (string, error) {
			return c.RealIP(), nil
		},
		DenyHandler: func(c echo.Context, id string, err error) error {
			return utils.Error(c, http.StatusTooManyRequests, "Too many GitHub review events, please retry", "")
		},
	})
}

// uploadRateLimiter allows short upload bursts from the QR batch uploader.
// Per-event/IP upload quotas are enforced in the moments controller.
func uploadRateLimiter() echo.MiddlewareFunc {
	fallback := middleware.NewRateLimiterMemoryStoreWithConfig(
		middleware.RateLimiterMemoryStoreConfig{Rate: 5, Burst: 30, ExpiresIn: 3 * time.Minute},
	)
	return middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: newDistributedRateLimitStore("upload", 30, 6*time.Second, false, fallback),
		IdentifierExtractor: func(c echo.Context) (string, error) {
			return c.RealIP(), nil
		},
		DenyHandler: func(c echo.Context, id string, err error) error {
			return utils.Error(c, http.StatusTooManyRequests, "Too many upload requests, please slow down", "")
		},
	})
}

func publicAccessCacheControl() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if publicAccessCacheSensitive(c) {
				headers := c.Response().Header()
				headers.Set(echo.HeaderCacheControl, "no-store")
				headers.Set("Pragma", "no-cache")
				headers.Set("Expires", "0")
				appendVaryHeader(headers, utils.HeaderEventAccessToken)
			}
			return next(c)
		}
	}
}

func publicAccessCacheSensitive(c echo.Context) bool {
	switch c.Request().Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
		return true
	}
	if publicAccessContentRequest(c) {
		return true
	}
	if utils.PublicInvitationToken(c) != "" {
		return true
	}
	if utils.PublicEventAccessToken(c) != "" {
		return true
	}
	return utils.PublicPreviewToken(c) != ""
}

func publicAccessContentRequest(c echo.Context) bool {
	path := c.Path()
	if path == "" {
		path = c.Request().URL.Path
	}
	if path == "/api/events/page-spec" || strings.HasSuffix(path, "/page-spec") || strings.HasSuffix(path, "/moments") {
		return true
	}
	if strings.HasPrefix(path, "/api/resources/") {
		return true
	}
	if strings.HasPrefix(path, "/api/events/section/") && strings.HasSuffix(path, "/attendees") {
		return true
	}
	return strings.HasPrefix(path, "/api/invitations/ByToken")
}

func appendVaryHeader(headers http.Header, values ...string) {
	seen := map[string]bool{}
	next := make([]string, 0, len(values))

	for _, headerValue := range headers.Values(echo.HeaderVary) {
		for _, part := range strings.Split(headerValue, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			key := strings.ToLower(part)
			if seen[key] {
				continue
			}
			seen[key] = true
			next = append(next, part)
		}
	}

	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		next = append(next, value)
	}

	if len(next) > 0 {
		headers.Set(echo.HeaderVary, strings.Join(next, ", "))
	}
}

// protectedRateLimiter is more permissive for dashboard traffic.
func protectedRateLimiter() echo.MiddlewareFunc {
	fallback := middleware.NewRateLimiterMemoryStoreWithConfig(
		middleware.RateLimiterMemoryStoreConfig{Rate: 60, Burst: 100, ExpiresIn: 3 * time.Minute},
	)
	return middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: newDistributedRateLimitStore("protected", 100, 2*time.Second, false, fallback),
		IdentifierExtractor: func(c echo.Context) (string, error) {
			return c.RealIP(), nil
		},
		DenyHandler: func(c echo.Context, id string, err error) error {
			return utils.Error(c, http.StatusTooManyRequests, "Too many requests, please slow down", "")
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
	public.Use(token.RequireTenantHost(cfg, "eventiapp"))
	public.Use(productmetricshttp.CaptureTenant("eventiapp"))
	public.Use(publicAccessCacheControl())
	public.Use(publicRateLimiter())

	// Events (Lectura pública)
	public.GET("/events/page-spec", events.GetPageSpec)                     // SDUI: PageSpec por token
	public.GET("/events/section/:sectionId/attendees", guests.GetAttendees) // SDUI: Graduados por sección
	public.GET("/events/phrases", events.ListPhrases)
	public.GET("/events/:identifier/meta", events.GetEventMeta)
	public.GET("/events/:identifier/page-spec", events.GetPageSpecByIdentifier) // SDUI: PageSpec por identifier (vista previa)
	public.GET("/events/:key", events.GetEvents)
	public.POST("/events/:identifier/view", events.TrackView)                  // Incrementa contador de vistas
	public.POST("/events/:identifier/performance", events.TrackPerformance)    // RUM agregado, sin PII
	public.POST("/events/:identifier/verify-access", events.VerifyEventAccess) // Verifica contraseña

	// Public moments: read-only wall endpoint with the public body limit/rate limit.
	public.GET("/events/:identifier/moments", moments.ListPublicMoments)

	// Only the two legacy multipart/form-data routes need a large request body.
	// Presign and confirmation routes carry small JSON documents; keeping their
	// limit low prevents an attacker from making the API buffer 225 MB before a
	// request that ultimately uploads directly from the browser to S3.
	uploadsLimiter := uploadRateLimiter()
	legacyUploadsGroup := e.Group("/api")
	legacyUploadsGroup.Use(middleware.BodyLimit("225M"))
	legacyUploadsGroup.Use(token.RequireTenantHost(cfg, "eventiapp"))
	legacyUploadsGroup.Use(productmetricshttp.CaptureTenant("eventiapp"))
	legacyUploadsGroup.Use(publicAccessCacheControl())
	legacyUploadsGroup.Use(uploadsLimiter)
	legacyUploadsGroup.POST("/events/:identifier/moments", moments.CreatePublicMoment)
	legacyUploadsGroup.POST("/events/:identifier/moments/shared", moments.CreateSharedMoment)

	presignedUploadsGroup := e.Group("/api")
	presignedUploadsGroup.Use(middleware.BodyLimit("2M"))
	presignedUploadsGroup.Use(token.RequireTenantHost(cfg, "eventiapp"))
	presignedUploadsGroup.Use(productmetricshttp.CaptureTenant("eventiapp"))
	presignedUploadsGroup.Use(publicAccessCacheControl())
	presignedUploadsGroup.Use(uploadsLimiter)
	presignedUploadsGroup.POST("/events/:identifier/moments/upload-url", moments.RequestPublicMomentUploadURL)
	presignedUploadsGroup.POST("/events/:identifier/moments/confirm", moments.ConfirmPublicMoment)
	presignedUploadsGroup.POST("/events/:identifier/moments/shared/upload-url", moments.RequestSharedUploadURL)
	presignedUploadsGroup.POST("/events/:identifier/moments/shared/batch-upload-urls", moments.RequestBatchSharedUploadURLs)
	presignedUploadsGroup.POST("/events/:identifier/moments/shared/confirm", moments.ConfirmSharedMoment)
	presignedUploadsGroup.POST("/events/:identifier/moments/shared/multipart/start", moments.StartSharedMultipartUpload)
	presignedUploadsGroup.POST("/events/:identifier/moments/shared/multipart/abort", moments.AbortSharedMultipartUpload)
	presignedUploadsGroup.POST("/events/:identifier/moments/shared/multipart/complete", moments.CompleteSharedMultipartUpload)

	// Resources
	public.GET("/resources/:id", resources.GetResource)
	public.GET("/resources/section/:key", resources.GetResourcesBySectionID)

	// Invitations & RSVP
	public.GET("/invitations/ByToken", invitations.GetInvitationByToken)
	public.GET("/invitations/ByToken/:token", invitations.GetInvitationByToken)
	rsvpGroup := public.Group("/invitations/rsvp")
	rsvpGroup.Use(sensitiveRateLimiter())
	rsvpGroup.POST("", invitations.ConfirmRSVP)

	// The locally operated ITBEM agent reports task completion through this
	// secret-authenticated callback. It is deliberately bound to the ITBEM API
	// hostname and is not part of the EventiApp public surface.
	internalAutomation := e.Group("/api/internal/automation")
	// Unlike dashboard requests, this isolated worker callback has no Cognito
	// middleware to attach the resolved configuration to the Echo context. The
	// automation controller still validates its output reference against that
	// runtime configuration, so attach the same immutable startup config here.
	internalAutomation.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("config", cfg)
			return next(c)
		}
	})
	internalAutomation.Use(token.RequireTenantHost(cfg, "itbem"))
	internalAutomation.PUT("/agents/heartbeat", automation.AgentHeartbeat)
	internalAutomation.PUT("/tasks/:id", automation.Complete)

	// GitHub sends signed pull-request events without an end-user JWT. The
	// handler is disabled unless its independent secret and repository allow
	// list are configured; it freezes a diff into a private review task only.
	githubReviewWebhook := e.Group("/api/internal/github")
	githubReviewWebhook.Use(middleware.BodyLimit("1M"))
	githubReviewWebhook.Use(githubReviewRateLimiter())
	githubReviewWebhook.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("config", cfg)
			return next(c)
		}
	})
	githubReviewWebhook.POST("/pull-request-review", automation.GitHubPullRequestReviewWebhook)

	// ==========================================
	// 🔒 RUTAS PROTEGIDAS (Dashboard / Admins)
	// ==========================================
	protected := e.Group("/api")
	protected.Use(middleware.BodyLimit("25M")) // Permite subir recursos (logos, fuentes, etc.)
	protected.Use(token.Autenticacion(cfg))
	protected.Use(protectedRateLimiter())
	protected.Use(applicationaccess.Require)
	protected.Use(productmetricshttp.Capture)
	protected.Use(idempotency.CriticalMutations)
	protected.Use(audit.Mutations)

	protected.GET("/session", sessions.GetSession)
	protected.POST("/session/organization-context", sessions.IssueOrganizationContext)
	protected.GET("/access-policies/:user_id/:application_code", accesspolicies.Manage)
	protected.PUT("/access-policies/:user_id/:application_code", accesspolicies.Manage)
	protected.GET("/audit-logs", auditlogs.List)
	protected.GET("/metrics/portfolio", productmetricsController.Portfolio)
	protected.POST("/automation/tasks", automation.Create)
	protected.GET("/automation/tasks", automation.List)
	protected.POST("/automation/tasks/:id/cancel", automation.Cancel)
	protected.POST("/automation/tasks/:id/retry-code-review", automation.RetryCodeReview)
	protected.GET("/automation/health", automation.Health)
	protected.GET("/automation/costs", automation.CostOverview)
	protected.GET("/automation/tasks/:id/output", automation.GetOutput)
	protected.GET("/automation/tasks/:id/input", automation.GetInputContent)
	protected.GET("/automation/tasks/:id/result", automation.GetOutputContent)
	protected.GET("/automation/tasks/:id/trace", automation.GetTrace)
	protected.GET("/automation/executions/:id/input", automation.GetExecutionInputContent)
	protected.GET("/automation/executions/:id/result", automation.GetExecutionResultContent)
	protected.GET("/automation/executions/:id/input/download", automation.GetExecutionInputDownload)
	protected.GET("/automation/executions/:id/result/download", automation.GetExecutionResultDownload)
	protected.GET("/automation/tool-executions/:id/report", automation.GetToolExecutionReportContent)
	protected.GET("/automation/tool-executions/:id/report/download", automation.GetToolExecutionReportDownload)
	protected.GET("/automation/tasks/:id/artifacts/:name", automation.GetArtifact)
	protected.POST("/automation/input-upload", automation.CreateInputUploadURL)
	protected.GET("/automation/portfolio", delivery.GetAutomationPortfolio)
	protected.GET("/automation/clients", delivery.ListClients)
	protected.PUT("/automation/clients/:id/profile", delivery.UpsertClientProfile)
	protected.GET("/automation/projects", delivery.ListProjects)
	protected.POST("/automation/projects", delivery.CreateProject)
	protected.GET("/automation/projects/:id", delivery.GetProject)
	protected.GET("/automation/projects/:id/budget", delivery.GetProjectBudget)
	protected.GET("/automation/projects/:id/costs", delivery.GetProjectCostSummary)
	protected.PUT("/automation/projects/:id/budget", delivery.UpdateProjectBudget)
	protected.GET("/automation/projects/:id/publication-readiness", delivery.PublicationReadiness)
	protected.POST("/automation/projects/:id/publication-readiness/verify", delivery.VerifyPublicationReadiness)
	protected.GET("/automation/projects/:id/members", delivery.ListMembers)
	protected.PUT("/automation/projects/:id/members", delivery.UpsertMember)
	protected.POST("/automation/projects/:id/context", delivery.CreateContext)
	protected.PATCH("/automation/projects/:id/context/:sourceID", delivery.UpdateContextMetadata)
	protected.POST("/automation/projects/:id/context/:sourceID/refresh", delivery.RefreshRemoteRepositoryContext)
	protected.POST("/automation/projects/:id/context/:sourceID/refresh-local", delivery.RefreshLocalWorkspaceContext)
	protected.POST("/automation/projects/:id/context/:sourceID/fetch-local-remote", delivery.FetchLocalWorkspaceRemoteRefs)
	protected.GET("/automation/projects/:id/repository-onboardings", delivery.ListRepositoryOnboardings)
	protected.POST("/automation/projects/:id/repository-onboardings/inspect", delivery.InspectRepositoryOnboarding)
	protected.POST("/automation/projects/:id/repository-onboardings/:onboardingID/probes", delivery.ProbeRepositoryOnboarding)
	protected.GET("/automation/projects/:id/repository-onboardings/:onboardingID/probes", delivery.ListRepositoryCapabilityProbes)
	protected.POST("/automation/projects/:id/repository-onboardings/:onboardingID/approve", delivery.ApproveRepositoryOnboarding)
	protected.GET("/automation/projects/:id/vault/revisions", delivery.ListProjectVaultRevisions)
	protected.GET("/automation/projects/:id/delivery-policy/effective", delivery.GetEffectiveProjectPolicy)
	protected.GET("/automation/projects/:id/delivery-policy/revisions", delivery.ListProjectPolicyRevisions)
	protected.POST("/automation/projects/:id/delivery-policy/revisions", delivery.CreateProjectPolicyRevision)
	protected.POST("/automation/projects/:id/delivery-policy/revisions/:revisionID/decisions", delivery.DecideProjectPolicyRevision)
	protected.GET("/automation/projects/:id/requests", delivery.ListRequests)
	protected.POST("/automation/projects/:id/requests", delivery.CreateRequest)
	// A decomposition is only a reviewable proposal. Its explicit apply endpoint
	// is the request-level human gate that materializes small work items.
	protected.GET("/automation/projects/:id/requests/:requestID/decompositions", delivery.ListRequestDecompositions)
	protected.POST("/automation/projects/:id/requests/:requestID/decompositions", delivery.CreateRequestDecomposition)
	protected.POST("/automation/projects/:id/requests/:requestID/decompositions/:decompositionID/apply", delivery.ApplyRequestDecomposition)
	protected.POST("/automation/projects/:id/work-items", delivery.CreateWorkItem)
	protected.GET("/automation/work-items/:id", delivery.GetWorkItem)
	protected.GET("/automation/work-items/:id/stream", delivery.StreamWorkItem)
	protected.GET("/automation/work-items/:id/execution-graph", delivery.GetExecutionGraph)
	protected.GET("/automation/work-items/:id/release-gate/evaluations", delivery.ListReleaseGateEvaluations)
	protected.GET("/automation/work-items/:id/budget", delivery.GetWorkItemBudget)
	protected.PUT("/automation/work-items/:id/budget", delivery.UpdateWorkItemBudget)
	protected.GET("/automation/work-items/:id/plans", delivery.ListPlans)
	protected.POST("/automation/work-items/:id/plans", delivery.CreatePlan)
	protected.POST("/automation/work-items/:id/plans/promote-agent", delivery.PromoteLatestAgentPlan)
	protected.GET("/automation/work-items/:id/change-sets", delivery.ListChangeSets)
	protected.POST("/automation/work-items/:id/change-sets", delivery.CreateChangeSet)
	protected.GET("/automation/work-items/:id/release", delivery.GetRelease)
	protected.PUT("/automation/work-items/:id/release", delivery.UpsertRelease)
	protected.GET("/automation/work-items/:id/release/report", delivery.DownloadReleaseReport)
	protected.POST("/automation/work-items/:id/transitions", delivery.TransitionWorkItem)
	protected.POST("/automation/work-items/:id/agent-runs", delivery.StartAgentRun)
	protected.GET("/automation/work-items/:id/publication-grants", delivery.ListPublicationGrants)
	protected.POST("/automation/work-items/:id/publication-grants", delivery.CreatePublicationGrant)
	protected.POST("/automation/work-items/:id/publication-grants/:grantID/revoke", delivery.RevokePublicationGrant)
	protected.POST("/automation/work-items/:id/evidence", delivery.CreateEvidence)
	protected.GET("/automation/work-items/:id/evidence/:evidenceId/asset", delivery.GetEvidenceAsset)
	protected.POST("/automation/work-items/:id/messages", delivery.CreateMessage)

	// ── Events ────────────────────────────────
	protected.GET("/events/all", events.ListEvents)
	protected.GET("/events", events.ListEvents)
	protected.GET("/events/dashboard", events.GetDashboardOverview)
	protected.GET("/events/notifications", events.GetEventNotifications)
	protected.GET("/events/:id/detail", events.GetEvent)
	protected.POST("/events", events.CreateEvent)
	protected.PUT("/events/:id", events.UpdateEvent)
	protected.DELETE("/events/:id", events.DeleteEvent)
	protected.POST("/events/:id/duplicate", events.DuplicateEvent)
	protected.POST("/events/:id/preview-token", events.IssuePreviewToken)
	protected.POST("/events/:id/cover", events.UploadEventCover)
	protected.POST("/events/covers/backfill", events.BackfillEventCovers)
	protected.DELETE("/events/:id/cover", events.DeleteEventCover)
	protected.POST("/events/:id/repair", events.RepairEvent)

	// ── Event Config (1:1 con Event, mismo ID) ─
	protected.GET("/events/:id/config", eventconfig.GetEventConfig)
	protected.GET("/events/:id/studio-workspace", eventconfig.GetStudioWorkspace)
	protected.PUT("/events/:id/config", eventconfig.UpdateEventConfig)
	protected.GET("/events/:id/analytics", events.GetEventAnalytics)
	protected.GET("/events/:id/invitations", invitations.ListByEvent)
	protected.POST("/invitations/:id/resend", invitations.ResendInvitation)

	// ── Event Sections ────────────────────────
	protected.GET("/events/:id/sections", eventsection.ListSectionsByEvent)
	protected.POST("/events/:id/sections", eventsection.CreateSection)
	protected.PATCH("/events/:id/sections/reorder", eventsection.ReorderSections)
	protected.PUT("/sections/:id", eventsection.UpdateSection)
	protected.DELETE("/sections/:id", eventsection.DeleteSection)

	// ── Seating Tables ───────────────────────
	protected.GET("/events/:id/tables", eventtables.ListTables)
	protected.GET("/events/:id/seating-workspace", eventtables.GetSeatingWorkspace)
	protected.POST("/events/:id/tables", eventtables.CreateTable)
	protected.PUT("/events/:id/tables/assign", eventtables.AssignTables)
	protected.PUT("/events/:id/tables/plan", eventtables.SavePlan)
	protected.PUT("/tables/:id", eventtables.UpdateTable)
	protected.DELETE("/tables/:id", eventtables.DeleteTable)

	// ── Resources ────────────────────────────
	protected.POST("/resources", resources.CreateResource)
	protected.POST("/resources/multiple", resources.UploadMultipleResources)
	protected.GET("/admin/resources/section/:key", resources.GetResourcesBySectionIDAdmin)
	protected.PUT("/resources/:id/content", resources.UpdateFileContent)
	protected.PUT("/resources/:id/replace", resources.ReplaceFile)
	protected.DELETE("/resources/:id", resources.DeleteResource)

	// ── Fonts ─────────────────────────────────
	protected.POST("/fonts/upload", fonts.UploadFonts)

	// ── Guests ────────────────────────────────
	protected.GET("/guests/:key", guests.GetGuests)
	protected.GET("/events/:id/checkin-workspace", guests.GetCheckinWorkspace)
	protected.GET("/events/:id/capabilities", eventmembers.Capabilities)
	protected.GET("/events/:id/members", eventmembers.List)
	protected.PUT("/events/:id/members", eventmembers.Upsert)
	protected.DELETE("/events/:id/members/:user_id", eventmembers.Remove)
	protected.GET("/events/:id/guests/export", guests.ExportGuestsCSV)
	protected.POST("/guests", guests.CreateGuest)
	protected.POST("/guests/batch", guests.CreateGuests) // Batch con invitaciones automáticas
	protected.POST("/guests/:id/rsvp-token", guests.EnsureRSVPToken)
	protected.PUT("/guests/:id", guests.UpdateGuest)
	protected.DELETE("/guests/bulk", guests.BulkDeleteGuests)            // Bulk delete — must be before /:id
	protected.PATCH("/guests/bulk/status", guests.BulkUpdateGuestStatus) // must be before /:id
	protected.DELETE("/guests/:id", guests.DeleteGuest)

	// ── Moments ───────────────────────────────
	protected.GET("/moments", moments.ListMoments)
	protected.POST("/moments/bulk-approve", moments.BulkApproveRejectMoments) // must be before /:id
	protected.GET("/moments/summary", moments.ListMomentSummaries)            // must be before /:id
	protected.GET("/moments/activity", moments.ListMomentActivity)            // combined background processing state
	protected.GET("/moments/in-flight", moments.ListInFlightMoments)
	protected.GET("/moments/reoptimizing", moments.ListReoptimizingMoments)
	protected.PATCH("/moments/reorder", moments.ReorderMoments)
	protected.POST("/moments/batch/reoptimize", moments.BatchReoptimizeMoments)
	protected.POST("/moments/backfill", moments.BackfillMomentVariants)
	protected.DELETE("/moments/bulk", moments.BulkDeleteMoments)
	protected.GET("/moments/:id", moments.GetMoment)
	protected.POST("/moments", moments.CreateMoment)
	protected.PUT("/moments/:id/requeue", moments.RequeueMoment) // must be before /:id plain PUT
	protected.PUT("/moments/:id", moments.UpdateMoment)
	protected.DELETE("/moments/:id", moments.DeleteMoment)

	// ── Clients ───────────────────────────────
	protected.POST("/clients", clients.CreateNewClient)
	protected.GET("/clients", clients.ListMyClients)
	protected.GET("/clients/children", clients.GetMySubClients)
	protected.POST("/clients/invite", clients.InviteUser)
	protected.GET("/clients/members", clients.ListClientMembers)
	protected.POST("/clients/members", clients.CreateClientMember)
	protected.DELETE("/clients/members/:user_id", clients.RemoveMember)
	protected.PUT("/clients/members/:user_id", clients.UpdateMemberRole)
	protected.GET("/clients/:id/member-applications/:user_id", sessions.ListMemberApplications)
	protected.PUT("/clients/:id/member-applications/:user_id/:application_code", sessions.SetMemberApplication)
	protected.GET("/clients/:id", clients.GetClient)
	protected.PUT("/clients/:id", clients.UpdateClient)
	protected.DELETE("/clients/:id", clients.DeleteClient)

	// ── Users ─────────────────────────────────
	protected.GET("/users", users.GetUser)
	protected.PUT("/users", users.UpdateUser)
	protected.DELETE("/users", users.DeleteUser)
	protected.GET("/users/all", users.ListAllUsers)
	protected.GET("/users/:id", users.GetUserDetail)
	protected.GET("/users/:id/clients", users.ListUserClients)
	protected.PUT("/users/:id", users.UpdateUserDetail)
	protected.DELETE("/users/:id", users.DeleteUserDetail)
	protected.PUT("/users/:id/deactivate", users.DeactivateUser)
	protected.PUT("/users/:id/activate", users.ActivateUser)
	protected.PUT("/users/:id/root-level", users.UpdateUserRootLevel)
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
	protected.GET("/catalogs/design-workspace", designtemplates.ListDesignCatalogWorkspace)
	protected.GET("/catalogs/design-templates/:id", designtemplates.GetDesignTemplate)
	protected.GET("/catalogs/color-palettes", designtemplates.ListColorPalettes)
	protected.GET("/catalogs/font-sets", designtemplates.ListFontSets)
	protected.GET("/catalogs/resource-types", resources.ListResourceTypes)
	protected.GET("/catalogs/guest-statuses", gueststatuses.ListGuestStatuses)
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
	internal.PUT("/events/:id/cover/content", events.UpdateEventCoverContent)
}
