package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"mia-website/backend/internal/config"
	"mia-website/backend/internal/handlers"
	"mia-website/backend/internal/integrations/slackoauth"
	"mia-website/backend/internal/integrations/smtpmail"
	"mia-website/backend/internal/middleware"
	"mia-website/backend/internal/pkg/logger"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/routes"
	"mia-website/backend/internal/services"
	"mia-website/backend/internal/services/briefnotify"
	"mia-website/backend/internal/services/quota"
	"mia-website/backend/internal/services/teamdetection"
	"mia-website/backend/internal/storage"
	"mia-website/backend/internal/worker"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Initialize Logger
	logger.Init(cfg.Environment)
	slog.Info("Starting AI Chief of Staff Backend", "env", cfg.Environment)

	// Connect to database
	if err := repository.Connect(); err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer repository.Close()

	// Initialize Worker Pool
	worker.InitializeGlobalPool(20, 1000)
	defer worker.GlobalPool.Stop()

	// Create Fiber app
	app := fiber.New(fiber.Config{
		// Trust the X-Forwarded-For header populated by the reverse
		// proxy (Azure Container Apps front door, nginx, etc.) so
		// `c.IP()` returns the real client IP. Without this the
		// `user_consents.ip_address` audit column would silently
		// record the proxy IP — useless for KVKK proof requests.
		ProxyHeader: fiber.HeaderXForwardedFor,
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			if e, ok := err.(*fiber.Error); ok {
				code = e.Code
			}
			return c.Status(code).JSON(fiber.Map{
				"error": err.Error(),
			})
		},
	})

	// Middleware
	app.Use(middleware.RequestLogger())
	corsOrigins := cfg.CORSOrigins
	if corsOrigins == "" {
		corsOrigins = "http://localhost:3000"
	}
	app.Use(cors.New(cors.Config{
		AllowOrigins:     corsOrigins,
		AllowCredentials: true,
		AllowHeaders:     "Origin, Content-Type, Accept, Authorization, Cookie, X-OAuth-State",
		ExposeHeaders:    "Set-Cookie, Retry-After, X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset, X-RateLimit-Tier",
	}))

	// Initialize Supabase Storage client (Phase 1 of batch transcription).
	// Nil when env vars aren't set — routes.go skips upload endpoints in that case.
	var storageClient *storage.Client
	if cfg.SupabaseStorageURL != "" && cfg.SupabaseServiceRoleKey != "" {
		c, err := storage.New(cfg.SupabaseStorageURL, cfg.SupabaseServiceRoleKey, cfg.SupabaseAudioBucket)
		if err != nil {
			slog.Warn("Supabase Storage client init failed, upload endpoints disabled", "error", err)
		} else {
			storageClient = c
			slog.Info("Supabase Storage client initialized", "bucket", cfg.SupabaseAudioBucket)
		}
	} else {
		slog.Warn("SUPABASE_STORAGE_URL or SUPABASE_SERVICE_ROLE_KEY not set, upload endpoints disabled")
	}

	// Wire ElevenLabs batch transcription only when API key, webhook ID, webhook secret,
	// and storage client are all explicitly configured. In real-time bot mode (Live WebSocket
	// inside meeting-bot), backend does not need batch webhooks or storage.
	var elevenLabsSvc *services.ElevenLabsBatchService
	if cfg.ElevenLabsAPIKey != "" && cfg.ElevenLabsWebhookID != "" && os.Getenv("ELEVENLABS_WEBHOOK_SECRET") != "" {
		elevenLabsSvc = services.NewElevenLabsBatchService(services.ElevenLabsBatchConfig{
			APIKey:       cfg.ElevenLabsAPIKey,
			WebhookID:    cfg.ElevenLabsWebhookID,
			ModelID:      cfg.ElevenLabsModelID,
			LanguageCode: cfg.ElevenLabsLanguage,
		})
	} else if cfg.ElevenLabsAPIKey != "" {
		slog.Info("ElevenLabs batch webhook credentials not set — batch transcription disabled (using real-time bot STT)")
	}
	var transcriptionSvc *services.TranscriptionService
	if elevenLabsSvc != nil && storageClient != nil {
		transcriptionSvc = services.NewTranscriptionService(elevenLabsSvc, storageClient)
		slog.Info("ElevenLabs batch transcription enabled", "model", cfg.ElevenLabsModelID)
	} else {
		slog.Warn("ElevenLabs batch transcription disabled (missing API key or storage)")
	}

	// Audio cleanup service (Phase 4). Deletes Supabase audio after
	// successful transcription and sweeps stale objects daily via cron.
	var audioCleanupSvc *services.AudioCleanupService
	if storageClient != nil {
		audioCleanupSvc = services.NewAudioCleanupService(storageClient)
		if transcriptionSvc != nil {
			transcriptionSvc.SetAudioCleanup(audioCleanupSvc)
		}
	}

	// Brief delivery unsubscribe signer (Phase 3). Constructed before
	// routes.Setup so the handler can be wired into the public
	// /api/unsubscribe route. Refuses to build when the secret is too
	// short — fail fast at boot rather than mis-sign tokens forever.
	var unsubscribeSigner *briefnotify.UnsubscribeSigner
	if cfg.UnsubscribeSigningSecret != "" {
		signer, err := briefnotify.NewUnsubscribeSigner(cfg.UnsubscribeSigningSecret)
		if err != nil {
			slog.Warn("unsubscribe signer disabled", "error", err)
		} else {
			unsubscribeSigner = signer
			slog.Info("Unsubscribe signer initialised")
		}
	} else {
		slog.Warn("UNSUBSCRIBE_SIGNING_SECRET not set — /api/unsubscribe disabled")
	}

	var unsubscribeHandler *handlers.UnsubscribeHandler
	if unsubscribeSigner != nil {
		unsubscribeHandler = handlers.NewUnsubscribeHandler(unsubscribeSigner, services.NewProfileService())
	}

	// Slack integration (Phase 4 of brief delivery roadmap). All four
	// Slack env vars must be set; any missing one disables the entire
	// integration (handler stays nil → routes not registered). We use
	// the unsubscribe signing secret as the state-signer secret — both
	// are HMAC-SHA256 with 15-30 day lifetimes, reusing the secret
	// saves an env var while payload layouts guarantee no cross-
	// protocol forgery.
	var slackIntegrationHandler *handlers.SlackIntegrationHandler
	if cfg.SlackClientID != "" && cfg.SlackClientSecret != "" && cfg.SlackTokenEncKey != "" && cfg.UnsubscribeSigningSecret != "" {
		slackClient, err := slackoauth.NewClient(cfg.SlackClientID, cfg.SlackClientSecret)
		if err != nil {
			slog.Warn("Slack OAuth client init failed", "error", err)
		} else {
			slackCipher, err := slackoauth.NewTokenCipher(cfg.SlackTokenEncKey)
			if err != nil {
				slog.Warn("Slack token cipher init failed", "error", err)
			} else {
				slackState, err := slackoauth.NewStateSigner(cfg.UnsubscribeSigningSecret)
				if err != nil {
					slog.Warn("Slack state signer init failed", "error", err)
				} else {
					h, err := handlers.NewSlackIntegrationHandler(handlers.SlackIntegrationConfig{
						Repo:             repository.NewSlackIntegrationRepository(),
						Client:           slackClient,
						State:            slackState,
						Cipher:           slackCipher,
						RedirectURI:      cfg.SlackRedirectURI,
						FrontendCallback: cfg.SlackFrontendCallback,
						ProfileEmails:    services.NewProfileService(),
					})
					if err != nil {
						slog.Warn("Slack integration handler wiring failed", "error", err)
					} else {
						slackIntegrationHandler = h
						slog.Info("Slack integration handler registered",
							"redirect_uri", cfg.SlackRedirectURI)
					}
				}
			}
		}
	} else {
		slog.Warn("Slack integration disabled — SLACK_* env vars missing")
	}

	// Phase 6: desktop WebSocket hub + command scheduler.
	//
	// The hub holds userID -> *websocket.Conn in memory. Multi-replica
	// deployments would need a pub/sub fan-out — tracked for post-MVP.
	// Until then, the backend Container App must run with min-replicas=1
	// so the single connection map is the source of truth.
	desktopHub := services.NewDesktopHub()
	desktopRepo := repository.NewDesktopRepository()
	desktopWSHandler := handlers.NewDesktopWSHandler(desktopHub, desktopRepo)

	// Phase 6 (v1.1 team detection): wire the CalendarBackfillService +
	// teamdetection.Service. Phase 7's onboarding handler will consume
	// teamDetectionSvc.Detect synchronously.
	// Phase 6 (v1.1 team detection) + Phase 7 wire-through.
	backfillSvc := services.NewCalendarBackfillService(services.NewCalendarService())
	teamDetectionSvc := teamdetection.NewService(backfillSvc, repository.NewTeamRepository(), nil)

	// Phase 7: admin audit endpoint for brief_deliveries. Stays nil
	// (route unregistered) when ADMIN_API_KEY isn't set so unauthenticated
	// traffic 404s instead of landing on a half-wired handler. The repo
	// is stateless so constructing a fresh instance here (not reusing
	// the Dispatcher's copy wired below) keeps the ordering simple.
	adminBriefDelivery := handlers.NewAdminBriefDeliveryHandlerFromEnv(repository.NewBriefDeliveryRepository())
	if adminBriefDelivery != nil {
		slog.Info("Admin brief-delivery audit endpoint registered")
	}

	// Setup routes
	routes.Setup(app, storageClient, transcriptionSvc, desktopWSHandler, unsubscribeHandler, slackIntegrationHandler, adminBriefDelivery, teamDetectionSvc)

	// Start Meeting Bot Scheduler (runs in background)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	profileService := services.NewProfileService()
	postMeetingService := services.NewPostMeetingService()

	// Phase 7.10 fix 2026-04-20 — always wire the scheduler. The
	// dispatcher picks ACA vs HTTP at construction time (see
	// NewBotDispatcher); the scheduler decides at Start whether it has
	// anything to dispatch. An ACA-only deployment intentionally has no
	// EXTERNAL_BOT_API_URL but still wants meetings to be dispatched.
	scheduler := services.NewSchedulerService(cfg.ExternalBotAPIURL, profileService)
	// Issue #39 — share a single QuotaService between the HTTP middleware
	// (built inside routes.Setup) and both schedulers, so the soft-cap
	// reading uses the same env-loaded plan registry everywhere.
	quotaSvc := quota.NewService(repository.NewUserQuotaRepository())
	scheduler.SetQuotaService(quotaSvc)
	go scheduler.Start(ctx)

	// Brief delivery pipeline (roadmap Phase 1). The dispatcher stays
	// dormant until concrete deliverers for email + Slack are registered
	// in later phases — ScheduleDeliveries with zero deliverers is a
	// no-op, so enabling this wiring now doesn't change user-visible
	// behavior. The scheduler gets a handle so it can fan-out rows
	// after each brief is generated; the worker gets one so notify_brief
	// jobs can be executed through the same Dispatcher instance.
	briefDeliveryRepo := repository.NewBriefDeliveryRepository()
	briefJobRepo := repository.NewBackgroundJobRepository()
	briefPrefsLoader := briefnotify.NewDBProfilePreferenceLoader()
	briefDispatcher := briefnotify.NewDispatcher(briefDeliveryRepo, briefPrefsLoader, briefJobRepo)
	briefContextLoader := briefnotify.NewDBContextLoader(
		repository.NewBriefRepository(),
		repository.NewMeetingRepository(),
	)

	// Phase 5: register the Slack deliverer when Slack integration is
	// wired AND the token cipher is available. Sharing the cipher with
	// the install handler means a single key governs both encryption
	// (install) and decryption (delivery) — no key drift possible.
	if slackIntegrationHandler != nil {
		slackChatClient, err := slackoauth.NewClient(cfg.SlackClientID, cfg.SlackClientSecret)
		if err == nil {
			slackChatCipher, cipherErr := slackoauth.NewTokenCipher(cfg.SlackTokenEncKey)
			if cipherErr == nil {
				slackDeliverer, err := briefnotify.NewSlackBriefDeliverer(
					briefnotify.SlackBriefDelivererConfig{DashboardBaseURL: cfg.FrontendURL},
					repository.NewSlackIntegrationRepository(),
					slackChatClient,
					slackoauth.NewWorkspaceRateLimiter(),
					slackChatCipher,
				)
				if err != nil {
					slog.Warn("Slack brief deliverer init failed", "error", err)
				} else {
					briefDispatcher.Register(slackDeliverer)
					slog.Info("Slack brief deliverer registered")
				}
			}
		}
	}

	// Register email brief deliverer: prefer Cloud Function (brief-notifier) when
	// BRIEF_NOTIFIER_FUNCTION_URL is configured; otherwise fall back to direct SMTP.
	if strings.TrimSpace(cfg.BriefNotifierFunctionURL) != "" {
		cfTransport, err := briefnotify.NewCloudFunctionEmailTransport(
			cfg.BriefNotifierFunctionURL,
			cfg.InternalServiceSharedSecret,
		)
		if err != nil {
			slog.Warn("Cloud Function transport init failed", "error", err)
		} else {
			fromAddr := cfg.BotMailFromAddress
			if fromAddr == "" {
				fromAddr = "brief@mia.local"
			}
			emailDeliverer, err := briefnotify.NewEmailBriefDeliverer(briefnotify.EmailBriefDelivererConfig{
				FromAddress:        fromAddr,
				FromDisplay:        cfg.BotMailFromDisplayName,
				ReplyTo:            cfg.BotMailReplyTo,
				DashboardBaseURL:   cfg.FrontendURL,
				UnsubscribeBaseURL: cfg.FrontendURL,
				UnsubscribeSigner:  unsubscribeSigner,
			}, cfTransport)
			if err != nil {
				slog.Warn("Cloud Function EmailBriefDeliverer init failed", "error", err)
			} else {
				briefDispatcher.Register(emailDeliverer)
				slog.Info("Cloud Function email brief deliverer registered", "url", cfg.BriefNotifierFunctionURL)
			}
		}
	} else if cfg.SMTPHost != "" && cfg.BotMailAppPassword != "" && cfg.BotMailAuthUser != "" && cfg.BotMailFromAddress != "" {
		smtpClient, err := smtpmail.New(smtpmail.Config{
			Host:         cfg.SMTPHost,
			Port:         cfg.SMTPPort,
			AuthUser:     cfg.BotMailAuthUser,
			AuthPassword: cfg.BotMailAppPassword,
		})
		if err != nil {
			slog.Warn("SMTP client init failed — email brief deliveries disabled", "error", err)
		} else {
			emailDeliverer, err := briefnotify.NewEmailBriefDeliverer(briefnotify.EmailBriefDelivererConfig{
				FromAddress:        cfg.BotMailFromAddress,
				FromDisplay:        cfg.BotMailFromDisplayName,
				ReplyTo:            cfg.BotMailReplyTo,
				DashboardBaseURL:   cfg.FrontendURL,
				UnsubscribeBaseURL: "",
				UnsubscribeSigner:  unsubscribeSigner,
			}, smtpmail.NewBriefAdapter(smtpClient))
			if err != nil {
				slog.Warn("EmailBriefDeliverer init failed — email brief deliveries disabled", "error", err)
			} else {
				briefDispatcher.Register(emailDeliverer)
				slog.Info("Email brief deliverer registered",
					"smtp_host", cfg.SMTPHost,
					"from", cfg.BotMailFromAddress)
			}
		}
	} else {
		slog.Warn("SMTP env vars not fully set — email brief deliveries disabled")
	}

	// Start durable DB-backed background job workers (post-meeting processing queue + transcribe jobs + notify_brief)
	backgroundJobWorker := worker.NewBackgroundJobWorker(
		postMeetingService,
		transcriptionSvc,
		briefDispatcher,
		briefContextLoader,
	)
	go backgroundJobWorker.Start(ctx)

	// Start Watchdog Service (Checks for dead bots)
	watchdog := worker.NewWatchdogService()
	go watchdog.Start(ctx)

	// Start Calendar Watch Renewal Job (renews expiring Google Calendar subscriptions)
	go startCalendarWatchRenewalJob(ctx)

	// Start Brief Scheduler (generates pre-meeting briefs + fans out
	// delivery rows through the briefnotify dispatcher).
	briefScheduler := services.NewBriefScheduler().WithDispatcher(briefDispatcher)
	go briefScheduler.Start(ctx)

	// Start Audio Cleanup Cron (Phase 4: daily sweep of stale audio)
	if audioCleanupSvc != nil {
		go audioCleanupSvc.StartCron(ctx, 24*time.Hour)
		slog.Info("Audio cleanup cron started")
	}

	// Quota drift reconciliation cron (issue #39 Faz 5). Reconciles
	// usage_quota_periods.used_usd against SUM(usage_logs.cost_usd)
	// per active cycle once per UTC day; bumps quota_drift_total when
	// it corrects.
	go quota.NewDriftReconciler().StartCron(ctx, 24*time.Hour)
	slog.Info("Quota drift cron started")

	// Start Phase 6 Desktop Meeting Scheduler. Ticks every 30s and pushes
	// start/stop/force_stop commands over the WebSocket hub. Depends on the
	// MeetingRepository + RecordingMethodResolver + DesktopRepository wired
	// above.
	desktopScheduler := services.NewDesktopMeetingScheduler(
		desktopHub,
		desktopRepo,
		repository.NewMeetingRepository(),
		services.NewRecordingMethodService(),
		profileService,
	)
	desktopScheduler.SetQuotaService(quotaSvc)
	go desktopScheduler.Start(ctx)
	slog.Info("Desktop meeting scheduler started")

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start server in goroutine
	go func() {
		log.Printf("Server starting on port %s", cfg.Port)
		if err := app.Listen(":" + cfg.Port); err != nil {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Wait for shutdown signal
	<-sigChan
	log.Println("Shutting down server...")
	cancel()

	if err := app.Shutdown(); err != nil {
		log.Printf("Error during shutdown: %v", err)
	}

	log.Println("Server stopped gracefully")
}

// startCalendarWatchRenewalJob periodically checks for expiring calendar subscriptions and renews them
func startCalendarWatchRenewalJob(ctx context.Context) {
	log.Println("Starting Calendar Watch Renewal Job (checking every 6 hours)")

	calendarWatchService := services.NewCalendarWatchService()

	// Check immediately on startup
	go func() {
		if err := calendarWatchService.RenewExpiringSubscriptions(); err != nil {
			log.Printf("Error renewing calendar subscriptions: %v", err)
		}
	}()

	// Then check every 6 hours
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Calendar Watch Renewal Job stopped")
			return
		case <-ticker.C:
			log.Println("Checking for expiring calendar subscriptions...")
			if err := calendarWatchService.RenewExpiringSubscriptions(); err != nil {
				log.Printf("Error renewing calendar subscriptions: %v", err)
			}
		}
	}
}
