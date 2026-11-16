package routes

import (
	"os"
	"time"

	"mia-website/backend/internal/handlers"
	"mia-website/backend/internal/middleware"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services"
	"mia-website/backend/internal/services/quota"
	"mia-website/backend/internal/services/teamdetection"
	"mia-website/backend/internal/storage"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func authRateLimiter(max int, window time.Duration) fiber.Handler {
	return limiter.New(limiter.Config{
		Max:        max,
		Expiration: window,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error": "Too many requests, please try again later",
				"code":  "RATE_LIMITED",
			})
		},
	})
}

func disabledAuthHandler(c *fiber.Ctx) error {
	return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
		"code":  "PASSWORD_AUTH_DISABLED",
		"error": "Password-based authentication is temporarily disabled. Please sign in with Google.",
	})
}

// Setup configures all application routes.
// storageClient may be nil — upload endpoints are skipped in that case so a
// misconfigured deployment fails early rather than at upload time.
// transcriptionSvc may be nil (ElevenLabs key not set); the webhook endpoint
// is skipped so unauthenticated traffic is refused outright.
// desktopWS may be nil in environments without WebSocket wiring (older test
// harnesses, mostly); the /ws/desktop route is skipped so it 404s cleanly.
func Setup(
	app *fiber.App,
	storageClient *storage.Client,
	transcriptionSvc *services.TranscriptionService,
	desktopWS *handlers.DesktopWSHandler,
	unsubscribeHandler *handlers.UnsubscribeHandler,
	slackIntegrationHandler *handlers.SlackIntegrationHandler,
	adminBriefDelivery *handlers.AdminBriefDeliveryHandler,
	teamDetectionSvc teamdetection.Detector, // Phase 7 (D-24): consumed by TeamsHandler.Detect
) {
	// Health check
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status": "ok",
		})
	})

	// Prometheus metrics endpoint (Phase 8.3). Public by design — ACA env
	// firewall should gate external reachability. promhttp.Handler serves
	// the default registerer, which the metrics package auto-registers
	// against.
	app.Get("/metrics", adaptor.HTTPHandler(promhttp.Handler()))

	// Initialize handlers
	calendarHandler := handlers.NewCalendarHandler()
	meetingHandler := handlers.NewMeetingHandler()
	profileHandler := handlers.NewProfileHandler()
	// Phase 2.4 B7: wire the WS hub into the handlers that change
	// effective_method so they can publish `invalidate` messages.
	// `desktopWS` may be nil in older test harnesses — Hub() returns
	// nil and the helper is a no-op in that case.
	if desktopWS != nil {
		hub := desktopWS.Hub()
		meetingHandler.SetDesktopHub(hub)
		profileHandler.SetDesktopHub(hub)
	}
	authHandler := handlers.NewAuthHandler()
	botHandler := handlers.NewBotHandler()
	webhookHandler := handlers.NewWebhookHandler()
	usageHandler := handlers.NewUsageHandler()
	decisionHandler := handlers.NewDecisionHandler()
	noteHandler := handlers.NewNoteHandler()
	briefHandler := handlers.NewBriefHandler()
	chainHandler := handlers.NewChainHandler()
	flowHandler := handlers.NewFlowHandler()
	metricsHandler := handlers.NewMetricsHandler()
	// Phase 7 (v1.1): TeamsHandler owns POST/GET/DELETE under /api/teams.
	// detectSvc may be nil in older test harnesses; the Detect endpoint
	// will panic on nil deref at request time — production wires it
	// from cmd/server/main.go.
	teamsHandler := handlers.NewTeamsHandler(repository.NewTeamRepository(), teamDetectionSvc)
	consentHandler := handlers.NewConsentHandler()

	// Quota middleware chain for user-initiated cost-bearing endpoints.
	// Constructed once and reused across the routes that gate cost:
	// the abuse limiter caps per-user QPS so a 429'd client can't lean
	// on Retry-After to keep hammering, and RequireQuota enforces the
	// soft monthly cap defined in docs/quota-limits-roadmap.md.
	quotaSvc := quota.NewService(repository.NewUserQuotaRepository())
	quotaAbuse := middleware.QuotaAbuseLimiter()
	requireQuota := middleware.RequireQuota(quotaSvc)

	// Bot completion webhook (machine-to-machine, requires X-Bot-Api-Key)
	app.Post("/api/webhooks/bot-completion", middleware.BotAPIKeyMiddleware(), webhookHandler.BotCompletionWebhook)

	// Phase 6: Desktop WebSocket command channel. Registered outside the
	// JWT-protected /api group because the handshake carries its JWT in a
	// query param (cross-origin browsers can't send Authorization headers
	// on WebSocket upgrades).
	if desktopWS != nil {
		app.Get("/ws/desktop", desktopWS.UpgradeMiddleware, desktopWS.Handle)
	}

	// ElevenLabs batch STT webhook (Phase 3). Registered only when the
	// transcription service is wired; otherwise the endpoint 404s so
	// unauthenticated traffic has nowhere to land.
	if transcriptionSvc != nil {
		elWebhook := handlers.NewElevenLabsWebhookHandler(transcriptionSvc)
		app.Post("/api/webhooks/elevenlabs", elWebhook.HandleWebhook)
	}

	// API routes
	api := app.Group("/api")

	// Webhook routes (public)
	api.Post("/webhooks/google-calendar", webhookHandler.GoogleCalendarWebhook)

	// Public unsubscribe endpoint (brief mail footer).
	// Rate-limited per IP so a leaked signing-secret scare can't be
	// turned into a bulk opt-out attack against our user base.
	if unsubscribeHandler != nil {
		unsubLimit := authRateLimiter(30, time.Minute)
		api.Get("/unsubscribe", unsubLimit, unsubscribeHandler.Unsubscribe)
	}

	// Slack OAuth callback (public — Slack POSTs here after the user
	// consents). State param carries the userID, signed with a secret
	// only the backend knows, so the callback can't be spoofed.
	if slackIntegrationHandler != nil {
		slackLimit := authRateLimiter(30, time.Minute)
		api.Get("/integrations/slack/callback", slackLimit, slackIntegrationHandler.Callback)
	}

	// Admin brief delivery audit endpoint (ops tooling). Registered on
	// the public /api group because ops users don't carry a JWT — auth
	// is a bearer key matched by the handler itself. Rate-limited to
	// disincentivise scripted enumeration if the key leaks.
	if adminBriefDelivery != nil {
		adminLimit := authRateLimiter(60, time.Minute)
		api.Get("/admin/brief-deliveries", adminLimit, adminBriefDelivery.List)
	}

	// Admin user-quota endpoint (issue #39 Faz 5). Same ADMIN_API_KEY
	// gating + per-IP rate limit pattern as brief-deliveries. Skipped
	// when the key isn't set so unauthenticated traffic 404s.
	if adminUserQuota := handlers.NewAdminUserQuotaHandlerFromEnv(
		repository.NewUserQuotaRepository(), quotaSvc); adminUserQuota != nil {
		adminLimit := authRateLimiter(60, time.Minute)
		api.Get("/admin/users/:id/quota", adminLimit, adminUserQuota.Get)
		api.Patch("/admin/users/:id/quota", adminLimit, adminUserQuota.Patch)
	}

	// Public auth routes (rate limited per IP to prevent brute force / credential stuffing)
	strictLimit := authRateLimiter(5, time.Minute)   // signup, forgot, reset
	loginLimit := authRateLimiter(10, time.Minute)   // signin
	refreshLimit := authRateLimiter(60, time.Minute) // refresh, logout (higher since legit traffic)

	passwordAuthDisabled := os.Getenv("DISABLE_PASSWORD_AUTH") == "true"
	if passwordAuthDisabled {
		api.Post("/auth/signup", strictLimit, disabledAuthHandler)
		api.Post("/auth/signin", loginLimit, disabledAuthHandler)
		api.Post("/auth/forgot-password", strictLimit, disabledAuthHandler)
		api.Post("/auth/reset-password", strictLimit, disabledAuthHandler)
	} else {
		api.Post("/auth/signup", strictLimit, authHandler.SignUp)
		api.Post("/auth/signin", loginLimit, authHandler.SignIn)
		api.Post("/auth/forgot-password", strictLimit, authHandler.RequestPasswordReset)
		api.Post("/auth/reset-password", strictLimit, authHandler.ResetPassword)
	}
	api.Post("/auth/refresh", refreshLimit, authHandler.RefreshToken)
	api.Post("/auth/logout", refreshLimit, authHandler.Logout)

	// OAuth routes (public, rate limited)
	oauthLimit := authRateLimiter(20, time.Minute)
	api.Get("/auth/google", oauthLimit, authHandler.GetGoogleAuthURL)
	api.Get("/auth/google/callback", oauthLimit, authHandler.GoogleCallback)
	api.Post("/auth/google/desktop", oauthLimit, authHandler.GoogleDesktopSignIn)

	// Bot Routes (Machine-to-Machine, require X-Bot-Api-Key).
	// Per-route middleware (NOT a group) to avoid intercepting
	// other /meetings/* routes registered on the protected group below.
	botAuth := middleware.BotAPIKeyMiddleware()
	api.Post("/meetings/:id/heartbeat", botAuth, botHandler.Heartbeat)
	api.Post("/meetings/:id/events", botAuth, botHandler.HandleBotEvent)

	// Protected routes
	protected := api.Group("", middleware.AuthMiddleware())
	{
		// Auth routes
		protected.Get("/auth/me", authHandler.GetCurrentUser)
		protected.Post("/auth/google-token", authHandler.SaveGoogleToken)
		protected.Get("/auth/google-refresh-token", authHandler.GetGoogleRefreshToken)
		protected.Post("/auth/google-disconnect", authHandler.DisconnectGoogleCalendar)
		protected.Get("/auth/google/connect", authHandler.GetCalendarConnectionURL)
		protected.Get("/auth/calendar-status", authHandler.CheckCalendarConnection)

		// Calendar routes
		protected.Post("/sync-calendar", calendarHandler.SyncCalendar)
		protected.Post("/calendar/watch", calendarHandler.StartCalendarWatch)
		protected.Delete("/calendar/watch", calendarHandler.StopCalendarWatch)
		// Phase 2.4 B6: desktop recorder candidate-queue feed.
		protected.Get("/calendar/upcoming", calendarHandler.GetUpcoming)

		// Meeting routes
		protected.Get("/meetings", meetingHandler.GetMeetings)
		// Desktop recorder ad-hoc session create — registered BEFORE
		// `/meetings/:id` so the static path wins matching against the
		// dynamic param route below.
		protected.Post("/meetings/desktop", quotaAbuse, requireQuota, meetingHandler.CreateDesktopMeeting)
		// Desktop-only end-time PATCH. Dedicated endpoint so EditMeeting's
		// Google Calendar side effects can never reach a desktop synthetic
		// meeting. Refuses calendar meetings server-side.
		protected.Patch("/meetings/:id/desktop-end", meetingHandler.UpdateDesktopEndTime)
		protected.Get("/meetings/:id", meetingHandler.GetMeetingDetail)
		protected.Post("/meetings", meetingHandler.CreateMeeting)
		protected.Patch("/meetings/:id/respond", meetingHandler.RespondMeeting)
		protected.Patch("/meetings/:id", meetingHandler.EditMeeting)
		protected.Post("/meetings/:id/join-agent", quotaAbuse, requireQuota, meetingHandler.JoinMeetingWithAgent)
		protected.Delete("/meetings/:id", meetingHandler.CancelMeeting)
		protected.Get("/meetings/:id/full-transcript", meetingHandler.GetFullTranscript)
		protected.Get("/meetings/:id/usage", usageHandler.GetMeetingUsage)

		// Decision items routes
		protected.Get("/meetings/:id/decision-items", decisionHandler.GetMeetingDecisionItems)
		protected.Patch("/decision-items/:id", decisionHandler.UpdateItem)
		protected.Post("/decision-items/:id/complete", decisionHandler.CompleteItem)
		protected.Post("/decision-items/:id/dismiss", decisionHandler.DismissItem)
		protected.Delete("/decision-items/:id", decisionHandler.DeleteItem)
		protected.Post("/meetings/:id/extract", quotaAbuse, requireQuota, decisionHandler.RetryExtraction)

		// Meeting notes routes
		protected.Get("/meetings/:id/notes", noteHandler.GetMeetingNote)
		protected.Post("/meetings/:id/notes", noteHandler.UpsertMeetingNote)

		// Consent management routes (issue #41 — KVKK/GDPR).
		// Static paths registered before any potential dynamic ones so
		// matching is unambiguous.
		protected.Post("/consents", consentHandler.RecordConsents)
		protected.Get("/consents", consentHandler.ListConsents)
		protected.Get("/consents/status", consentHandler.GetStatus)
		protected.Post("/consents/revoke", consentHandler.RevokeConsent)

		// Chain management routes
		protected.Patch("/meetings/:id/chain", chainHandler.UpdateMeetingChain)

		// Chain list — backs the move-to-chain picker. Returns the user's
		// chains ordered by most-recent meeting first.
		protected.Get("/chains", chainHandler.ListChains)

		// Chain suggestion inbox (Phase 3 — chain detection v3).
		// Empty list until Phase 4 starts writing pending suggestions
		// from the metadata LLM fallback band; endpoints are wired now
		// so the frontend inbox UI has a stable contract to build against.
		protected.Get("/chain-suggestions", chainHandler.ListChainSuggestions)
		protected.Post("/chain-suggestions/:id/accept", chainHandler.AcceptChainSuggestion)
		protected.Post("/chain-suggestions/:id/decline", chainHandler.DeclineChainSuggestion)

		// Per-meeting recording method override (Phase 5: bot | desktop | null-to-clear)
		protected.Patch("/meetings/:id/recording-method", meetingHandler.UpdateRecordingMethod)

		// Flow visualization routes
		protected.Get("/flows/data", flowHandler.GetFlowData)

		// Brief routes
		protected.Get("/briefs/upcoming", briefHandler.GetUpcomingBriefs)
		protected.Get("/briefs/:meeting_id", briefHandler.GetBriefDetail)
		protected.Post("/briefs/:meeting_id/feedback", briefHandler.SubmitFeedback)

		// Bot routes
		protected.Post("/meetings/:id/join", quotaAbuse, requireQuota, botHandler.JoinMeeting)
		protected.Patch("/meetings/:id/bot", botHandler.ToggleBot)

		// Profile routes
		protected.Get("/profile", profileHandler.GetProfile)
		// PATCH is the Phase 5 roadmap verb; PUT stays for backwards
		// compatibility with the older client that still issues PUT.
		protected.Put("/profile", profileHandler.UpdateProfile)
		protected.Patch("/profile", profileHandler.UpdateProfile)
		// Brief delivery preferences — narrow endpoint for per-channel
		// toggles. Registered on a distinct path to avoid colliding
		// with the above PATCH /profile catch-all.
		protected.Patch("/profile/notifications", profileHandler.UpdateNotificationPreferences)

		// Phase 7 (v1.1): Team detection + save + Teams page (D-24).
		// All five endpoints sit under the existing JWT-protected group;
		// the handler reads userID from c.Locals (set by AuthMiddleware).
		protected.Post("/teams/detect", teamsHandler.Detect)
		protected.Post("/teams/save", teamsHandler.Save)
		protected.Post("/teams/skip", teamsHandler.Skip)
		protected.Get("/teams", teamsHandler.List)
		protected.Delete("/teams/members/:id", teamsHandler.DeleteMember)

		// Usage routes
		protected.Get("/usage/summary", usageHandler.GetUserUsageSummary)
		// Quota görünürlüğü (issue #39). Sadece yüzde + reset tarihi
		// döner; dolar sızdırmaz — UI bu endpoint'i polling'le tüketir.
		protected.Get("/usage/me", usageHandler.GetMyQuota)

		// Unsubscribe restore — JWT-protected companion of the public
		// GET /api/unsubscribe. Lets a signed-in user undo the opt-out
		// from /settings/notifications.
		if unsubscribeHandler != nil {
			protected.Post("/unsubscribe/restore", unsubscribeHandler.Restore)
		}

		// Slack integration management (all JWT-protected except
		// /callback which lives on the public api group above).
		if slackIntegrationHandler != nil {
			protected.Get("/integrations/slack/install", slackIntegrationHandler.Install)
			protected.Post("/integrations/slack/resolve-email", slackIntegrationHandler.ResolveEmail)
			protected.Post("/integrations/slack/disconnect", slackIntegrationHandler.Disconnect)
			protected.Get("/integrations/slack/status", slackIntegrationHandler.Status)
		}

		// Internal metrics (admin/pilot)
		protected.Get("/admin/metrics", metricsHandler.GetMVPMetrics)
	}

	// Audio upload endpoints (Phase 2: batch transcription).
	// Registered only when Supabase Storage is configured so the rest of the
	// app can still run without the upload feature.
	if storageClient != nil {
		uploadHandler := handlers.NewUploadHandler(storageClient)

		// User-facing (JWT) — recorder + ad-hoc upload
		protected.Post("/meetings/:id/upload-manifest", uploadHandler.UploadManifest)
		protected.Post("/meetings/:id/upload-audio-chunk", uploadHandler.UploadAudioChunk)
		protected.Post("/meetings/:id/upload-audio", uploadHandler.UploadAudio)
		protected.Post("/meetings/:id/upload-complete", uploadHandler.UploadComplete)
		protected.Get("/meetings/:id/signed-url", uploadHandler.SignedURL)

		// Bot-facing (X-Bot-Api-Key)
		api.Post("/meetings/:id/bot-upload-chunk", botAuth, uploadHandler.BotUploadAudioChunk)
		api.Post("/meetings/:id/bot-upload-complete", botAuth, uploadHandler.BotUploadComplete)
	}
}
