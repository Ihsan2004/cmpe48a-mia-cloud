// Package services — calendar_backfill.go is the one-shot, 30-day past-only
// Google Calendar fetcher used by the team-detection onboarding flow
// (v1.1 milestone, Phase 5). It returns normalized attendee data shaped
// exactly the way Phase 6's detection algorithm consumes it.
//
// Strict scope-out (FETCH-03): this file MUST NOT modify calendar.go. The
// only cross-file symbols it references from calendar.go are
// CalendarService.GetServiceWithRefreshToken (the public OAuth helper at
// calendar.go:201) and the package-private isGoogleReconnectRequiredError
// (calendar.go:30) — both via direct calls within the same `services`
// package. Any additional touchpoint to calendar.go is a FETCH-03 violation.
//
// The fetcher uses errgroup-bounded fan-out across six 5-day buckets
// (CONTEXT.md D-04, D-05) and a single upfront OAuth refresh (D-07). It
// returns a typed *BackfillError carrying one of five ErrorClass values
// so the upstream onboarding handler (Phase 7) can route to retry/skip UX
// (FETCH-04). The orchestration code lives in plan 05-02; this file (as
// of plan 05-01) holds only the contract types + pure-function helpers.

package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/googleapi"
)

// BackfillEvent is the per-event shape consumed by Phase 6's team-detection
// algorithm. It deliberately does NOT carry the full *calendar.Event — that
// is the SDK isolation boundary (D-03). Field order mirrors CONTEXT.md D-01.
type BackfillEvent struct {
	EventID          string
	RecurringEventID string
	StartTime        time.Time
	EndTime          time.Time
	Organizer        string
	Title            string
	Attendees        []BackfillAttendee
}

// BackfillAttendee is the per-attendee shape after the four-step
// normalization (D-12). Email is lowercased + trimmed; ResponseStatus is
// passed through verbatim from Google ("accepted" | "tentative" |
// "needsAction" | "declined") — declined attendees are KEPT (D-11). The
// algorithm in Phase 6 decides how to weight responseStatus.
type BackfillAttendee struct {
	Email          string
	ResponseStatus string
}

// ErrorClass is the typed enum of five FETCH-04 retry/skip UX classes.
type ErrorClass string

const (
	ClassOAuthExpired   ErrorClass = "oauth_expired"
	ClassQuotaExhausted ErrorClass = "quota_exhausted"
	ClassNetwork        ErrorClass = "network"
	ClassServer5xx      ErrorClass = "server_5xx"
	ClassPermanent      ErrorClass = "permanent"
)

// DefaultQuotaRetryAfter is the fallback wait duration emitted by
// classifyGoogleError when a 429/403-rateLimit response lacks a
// Retry-After header. Google's official guidance is exponential backoff;
// 30s is the spec-suggested initial wait. RESEARCH §1 + §7 Open Risk #6.
const DefaultQuotaRetryAfter = 30 * time.Second

// BackfillError is the typed error returned to Phase 7's handler (D-16).
// Class drives the UX branch; RetryAfter is non-zero only for
// ClassQuotaExhausted; Bucket is -1 for upfront-probe failures and 0..5
// for per-bucket failures.
type BackfillError struct {
	Class      ErrorClass
	RetryAfter time.Duration
	Cause      error
	Bucket     int
}

func (e *BackfillError) Error() string {
	if e.Bucket < 0 {
		return fmt.Sprintf("calendar backfill: %s during upfront probe: %v", e.Class, e.Cause)
	}
	return fmt.Sprintf("calendar backfill: %s in bucket %d: %v", e.Class, e.Bucket, e.Cause)
}

func (e *BackfillError) Unwrap() error { return e.Cause }

// classifyGoogleError maps any error returned by the Google Calendar SDK
// (or surrounding transport) into exactly one of the five FETCH-04
// ErrorClass values. Caller passes bucket=-1 for the upfront-probe path
// and 0..5 for per-bucket failures. Nil-safe: returns nil when err==nil.
//
// Mapping table is RESEARCH.md §2; this function is its single
// implementation point. Any new error shape that surfaces from Google
// must be added here, never inline at the call site.
func classifyGoogleError(err error, bucket int) *BackfillError {
	if err == nil {
		return nil
	}

	// Probe-path token failure: the existing helper already detects
	// invalid_grant / token-expired / insufficientPermissions strings
	// (calendar.go:30-43). Reuse it directly — same package, no
	// FETCH-03 violation. This branch fires for the upfront-probe path
	// and ALSO catches mid-run 401s whose body contains those strings.
	if isGoogleReconnectRequiredError(err) {
		return &BackfillError{Class: ClassOAuthExpired, Bucket: bucket, Cause: err}
	}

	var gErr *googleapi.Error
	if errors.As(err, &gErr) {
		switch gErr.Code {
		case 401:
			return &BackfillError{Class: ClassOAuthExpired, Bucket: bucket, Cause: err}
		case 429:
			return &BackfillError{
				Class:      ClassQuotaExhausted,
				RetryAfter: parseRetryAfter(gErr.Header),
				Bucket:     bucket,
				Cause:      err,
			}
		case 403:
			if hasRateLimitReason(gErr.Errors) {
				return &BackfillError{
					Class:      ClassQuotaExhausted,
					RetryAfter: parseRetryAfter(gErr.Header),
					Bucket:     bucket,
					Cause:      err,
				}
			}
			return &BackfillError{Class: ClassPermanent, Bucket: bucket, Cause: err}
		case 500, 502, 503, 504:
			return &BackfillError{Class: ClassServer5xx, Bucket: bucket, Cause: err}
		default:
			// 400, 404, 410, others
			return &BackfillError{Class: ClassPermanent, Bucket: bucket, Cause: err}
		}
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &BackfillError{Class: ClassNetwork, Bucket: bucket, Cause: err}
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return &BackfillError{Class: ClassNetwork, Bucket: bucket, Cause: err}
	}

	// Conservative default — assume transport-layer issue. RESEARCH §2
	// catch-all row.
	return &BackfillError{Class: ClassNetwork, Bucket: bucket, Cause: err}
}

// hasRateLimitReason scans gErr.Errors for any Reason in the rate-limit
// set ("rateLimitExceeded", "userRateLimitExceeded", "quotaExceeded").
// Iterates the full slice (not just [0]) per Assumption A3 in RESEARCH.
func hasRateLimitReason(items []googleapi.ErrorItem) bool {
	for _, it := range items {
		switch it.Reason {
		case "rateLimitExceeded", "userRateLimitExceeded", "quotaExceeded":
			return true
		}
	}
	return false
}

// parseRetryAfter reads the Retry-After header per RFC 7231: integer
// seconds OR HTTP-date. Returns DefaultQuotaRetryAfter (30s) when the
// header is missing, malformed, or the HTTP-date has already passed.
// Google does not consistently emit Retry-After per their own quota
// guidance (RESEARCH §1, Pitfall 5).
func parseRetryAfter(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return DefaultQuotaRetryAfter
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return DefaultQuotaRetryAfter
}

// phoneAttendeeRE matches phone-shaped pseudo-emails Google uses for
// dial-in participants: optional "+", then digit, then 6+ chars of
// digits/spaces/dashes/parens, then "@" + anything. Anchored at start
// (^) so a normal email containing a digit prefix is not dropped.
// CONTEXT.md D-12 step 4. The regex is compiled once at package init.
var phoneAttendeeRE = regexp.MustCompile(`^\+?[0-9][0-9 \-()]{6,}@.*`)

// normalizeAttendees applies the four-step rule from CONTEXT.md D-12 in
// order, plus D-13 (drop attendees with no email — display names never
// leave the fetcher boundary). userEmail is the caller's own address;
// the lowercased form is used for self-attendee detection.
//
// This helper is INTERNAL — Phase 6 must NOT re-normalize. Per D-13 the
// PRIVACY-04 ceiling is enforced here and only here.
func normalizeAttendees(attendees []*calendar.EventAttendee, userEmail string) []BackfillAttendee {
	if len(attendees) == 0 {
		return nil
	}
	selfLower := strings.ToLower(strings.TrimSpace(userEmail))
	out := make([]BackfillAttendee, 0, len(attendees))
	for _, a := range attendees {
		if a == nil {
			continue
		}
		// Step 1: lowercase + trim. Step 0: drop if no email (D-13).
		email := strings.ToLower(strings.TrimSpace(a.Email))
		if email == "" {
			continue
		}
		// Step 2: drop self.
		if email == selfLower {
			continue
		}
		// Step 3: drop room/resource calendars.
		if strings.HasSuffix(email, "@resource.calendar.google.com") {
			continue
		}
		// Step 4: drop phone-shaped pseudo-emails.
		if phoneAttendeeRE.MatchString(email) {
			continue
		}
		// Keep — passthrough ResponseStatus verbatim (D-11 — declined kept).
		out = append(out, BackfillAttendee{
			Email:          email,
			ResponseStatus: a.ResponseStatus,
		})
	}
	return out
}

// bucketWindow describes a single 5-day pagination bucket. Inclusive
// start, exclusive end. CONTEXT.md D-04, D-05.
type bucketWindow struct {
	Start time.Time
	End   time.Time
}

// bucketWindows splits the closed-open interval [now-30d, now) into
// exactly six 5-day buckets, in chronological order. Bucket 0 is the
// oldest (now-30d to now-25d); bucket 5 is the newest (now-5d to now).
// Bucket 5's End equals `now` exactly, by construction (30 / 6 = 5
// with no remainder). CONTEXT.md D-15 — the caller snapshots `now`
// once at function entry and passes it in here.
func bucketWindows(now time.Time) [6]bucketWindow {
	const numBuckets = 6
	const bucketDur = 5 * 24 * time.Hour
	timeMin := now.Add(-30 * 24 * time.Hour)
	var out [numBuckets]bucketWindow
	for i := 0; i < numBuckets; i++ {
		bStart := timeMin.Add(time.Duration(i) * bucketDur)
		bEnd := bStart.Add(bucketDur)
		if i == numBuckets-1 {
			bEnd = now // last bucket ends exactly at `now` to avoid drift
		}
		out[i] = bucketWindow{Start: bStart, End: bEnd}
	}
	return out
}

// listRequest mirrors the parameters CalendarBackfillService passes into
// the Events.List builder. Defining it as a struct (rather than a
// growing positional arg list) keeps the seam stable across SDK version
// bumps. Field names match the SDK builder methods 1:1.
type listRequest struct {
	CalendarID   string
	TimeMin      time.Time
	TimeMax      time.Time
	SingleEvents bool
	PageToken    string
	MaxResults   int64
	ShowDeleted  bool
}

// listResponse is the narrow projection of *calendar.Events the
// orchestrator actually consumes — Items + NextPageToken. Bringing the
// full SDK Events struct across the seam would defeat the purpose.
type listResponse struct {
	Items         []*calendar.Event
	NextPageToken string
}

// eventsLister narrows the *calendar.Service surface for testability.
// Production wires sdkEventsLister (an adapter that calls the real
// service.Events.List builder); tests inject a fake that emits canned
// listResponse values + injected per-call latency. RESEARCH §4 — this
// pattern matches calendar_upcoming.go's narrowing-interface seams.
//
// IMPORTANT: this seam is BELOW CalendarService.GetServiceWithRefreshToken,
// not parallel to it. The OAuth surface stays singular (D-09); only the
// raw .List(...).Do() round-trip is mocked.
type eventsLister interface {
	List(ctx context.Context, req listRequest) (*listResponse, error)
}

// --- public seam consumed by Phase 7 handler tests ----------------------

// BackfillFetcher is the one-method seam Phase 7's onboarding handler
// uses to mock the fetcher in unit tests. Production wires
// *CalendarBackfillService; tests inject a fake. Declared here (not in
// Phase 7) so the contract lives next to the implementation. CONTEXT.md
// <code_context>.
type BackfillFetcher interface {
	FetchPast30DaysWithAttendees(ctx context.Context, userID uuid.UUID, userEmail string) ([]BackfillEvent, error)
}

// --- service struct + constructor --------------------------------------

// calendarSvcLike narrows *CalendarService to just the OAuth surface
// Phase 5 uses. This is purely defensive — the only method we call is
// GetServiceWithRefreshToken — but pinning the surface here lets tests
// verify (via type assertion) that we never reach into other methods of
// CalendarService and lets us inject a stub in unit tests without
// constructing a full *CalendarService.
type calendarSvcLike interface {
	GetServiceWithRefreshToken(userID uuid.UUID) (*calendar.Service, error)
}

// CalendarBackfillService is the public type Phase 7 wires up. It does
// not embed *CalendarService — it depends only on the narrow
// calendarSvcLike surface (D-09: GetServiceWithRefreshToken is the only
// allowed cross-reference). Tests inject a stub for both
// calendarSvcLike (the OAuth probe) and listerFactory (the SDK seam).
type CalendarBackfillService struct {
	calendarSvc   calendarSvcLike
	listerFactory func(*calendar.Service) eventsLister // production: sdkEventsLister; tests: fake
	now           func() time.Time
}

// NewCalendarBackfillService is the production wire. Phase 7 calls this
// once at startup and reuses the returned *CalendarBackfillService for
// all onboarding requests; *calendar.Service is built per-request via
// the OAuth probe.
func NewCalendarBackfillService(calendarSvc *CalendarService) *CalendarBackfillService {
	return &CalendarBackfillService{
		calendarSvc:   calendarSvc,
		listerFactory: func(svc *calendar.Service) eventsLister { return &sdkEventsLister{svc: svc} },
		now:           func() time.Time { return time.Now().UTC() },
	}
}

// SetNowFunc lets tests freeze the clock. Mirrors calendar_upcoming.go.
func (s *CalendarBackfillService) SetNowFunc(f func() time.Time) {
	if f != nil {
		s.now = f
	}
}

// --- production SDK adapter --------------------------------------------

// sdkEventsLister wraps *calendar.Service.Events.List into the
// eventsLister seam. RESEARCH §4. ShowDeleted is forced to false; we
// don't want cancelled events. MaxResults is set per-call by the
// walker, defaulting to maxResultsPerPage (2500 — the SDK ceiling).
type sdkEventsLister struct {
	svc *calendar.Service
}

func (a *sdkEventsLister) List(ctx context.Context, req listRequest) (*listResponse, error) {
	resp, err := a.svc.Events.List(req.CalendarID).
		TimeMin(req.TimeMin.Format(time.RFC3339)).
		TimeMax(req.TimeMax.Format(time.RFC3339)).
		SingleEvents(req.SingleEvents).
		PageToken(req.PageToken).
		MaxResults(req.MaxResults).
		ShowDeleted(req.ShowDeleted).
		Context(ctx).
		Do()
	if err != nil {
		return nil, err
	}
	return &listResponse{
		Items:         resp.Items,
		NextPageToken: resp.NextPageToken,
	}, nil
}

// --- pagination + bucket walker ----------------------------------------

const (
	// maxResultsPerPage is the Calendar API hard ceiling per page.
	// RESEARCH §1 — verified at developers.google.com/calendar/api/v3/reference/events/list.
	maxResultsPerPage int64 = 2500

	// maxPagesPerBucket caps the per-bucket pagination loop at 10 pages.
	// 10 × 2500 = 25,000 events per 5-day bucket = 150,000 events per
	// 30-day window. Impossible in real-world calendars; the cap exists
	// to defend against a misbehaving fake lister or a Google bug that
	// returns the same nextPageToken forever (RESEARCH §6 Pitfall 6).
	// Cap-exceeded → ClassPermanent (T-05-04 mitigation — DoS via
	// attacker-crafted calendar with millions of events).
	maxPagesPerBucket = 10

	// primaryCalendar is the only calendar the fetcher reads (D-14).
	primaryCalendar = "primary"
)

// fetchBucket walks pageToken serially within one bucket. Returns
// the flattened, attendee-normalized BackfillEvent slice. On any error
// returns a fully-populated *BackfillError with Bucket set; caller
// (errgroup goroutine) returns it directly.
//
// All-day events (event.Start.DateTime == "") are silently skipped —
// they have no time component the algorithm can consume. Existing
// precedent: webhook.go:121-126.
func (s *CalendarBackfillService) fetchBucket(
	ctx context.Context,
	lister eventsLister,
	bucket int,
	bw bucketWindow,
	userEmail string,
) ([]BackfillEvent, error) {
	out := make([]BackfillEvent, 0, 64)
	pageToken := ""
	for page := 0; page < maxPagesPerBucket; page++ {
		// D-10: SingleEvents(true) — every occurrence of a recurring series
		// is expanded into its own event row so Phase 6's temporal-spread
		// weighting can decay them independently.
		resp, err := lister.List(ctx, listRequest{
			CalendarID:   primaryCalendar,
			TimeMin:      bw.Start,
			TimeMax:      bw.End,
			SingleEvents: true, // D-10 — see comment above
			PageToken:    pageToken,
			MaxResults:   maxResultsPerPage,
			ShowDeleted:  false,
		})
		if err != nil {
			return nil, classifyGoogleError(err, bucket)
		}
		for _, e := range resp.Items {
			if e == nil || e.Start == nil || e.Start.DateTime == "" {
				// All-day or malformed — skip silently.
				continue
			}
			start, perr := time.Parse(time.RFC3339, e.Start.DateTime)
			if perr != nil {
				continue // malformed timestamp; skip rather than fail the whole bucket
			}
			var end time.Time
			if e.End != nil && e.End.DateTime != "" {
				if t, perr2 := time.Parse(time.RFC3339, e.End.DateTime); perr2 == nil {
					end = t
				}
			}
			organizer := ""
			if e.Organizer != nil {
				organizer = strings.ToLower(strings.TrimSpace(e.Organizer.Email))
			}
			out = append(out, BackfillEvent{
				EventID:          e.Id,
				RecurringEventID: e.RecurringEventId,
				StartTime:        start.UTC(),
				EndTime:          end.UTC(),
				Organizer:        organizer,
				Title:            e.Summary,
				Attendees:        normalizeAttendees(e.Attendees, userEmail),
			})
		}
		if resp.NextPageToken == "" {
			return out, nil
		}
		pageToken = resp.NextPageToken
	}
	// Cap exceeded — defensive ClassPermanent. T-05-04.
	return nil, &BackfillError{
		Class:  ClassPermanent,
		Bucket: bucket,
		Cause:  fmt.Errorf("calendar backfill: bucket %d exceeded %d page cap", bucket, maxPagesPerBucket),
	}
}

// --- top-level orchestrator (FETCH-01 + FETCH-04) ----------------------

// FetchPast30DaysWithAttendees is the public entry point. CONTEXT.md
// D-04 (6×5d), D-07 (single upfront refresh), D-15 (window snapshot),
// D-18 (first-failure-wins).
//
// Failure modes (all returned as *BackfillError, never as plain error):
//   - probe failure → Bucket=-1, classifyGoogleError-mapped class
//   - any bucket failure → that bucket's class wins (errgroup cancels siblings)
//   - cap exceeded → Bucket=N, ClassPermanent
//
// On success, returns events flattened in deterministic bucket-order
// (bucket 0 first, bucket 5 last). Within a bucket the order matches
// what Google returned. Phase 6's algorithm is order-insensitive but
// deterministic ordering helps with reproducibility logs.
func (s *CalendarBackfillService) FetchPast30DaysWithAttendees(
	ctx context.Context,
	userID uuid.UUID,
	userEmail string,
) ([]BackfillEvent, error) {
	// Single upfront probe (D-07). Failure → Bucket=-1.
	svc, err := s.calendarSvc.GetServiceWithRefreshToken(userID)
	if err != nil {
		return nil, classifyGoogleError(err, -1)
	}
	lister := s.listerFactory(svc)

	// Snapshot the window once (D-15). Each call observes its own clock.
	now := s.now()
	buckets := bucketWindows(now)

	slog.Info("calendar backfill: starting",
		"user_id", userID,
		"buckets", len(buckets),
		"window_start", buckets[0].Start.Format(time.RFC3339),
		"window_end", buckets[len(buckets)-1].End.Format(time.RFC3339),
	)

	results := make([][]BackfillEvent, len(buckets))
	g, gctx := errgroup.WithContext(ctx)
	for i := range buckets {
		i := i // capture (defensive — Go 1.22+ doesn't need this, but matches codebase convention)
		bw := buckets[i]
		g.Go(func() error {
			events, ferr := s.fetchBucket(gctx, lister, i, bw, userEmail)
			if ferr != nil {
				slog.Warn("calendar backfill: bucket failed",
					"bucket", i,
					"class", ferr.(*BackfillError).Class,
				)
				return ferr // already typed *BackfillError
			}
			results[i] = events
			return nil
		})
	}
	if werr := g.Wait(); werr != nil {
		// errgroup returns the first non-nil error. Already typed.
		return nil, werr
	}

	// Flatten in bucket order (D-18 — partial success not surfaced; we
	// only reach here if all 6 buckets succeeded).
	total := 0
	for _, b := range results {
		total += len(b)
	}
	out := make([]BackfillEvent, 0, total)
	for _, b := range results {
		out = append(out, b...)
	}
	return out, nil
}
