// Package metrics exposes Prometheus counters/histograms for the batch
// transcription pipeline. Wired into the Scribe submit path, the
// ElevenLabs webhook handler, the bot/desktop upload chunk path, the
// background worker's retry-exhaust branch, and the audio cleanup
// service.
//
// Metrics are registered against the default prometheus.DefaultRegisterer
// at package init so any handler hitting promhttp.Handler() via
// /metrics will surface them without extra wiring. Tests that need
// isolation can swap out the registerer with testutil helpers.
//
// All counters use package-level vars so call sites don't need to plumb
// a registry through their constructors. This matches the common
// prometheus/client_golang usage pattern.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Label values for BatchTranscriptionFailed.
const (
	FailStageSubmit          = "submit_failed"
	FailStageWebhook         = "webhook_failed"
	FailStageRetryExhausted  = "retry_exhausted"
)

var (
	// BatchTranscriptionSubmitted counts successful Scribe v2 submit calls
	// (ElevenLabs accepted the audio URL and returned a request_id).
	BatchTranscriptionSubmitted = promauto.NewCounter(prometheus.CounterOpts{
		Name: "batch_transcription_submitted_total",
		Help: "ElevenLabs Scribe v2 submissions that returned a request_id.",
	})

	// BatchTranscriptionCompleted counts webhook ingests that successfully
	// wrote segments + flipped the meeting into transcription_status=completed.
	BatchTranscriptionCompleted = promauto.NewCounter(prometheus.CounterOpts{
		Name: "batch_transcription_completed_total",
		Help: "ElevenLabs webhook payloads ingested and persisted.",
	})

	// BatchTranscriptionFailed counts pipeline failures by stage. See the
	// FailStage* constants for the label values.
	BatchTranscriptionFailed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "batch_transcription_failed_total",
		Help: "Batch transcription failures, labelled by pipeline stage.",
	}, []string{"stage"})

	// AudioUploadBytes is the cumulative number of bytes received via the
	// resumable upload endpoints (bot + desktop). Every chunk bumps this
	// regardless of whether the upload ultimately finalises.
	AudioUploadBytes = promauto.NewCounter(prometheus.CounterOpts{
		Name: "audio_upload_bytes_total",
		Help: "Bytes appended to upload sessions via chunk endpoints.",
	})

	// AudioStorageDeleted counts successful Supabase deletes of meeting
	// audio (both immediate post-transcription deletes and cron sweeps).
	AudioStorageDeleted = promauto.NewCounter(prometheus.CounterOpts{
		Name: "audio_storage_deleted_total",
		Help: "Meeting audio objects removed from Supabase Storage.",
	})

	// BatchTranscriptionDurationSeconds — Phase 8.3 success criterion.
	// Observed by the webhook handler (IngestWebhookResult): time from
	// meeting.transcription_provider_job_id being recorded (submit) to the
	// webhook ingest that flipped transcription_status=completed. Useful
	// for alerting on ElevenLabs tail latency + regressions.
	//
	// Bucket choice: batch STT for a 3h meeting usually completes in
	// 30-120s; we want resolution in that band plus a long tail for
	// degraded provider conditions. Default prometheus buckets skew too
	// low (under 10s), so we hand-pick.
	BatchTranscriptionDurationSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "batch_transcription_duration_seconds",
		Help:    "Wall-clock time from ElevenLabs submit to webhook ingest success.",
		Buckets: []float64{5, 15, 30, 60, 120, 300, 600, 1800, 3600},
	})

	// ============================================================
	// Brief delivery pipeline (roadmap Phase 7)
	// ============================================================
	//
	// Labels:
	//   channel = "email" | "slack"
	//   status  = "sent" | "failed" | "skipped"
	//
	// "skipped" covers intentional no-ops: unsubscribed email, revoked
	// Slack integration, pending matched_email. Keeping them as a first-
	// class status (not "failed") prevents alert rules from firing on
	// healthy opt-out behaviour.

	// BriefDeliveryAttempted bumps every time the worker reaches a
	// terminal decision for a notify_brief job — regardless of outcome.
	// Alerting: watch status=failed rate over time; sent+skipped is
	// the healthy envelope.
	BriefDeliveryAttempted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "brief_delivery_attempted_total",
		Help: "Brief delivery terminal outcomes, by channel + status.",
	}, []string{"channel", "status"})

	// BriefDeliveryDurationSeconds observes the time from the worker
	// picking the job up to the terminal state write. Histogram buckets
	// skew low because SMTP + Slack are usually sub-second; the 5s tail
	// catches TLS handshake / provider degradation.
	BriefDeliveryDurationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "brief_delivery_duration_seconds",
		Help:    "Wall-clock time for one brief delivery attempt.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30},
	}, []string{"channel"})

	// SlackAPIErrors counts non-success chat.postMessage responses
	// bucketed by Slack's error code ("invalid_auth", "channel_not_found",
	// "rate_limited", "ratelimited", etc.). Low-cardinality field —
	// Slack error codes are a bounded set.
	SlackAPIErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "slack_api_errors_total",
		Help: "chat.postMessage non-OK responses by Slack error code.",
	}, []string{"code"})

	// SMTPErrors counts non-success SMTP sends bucketed by coarse code
	// class ("4xx" = transient, "5xx" = permanent, "network" = dial/TLS).
	// Keeping this to three classes prevents label explosion while still
	// letting us alert on "5xx spike" vs "network blip".
	// ---- Quota (issue #39, Faz 5) ----------------------------------
	//
	// QuotaChecks counts every Service.Check invocation, labelled by tier
	// and the outcome bucket. "allow" = below cap; "deny" = Exceeded=true;
	// "error" = repo error (fail-open path so the call still continued).
	// Both the HTTP middleware and the scheduler gates feed into this.
	QuotaChecks = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "quota_checks_total",
		Help: "Quota pre-flight checks, labelled by tier and result (allow|deny|error).",
	}, []string{"tier", "result"})

	// QuotaRejections counts only the deny path, but labels by endpoint /
	// gate so dashboards can show "where the 429s are landing". Endpoint
	// label is a stable string (route path for HTTP, "scheduler:bot" or
	// "scheduler:desktop" for cron-driven denies) — keep cardinality low.
	QuotaRejections = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "quota_rejections_total",
		Help: "Quota denials grouped by tier and the call site that issued the 429.",
	}, []string{"tier", "endpoint"})

	// QuotaCheckDurationSeconds tracks Check latency. Almost all calls
	// are a single PK lookup so the buckets skew low; the long tail
	// covers DB hiccups so an alert can spot quota-check induced latency
	// before users notice.
	QuotaCheckDurationSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "quota_check_duration_seconds",
		Help:    "Wall-clock duration of QuotaService.Check.",
		Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
	})

	// QuotaDrift counts the drift cron's reconciliations. "over" means
	// the materialized counter was ahead of usage_logs SUM (counter
	// rolled back to actual); "under" means counter was behind (caught
	// up). Either direction > 0.01 USD bumps this — drift_total going
	// up signals an issue with the atomic RecordUsage path.
	QuotaDrift = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "quota_drift_total",
		Help: "Counter↔usage_logs drift events reconciled by the daily cron, by direction (over|under).",
	}, []string{"direction"})

	// QuotaCycleResets counts new usage_quota_periods rows created when
	// a user's cycle rolls over. Tier-labelled so dashboards can plot
	// "active free users per cycle" without scanning user_quotas.
	QuotaCycleResets = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "quota_cycle_resets_total",
		Help: "Cycle rollovers (new usage_quota_periods rows inserted).",
	}, []string{"tier"})

	SMTPErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "smtp_errors_total",
		Help: "SMTP send failures by coarse class.",
	}, []string{"class"})
)

// Brief delivery status label constants — use these instead of raw
// strings so a typo fails at compile time and the counter cardinality
// stays predictable.
const (
	BriefStatusSent    = "sent"
	BriefStatusFailed  = "failed"
	BriefStatusSkipped = "skipped"

	BriefChannelEmail = "email"
	BriefChannelSlack = "slack"

	SMTPErrClass4xx     = "4xx"
	SMTPErrClass5xx     = "5xx"
	SMTPErrClassNetwork = "network"

	// Quota result label values (issue #39).
	QuotaResultAllow = "allow"
	QuotaResultDeny  = "deny"
	QuotaResultError = "error"

	// Quota drift directions.
	QuotaDriftOver  = "over"  // counter > usage_logs SUM
	QuotaDriftUnder = "under" // counter < usage_logs SUM
)
