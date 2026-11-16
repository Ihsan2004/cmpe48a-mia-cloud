package services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/googleapi"
)

func TestClassifyGoogleError_NilReturnsNil(t *testing.T) {
	if got := classifyGoogleError(nil, 0); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestClassifyGoogleError_OAuthExpired_InvalidGrant(t *testing.T) {
	err := errors.New("oauth2: cannot fetch token: invalid_grant")
	be := classifyGoogleError(err, -1)
	if be == nil || be.Class != ClassOAuthExpired || be.Bucket != -1 {
		t.Fatalf("want oauth_expired bucket=-1, got %#v", be)
	}
}

func TestClassifyGoogleError_OAuth401(t *testing.T) {
	err := &googleapi.Error{Code: 401, Message: "Invalid Credentials"}
	be := classifyGoogleError(err, 2)
	if be == nil || be.Class != ClassOAuthExpired || be.Bucket != 2 {
		t.Fatalf("want oauth_expired bucket=2, got %#v", be)
	}
}

func TestClassifyGoogleError_QuotaExhausted_429_WithHeader(t *testing.T) {
	err := &googleapi.Error{
		Code:   429,
		Header: http.Header{"Retry-After": []string{"45"}},
	}
	be := classifyGoogleError(err, 0)
	if be == nil || be.Class != ClassQuotaExhausted {
		t.Fatalf("want quota_exhausted, got %#v", be)
	}
	if be.RetryAfter != 45*time.Second {
		t.Fatalf("want RetryAfter=45s, got %v", be.RetryAfter)
	}
}

func TestClassifyGoogleError_QuotaExhausted_403_RateLimit_NoHeader(t *testing.T) {
	err := &googleapi.Error{
		Code:   403,
		Errors: []googleapi.ErrorItem{{Reason: "rateLimitExceeded"}},
	}
	be := classifyGoogleError(err, 1)
	if be == nil || be.Class != ClassQuotaExhausted {
		t.Fatalf("want quota_exhausted, got %#v", be)
	}
	if be.RetryAfter != DefaultQuotaRetryAfter {
		t.Fatalf("want RetryAfter=%v, got %v", DefaultQuotaRetryAfter, be.RetryAfter)
	}
}

func TestClassifyGoogleError_Permanent_403_NoRateLimitReason(t *testing.T) {
	err := &googleapi.Error{
		Code:   403,
		Errors: []googleapi.ErrorItem{{Reason: "forbidden"}},
	}
	be := classifyGoogleError(err, 3)
	if be == nil || be.Class != ClassPermanent {
		t.Fatalf("want permanent, got %#v", be)
	}
}

func TestClassifyGoogleError_Server5xx(t *testing.T) {
	for _, code := range []int{500, 502, 503, 504} {
		err := &googleapi.Error{Code: code}
		be := classifyGoogleError(err, 0)
		if be == nil || be.Class != ClassServer5xx {
			t.Fatalf("code=%d: want server_5xx, got %#v", code, be)
		}
	}
}

func TestClassifyGoogleError_Permanent_400(t *testing.T) {
	err := &googleapi.Error{Code: 400}
	be := classifyGoogleError(err, 0)
	if be == nil || be.Class != ClassPermanent {
		t.Fatalf("want permanent, got %#v", be)
	}
}

func TestClassifyGoogleError_Permanent_404(t *testing.T) {
	err := &googleapi.Error{Code: 404}
	be := classifyGoogleError(err, 0)
	if be == nil || be.Class != ClassPermanent {
		t.Fatalf("want permanent, got %#v", be)
	}
}

func TestClassifyGoogleError_Network_DeadlineExceeded(t *testing.T) {
	be := classifyGoogleError(context.DeadlineExceeded, 4)
	if be == nil || be.Class != ClassNetwork {
		t.Fatalf("want network, got %#v", be)
	}
}

func TestClassifyGoogleError_Network_NetOpError(t *testing.T) {
	err := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	be := classifyGoogleError(err, 5)
	if be == nil || be.Class != ClassNetwork {
		t.Fatalf("want network, got %#v", be)
	}
}

func TestClassifyGoogleError_Network_UnknownErrorConservative(t *testing.T) {
	be := classifyGoogleError(errors.New("totally unknown"), 0)
	if be == nil || be.Class != ClassNetwork {
		t.Fatalf("want network (conservative), got %#v", be)
	}
}

func TestParseRetryAfter_Seconds(t *testing.T) {
	h := http.Header{"Retry-After": []string{"60"}}
	if got := parseRetryAfter(h); got != 60*time.Second {
		t.Fatalf("want 60s, got %v", got)
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	future := time.Now().UTC().Add(30 * time.Second).Format(http.TimeFormat)
	h := http.Header{"Retry-After": []string{future}}
	got := parseRetryAfter(h)
	// Allow ±2s tolerance for clock motion between header build + parse.
	if got < 28*time.Second || got > 32*time.Second {
		t.Fatalf("want ~30s, got %v", got)
	}
}

func TestParseRetryAfter_MissingFallsBackToDefault(t *testing.T) {
	if got := parseRetryAfter(http.Header{}); got != DefaultQuotaRetryAfter {
		t.Fatalf("want %v, got %v", DefaultQuotaRetryAfter, got)
	}
}

func TestParseRetryAfter_GarbageFallsBackToDefault(t *testing.T) {
	h := http.Header{"Retry-After": []string{"not-a-duration"}}
	if got := parseRetryAfter(h); got != DefaultQuotaRetryAfter {
		t.Fatalf("want %v, got %v", DefaultQuotaRetryAfter, got)
	}
}

// --- normalization tests (D-11, D-12, D-13) ---

func TestNormalize_LowercaseAndTrim(t *testing.T) {
	in := []*calendar.EventAttendee{
		{Email: "  Alice@Example.COM ", ResponseStatus: "accepted"},
	}
	got := normalizeAttendees(in, "bob@example.com")
	if len(got) != 1 || got[0].Email != "alice@example.com" {
		t.Fatalf("want lowercased+trimmed, got %#v", got)
	}
	if got[0].ResponseStatus != "accepted" {
		t.Fatalf("ResponseStatus passthrough broken: %q", got[0].ResponseStatus)
	}
}

func TestNormalize_DropSelf(t *testing.T) {
	in := []*calendar.EventAttendee{
		{Email: "alice@example.com"},
		{Email: "BOB@example.com"},
	}
	got := normalizeAttendees(in, "bob@example.com")
	if len(got) != 1 || got[0].Email != "alice@example.com" {
		t.Fatalf("self should be dropped, got %#v", got)
	}
}

func TestNormalize_DropResource(t *testing.T) {
	in := []*calendar.EventAttendee{
		{Email: "room1@resource.calendar.google.com"},
		{Email: "Room2@RESOURCE.CALENDAR.GOOGLE.COM"},
		{Email: "alice@example.com"},
	}
	got := normalizeAttendees(in, "bob@example.com")
	if len(got) != 1 || got[0].Email != "alice@example.com" {
		t.Fatalf("rooms should be dropped, got %#v", got)
	}
}

func TestNormalize_DropPhone(t *testing.T) {
	in := []*calendar.EventAttendee{
		{Email: "+14155551234@meet.google.com"},
		{Email: "1 (415) 555-1234@meet.google.com"},
		{Email: "alice@example.com"},
		{Email: "carol@example.com"}, // starts with letter — kept
	}
	got := normalizeAttendees(in, "bob@example.com")
	if len(got) != 2 {
		t.Fatalf("want 2 (alice + carol), got %d: %#v", len(got), got)
	}
}

func TestNormalize_DropEmptyEmail_DisplayNameNeverCarried(t *testing.T) {
	in := []*calendar.EventAttendee{
		{Email: "", DisplayName: "Charlie"},
		{Email: "alice@example.com", DisplayName: "Alice Smith"},
	}
	got := normalizeAttendees(in, "bob@example.com")
	if len(got) != 1 || got[0].Email != "alice@example.com" {
		t.Fatalf("want only alice, got %#v", got)
	}
	// PRIVACY-04 / D-13 enforcement — DisplayName must not be on the
	// BackfillAttendee struct at all. Compile-time guarantee, but we
	// also assert the in-memory shape has no display-name leak.
	for _, ba := range got {
		if ba.Email == "Alice Smith" {
			t.Fatal("display name leaked into Email field")
		}
	}
}

func TestNormalize_DeclinedAttendeesKept(t *testing.T) {
	in := []*calendar.EventAttendee{
		{Email: "alice@example.com", ResponseStatus: "declined"},
		{Email: "carol@example.com", ResponseStatus: "tentative"},
	}
	got := normalizeAttendees(in, "bob@example.com")
	if len(got) != 2 {
		t.Fatalf("declined should be kept, got %d: %#v", len(got), got)
	}
	if got[0].ResponseStatus != "declined" || got[1].ResponseStatus != "tentative" {
		t.Fatalf("ResponseStatus passthrough broken: %#v", got)
	}
}

func TestNormalize_OrderPreserved(t *testing.T) {
	in := []*calendar.EventAttendee{
		{Email: "d@example.com"},
		{Email: "a@example.com"},
		{Email: "c@example.com"},
		{Email: "b@example.com"},
	}
	got := normalizeAttendees(in, "self@example.com")
	want := []string{"d@example.com", "a@example.com", "c@example.com", "b@example.com"}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %d want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Email != w {
			t.Fatalf("idx %d: got %q want %q", i, got[i].Email, w)
		}
	}
}

// --- bucket math tests (D-04, D-05, D-15) ---

func TestBucketWindows_ExactCoverage(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	buckets := bucketWindows(now)

	// First bucket starts at now-30d.
	wantStart := now.Add(-30 * 24 * time.Hour)
	if !buckets[0].Start.Equal(wantStart) {
		t.Fatalf("bucket 0 Start = %v, want %v", buckets[0].Start, wantStart)
	}
	// Last bucket ends exactly at now.
	if !buckets[5].End.Equal(now) {
		t.Fatalf("bucket 5 End = %v, want %v", buckets[5].End, now)
	}
	// No overlap, no gap: bucket[i].End == bucket[i+1].Start for all i.
	for i := 0; i < 5; i++ {
		if !buckets[i].End.Equal(buckets[i+1].Start) {
			t.Fatalf("gap/overlap between bucket %d (%v) and %d (%v)",
				i, buckets[i].End, i+1, buckets[i+1].Start)
		}
	}
}

func TestBucketWindows_EachIsFiveDays(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 30, 45, 0, time.UTC)
	buckets := bucketWindows(now)
	want := 5 * 24 * time.Hour
	for i, b := range buckets {
		got := b.End.Sub(b.Start)
		if got != want {
			t.Fatalf("bucket %d duration = %v, want %v", i, got, want)
		}
	}
}

func TestBucketWindows_Deterministic(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	a := bucketWindows(now)
	b := bucketWindows(now)
	for i := 0; i < 6; i++ {
		if !a[i].Start.Equal(b[i].Start) || !a[i].End.Equal(b[i].End) {
			t.Fatalf("bucket %d not deterministic: %v vs %v", i, a[i], b[i])
		}
	}
}

// =====================================================================
// Test fakes for the orchestrator (Task 2 of plan 05-02)
// =====================================================================

// fakeCalendarSvc implements calendarSvcLike. Returns either a stub
// *calendar.Service (acceptable for tests because we never actually
// call .Events.List on it — the listerFactory swaps in a fake) or an
// error to drive the probe-failure path.
type fakeCalendarSvc struct {
	probeErr error
}

func (f *fakeCalendarSvc) GetServiceWithRefreshToken(_ uuid.UUID) (*calendar.Service, error) {
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	// We can return the zero-value pointer because production code goes
	// through listerFactory before touching anything on *calendar.Service.
	// The test injects a fake listerFactory in newTestBackfillService.
	return &calendar.Service{}, nil
}

// bucketResponse is what fakeEventsLister returns per (bucket, page) key.
type bucketResponse struct {
	items         []*calendar.Event
	nextPageToken string
	err           error
	delay         time.Duration // simulated round-trip latency
}

// fakeEventsLister implements eventsLister. The bucket index is derived
// from the listRequest.TimeMin (matched to the bucketWindow Start). Per
// (bucket, pageToken) responses are pre-loaded; an unmatched request
// returns an empty page (terminates the walker).
type fakeEventsLister struct {
	mu       sync.Mutex
	requests []listRequest // for assertions

	bucketStarts [6]time.Time                      // populated from bucketWindows(testNow)
	responses    map[int]map[string]bucketResponse // bucketIdx → pageToken → response
	defaultDelay time.Duration
}

func (f *fakeEventsLister) bucketIndex(req listRequest) int {
	for i, st := range f.bucketStarts {
		if req.TimeMin.Equal(st) {
			return i
		}
	}
	return -1
}

func (f *fakeEventsLister) List(ctx context.Context, req listRequest) (*listResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	idx := f.bucketIndex(req)
	var resp bucketResponse
	if idx >= 0 {
		if pages, ok := f.responses[idx]; ok {
			if r, ok2 := pages[req.PageToken]; ok2 {
				resp = r
			}
		}
	}
	f.mu.Unlock()

	delay := resp.delay
	if delay == 0 {
		delay = f.defaultDelay
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if resp.err != nil {
		return nil, resp.err
	}
	return &listResponse{Items: resp.items, NextPageToken: resp.nextPageToken}, nil
}

func newTestBackfillService(t *testing.T, fakeSvc *fakeCalendarSvc, fakeLister *fakeEventsLister, frozenNow time.Time) *CalendarBackfillService {
	t.Helper()
	s := &CalendarBackfillService{
		calendarSvc:   fakeSvc,
		listerFactory: func(_ *calendar.Service) eventsLister { return fakeLister },
		now:           func() time.Time { return frozenNow },
	}
	// Populate the lister's bucket starts so it can resolve incoming
	// listRequest.TimeMin → bucket index.
	bws := bucketWindows(frozenNow)
	for i, b := range bws {
		fakeLister.bucketStarts[i] = b.Start
	}
	return s
}

// makeEvent builds a *calendar.Event with the supplied id, RFC3339 start,
// 30-min duration, organizer, summary, and a slice of attendees.
func makeEvent(id, startRFC3339, organizer, summary string, attendees []*calendar.EventAttendee) *calendar.Event {
	end, _ := time.Parse(time.RFC3339, startRFC3339)
	end = end.Add(30 * time.Minute)
	return &calendar.Event{
		Id:        id,
		Summary:   summary,
		Start:     &calendar.EventDateTime{DateTime: startRFC3339},
		End:       &calendar.EventDateTime{DateTime: end.Format(time.RFC3339)},
		Organizer: &calendar.EventOrganizer{Email: organizer},
		Attendees: attendees,
	}
}

// =====================================================================
// Orchestrator tests (FETCH-01, FETCH-04 wiring + bucket walker tests)
// =====================================================================

func TestFetch_Success_30Events(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	fakeLister := &fakeEventsLister{responses: map[int]map[string]bucketResponse{}}
	for i := 0; i < 6; i++ {
		// 5 events per bucket, distinct IDs, attendees include self + alice
		bw := bucketWindows(now)[i]
		events := make([]*calendar.Event, 5)
		for j := 0; j < 5; j++ {
			events[j] = makeEvent(
				fmt.Sprintf("evt-%d-%d", i, j),
				bw.Start.Add(time.Duration(j)*time.Hour).Format(time.RFC3339),
				"alice@example.com",
				"Standup",
				[]*calendar.EventAttendee{
					{Email: "self@example.com"}, // dropped by normalize
					{Email: "alice@example.com", ResponseStatus: "accepted"},
				},
			)
		}
		fakeLister.responses[i] = map[string]bucketResponse{
			"": {items: events, nextPageToken: ""},
		}
	}
	svc := newTestBackfillService(t, &fakeCalendarSvc{}, fakeLister, now)

	out, err := svc.FetchPast30DaysWithAttendees(context.Background(), uuid.New(), "self@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 30 {
		t.Fatalf("want 30 events, got %d", len(out))
	}
	for _, ev := range out {
		if len(ev.Attendees) != 1 || ev.Attendees[0].Email != "alice@example.com" {
			t.Fatalf("normalize failed for %s: %#v", ev.EventID, ev.Attendees)
		}
	}
}

func TestFetch_Pagination(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	bw := bucketWindows(now)[0]
	page1Events := []*calendar.Event{makeEvent("p1-a", bw.Start.Format(time.RFC3339), "alice@example.com", "X", nil)}
	page2Events := []*calendar.Event{makeEvent("p2-a", bw.Start.Add(time.Hour).Format(time.RFC3339), "alice@example.com", "X", nil)}
	fakeLister := &fakeEventsLister{
		responses: map[int]map[string]bucketResponse{
			0: {
				"":   {items: page1Events, nextPageToken: "p2"},
				"p2": {items: page2Events, nextPageToken: ""},
			},
		},
	}
	svc := newTestBackfillService(t, &fakeCalendarSvc{}, fakeLister, now)
	out, err := svc.FetchPast30DaysWithAttendees(context.Background(), uuid.New(), "self@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2, got %d", len(out))
	}
}

func TestFetch_PageCapExceeded(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	bw := bucketWindows(now)[2]
	loopingEvent := []*calendar.Event{makeEvent("loop", bw.Start.Format(time.RFC3339), "alice@example.com", "X", nil)}
	fakeLister := &fakeEventsLister{
		responses: map[int]map[string]bucketResponse{
			2: {
				"":     {items: loopingEvent, nextPageToken: "loop"},
				"loop": {items: loopingEvent, nextPageToken: "loop"}, // never terminates
			},
		},
	}
	// Other 5 buckets return empty pages — they finish fast.
	for i := 0; i < 6; i++ {
		if i == 2 {
			continue
		}
		fakeLister.responses[i] = map[string]bucketResponse{"": {items: nil, nextPageToken: ""}}
	}
	svc := newTestBackfillService(t, &fakeCalendarSvc{}, fakeLister, now)

	_, err := svc.FetchPast30DaysWithAttendees(context.Background(), uuid.New(), "self@example.com")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	be, ok := err.(*BackfillError)
	if !ok {
		t.Fatalf("want *BackfillError, got %T", err)
	}
	if be.Class != ClassPermanent || be.Bucket != 2 {
		t.Fatalf("want ClassPermanent bucket=2, got %#v", be)
	}
}

func TestFetch_AllDayEventsSkipped(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	bw := bucketWindows(now)[0]
	allDay := &calendar.Event{
		Id:    "allday",
		Start: &calendar.EventDateTime{Date: "2026-05-08"}, // DateTime empty
		End:   &calendar.EventDateTime{Date: "2026-05-09"},
	}
	timed1 := makeEvent("timed-1", bw.Start.Format(time.RFC3339), "alice@example.com", "X", nil)
	timed2 := makeEvent("timed-2", bw.Start.Add(time.Hour).Format(time.RFC3339), "alice@example.com", "X", nil)

	fakeLister := &fakeEventsLister{
		responses: map[int]map[string]bucketResponse{
			0: {"": {items: []*calendar.Event{timed1, allDay, timed2}, nextPageToken: ""}},
		},
	}
	for i := 1; i < 6; i++ {
		fakeLister.responses[i] = map[string]bucketResponse{"": {items: nil, nextPageToken: ""}}
	}
	svc := newTestBackfillService(t, &fakeCalendarSvc{}, fakeLister, now)

	out, err := svc.FetchPast30DaysWithAttendees(context.Background(), uuid.New(), "self@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 (all-day silently skipped), got %d: %#v", len(out), out)
	}
	for _, ev := range out {
		if ev.EventID == "allday" {
			t.Fatal("all-day event leaked through")
		}
	}
}

func TestFetch_RecurringPassthrough(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	bw := bucketWindows(now)[0]
	ev := makeEvent("instance-1", bw.Start.Format(time.RFC3339), "alice@example.com", "X", nil)
	ev.RecurringEventId = "series-abc"
	fakeLister := &fakeEventsLister{
		responses: map[int]map[string]bucketResponse{
			0: {"": {items: []*calendar.Event{ev}, nextPageToken: ""}},
		},
	}
	for i := 1; i < 6; i++ {
		fakeLister.responses[i] = map[string]bucketResponse{"": {items: nil, nextPageToken: ""}}
	}
	svc := newTestBackfillService(t, &fakeCalendarSvc{}, fakeLister, now)

	out, err := svc.FetchPast30DaysWithAttendees(context.Background(), uuid.New(), "self@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].RecurringEventID != "series-abc" {
		t.Fatalf("want RecurringEventID=series-abc, got %#v", out)
	}
}

func TestFetch_DeclinedAttendeesKept_E2E(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	bw := bucketWindows(now)[0]
	ev := makeEvent("e1", bw.Start.Format(time.RFC3339), "alice@example.com", "X",
		[]*calendar.EventAttendee{
			{Email: "carol@example.com", ResponseStatus: "declined"},
			{Email: "dan@example.com", ResponseStatus: "accepted"},
		})
	fakeLister := &fakeEventsLister{
		responses: map[int]map[string]bucketResponse{
			0: {"": {items: []*calendar.Event{ev}, nextPageToken: ""}},
		},
	}
	for i := 1; i < 6; i++ {
		fakeLister.responses[i] = map[string]bucketResponse{"": {items: nil, nextPageToken: ""}}
	}
	svc := newTestBackfillService(t, &fakeCalendarSvc{}, fakeLister, now)

	out, err := svc.FetchPast30DaysWithAttendees(context.Background(), uuid.New(), "self@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || len(out[0].Attendees) != 2 {
		t.Fatalf("want 2 attendees both kept, got %#v", out)
	}
	// Sort by email so order is stable across map-iteration whims.
	emails := []string{out[0].Attendees[0].Email, out[0].Attendees[1].Email}
	statuses := map[string]string{
		out[0].Attendees[0].Email: out[0].Attendees[0].ResponseStatus,
		out[0].Attendees[1].Email: out[0].Attendees[1].ResponseStatus,
	}
	sort.Strings(emails)
	if statuses["carol@example.com"] != "declined" || statuses["dan@example.com"] != "accepted" {
		t.Fatalf("ResponseStatus passthrough broken: %#v", statuses)
	}
}

func TestFetch_FirstFailureWins(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	fakeLister := &fakeEventsLister{
		responses: map[int]map[string]bucketResponse{
			0: {"": {err: &googleapi.Error{Code: 401}, delay: 0}},
			4: {"": {err: &googleapi.Error{Code: 500}, delay: 50 * time.Millisecond}},
		},
	}
	for _, i := range []int{1, 2, 3, 5} {
		fakeLister.responses[i] = map[string]bucketResponse{"": {items: nil, nextPageToken: ""}}
	}
	svc := newTestBackfillService(t, &fakeCalendarSvc{}, fakeLister, now)

	_, err := svc.FetchPast30DaysWithAttendees(context.Background(), uuid.New(), "self@example.com")
	be, ok := err.(*BackfillError)
	if !ok {
		t.Fatalf("want *BackfillError, got %T (%v)", err, err)
	}
	if be.Class != ClassOAuthExpired {
		t.Fatalf("first failure should be 401 (OAuthExpired); got %v", be.Class)
	}
}

func TestFetch_ProbeFailure_OAuthExpired(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	probeErr := errors.New("oauth2: cannot fetch token: invalid_grant")
	fakeLister := &fakeEventsLister{responses: map[int]map[string]bucketResponse{}}
	svc := newTestBackfillService(t, &fakeCalendarSvc{probeErr: probeErr}, fakeLister, now)

	_, err := svc.FetchPast30DaysWithAttendees(context.Background(), uuid.New(), "self@example.com")
	be, ok := err.(*BackfillError)
	if !ok {
		t.Fatalf("want *BackfillError, got %T", err)
	}
	if be.Class != ClassOAuthExpired || be.Bucket != -1 {
		t.Fatalf("want oauth_expired bucket=-1 (probe), got %#v", be)
	}
}

func TestFetch_WindowSnapshot_Independent(t *testing.T) {
	// Two calls with different `now` values must produce different bucket
	// boundaries — proves the snapshot is per-call, not shared. We extract
	// the WINDOW END from each call by finding the maximum TimeMax across
	// the 6 requests (which is bucket 5's End == now per bucketWindows).
	// Using a max-scan rather than "last request" is necessary because the
	// 6 bucket goroutines are dispatched via errgroup and complete in
	// non-deterministic order.
	t1 := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(5 * time.Millisecond)

	maxTimeMax := func(reqs []listRequest) time.Time {
		var m time.Time
		for _, r := range reqs {
			if r.TimeMax.After(m) {
				m = r.TimeMax
			}
		}
		return m
	}

	fakeLister := &fakeEventsLister{responses: map[int]map[string]bucketResponse{}}
	for i := 0; i < 6; i++ {
		fakeLister.responses[i] = map[string]bucketResponse{"": {items: nil, nextPageToken: ""}}
	}
	svc := newTestBackfillService(t, &fakeCalendarSvc{}, fakeLister, t1)

	_, _ = svc.FetchPast30DaysWithAttendees(context.Background(), uuid.New(), "self@example.com")
	firstWindowEnd := maxTimeMax(fakeLister.requests)

	// Re-point the now func and rebuild the lister's bucketStarts.
	svc.SetNowFunc(func() time.Time { return t2 })
	bws := bucketWindows(t2)
	for i, b := range bws {
		fakeLister.bucketStarts[i] = b.Start
	}
	fakeLister.requests = nil

	_, _ = svc.FetchPast30DaysWithAttendees(context.Background(), uuid.New(), "self@example.com")
	secondWindowEnd := maxTimeMax(fakeLister.requests)

	if firstWindowEnd.Equal(secondWindowEnd) {
		t.Fatalf("snapshot leaked: both calls saw TimeMax=%v", firstWindowEnd)
	}
	if d := secondWindowEnd.Sub(firstWindowEnd); d != 5*time.Millisecond {
		t.Fatalf("expected 5ms gap, got %v", d)
	}
}

// TestFetch_FETCH01_NoOAuthConfigInstantiation enforces FETCH-01: the
// fetcher must reuse existing OAuth tokens, never instantiate a new
// oauth2.Config or call getOAuthConfig. Implemented as a source-grep
// test so it cannot drift silently if someone copy-pastes calendar.go
// internals into the backfill file.
func TestFetch_FETCH01_NoOAuthConfigInstantiation(t *testing.T) {
	src, err := os.ReadFile("calendar_backfill.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	bad := regexp.MustCompile(`getOAuthConfig|oauth2\.Config\{|oauth2\.NewClient`)
	if bad.Match(src) {
		t.Fatalf("FETCH-01 violation: calendar_backfill.go references forbidden OAuth-instantiation symbols")
	}
}

// TestFetch_HeavyUser_P95Under15s — Phase 5 success criterion 5.
func TestFetch_HeavyUser_P95Under15s(t *testing.T) {
	if testing.Short() {
		t.Skip("perf test skipped under -short")
	}
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	bws := bucketWindows(now)

	// 240 events distributed 40 per bucket × 6 buckets, 22 attendees each.
	const eventsPerBucket = 40
	const attendeesPerEvent = 22
	fakeLister := &fakeEventsLister{
		responses:    map[int]map[string]bucketResponse{},
		defaultDelay: 80 * time.Millisecond, // realistic Google round-trip
	}
	for b := 0; b < 6; b++ {
		evs := make([]*calendar.Event, eventsPerBucket)
		for j := 0; j < eventsPerBucket; j++ {
			atts := make([]*calendar.EventAttendee, attendeesPerEvent)
			for a := 0; a < attendeesPerEvent; a++ {
				atts[a] = &calendar.EventAttendee{
					Email:          fmt.Sprintf("user%d@example.com", a),
					ResponseStatus: "accepted",
				}
			}
			evs[j] = makeEvent(
				fmt.Sprintf("b%d-e%d", b, j),
				bws[b].Start.Add(time.Duration(j)*5*time.Minute).Format(time.RFC3339),
				"alice@example.com",
				"Heavy",
				atts,
			)
		}
		fakeLister.responses[b] = map[string]bucketResponse{"": {items: evs, nextPageToken: ""}}
	}
	svc := newTestBackfillService(t, &fakeCalendarSvc{}, fakeLister, now)

	const runs = 20
	durations := make([]time.Duration, runs)
	for i := 0; i < runs; i++ {
		// Reset request log between runs so the lister's bookkeeping doesn't grow.
		fakeLister.requests = nil
		start := time.Now()
		out, err := svc.FetchPast30DaysWithAttendees(context.Background(), uuid.New(), "self@example.com")
		durations[i] = time.Since(start)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if len(out) != 240 {
			t.Fatalf("run %d: want 240 events, got %d", i, len(out))
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95 := durations[18] // 95th percentile of 20 samples
	if p95 > 15*time.Second {
		t.Fatalf("p95 = %v, want ≤ 15s", p95)
	}
	t.Logf("p95 = %v across %d runs", p95, runs)
}
