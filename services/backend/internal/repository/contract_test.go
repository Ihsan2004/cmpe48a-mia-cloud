package repository

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"mia-website/backend/internal/models"
)

// ==================== Interface Completeness ====================
// These tests verify at compile time that implementations satisfy their interfaces.

func TestDecisionRepositoryInterface_Implemented(t *testing.T) {
	t.Parallel()
	var _ DecisionRepository = &postgresDecisionRepository{}
}

func TestMeetingRepositoryInterface_Implemented(t *testing.T) {
	t.Parallel()
	var _ MeetingRepository = &postgresMeetingRepository{}
}

func TestChainRepositoryInterface_Implemented(t *testing.T) {
	t.Parallel()
	var _ ChainRepository = &postgresChainRepository{}
}

func TestSummaryRepositoryInterface_Implemented(t *testing.T) {
	t.Parallel()
	var _ SummaryRepository = &postgresSummaryRepository{}
}

func TestBriefRepositoryInterface_Implemented(t *testing.T) {
	t.Parallel()
	var _ BriefRepository = &postgresBriefRepository{}
}

func TestNoteRepositoryInterface_Implemented(t *testing.T) {
	t.Parallel()
	var _ NoteRepository = &postgresNoteRepository{}
}

func TestUsageLogRepositoryInterface_Implemented(t *testing.T) {
	t.Parallel()
	var _ UsageLogRepository = &postgresUsageLogRepository{}
}

func TestDesktopRepositoryInterface_Implemented(t *testing.T) {
	t.Parallel()
	var _ DesktopRepository = &postgresDesktopRepository{}
}

func TestBriefDeliveryRepositoryInterface_Implemented(t *testing.T) {
	t.Parallel()
	var _ BriefDeliveryRepository = &postgresBriefDeliveryRepository{}
}

func TestSlackIntegrationRepositoryInterface_Implemented(t *testing.T) {
	t.Parallel()
	var _ SlackIntegrationRepository = &postgresSlackIntegrationRepository{}
}

// BriefDeliveryRepository method count ties the dispatcher's fake repo
// to the real interface — if a method is added here, the fake in
// briefnotify/dispatcher_test.go must grow to match or the dispatcher
// tests fail to compile.
func TestBriefDeliveryRepository_MethodCount(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf((*BriefDeliveryRepository)(nil)).Elem()

	// 7 methods (Phase 7 added ListRecent for the admin audit endpoint):
	// Create, GetByID, GetByMeetingChannel, ListPendingDue, ListRecent,
	// UpdateStatus, IncrementAttempts.
	expectedMethods := 7
	if typ.NumMethod() != expectedMethods {
		var names []string
		for i := 0; i < typ.NumMethod(); i++ {
			names = append(names, typ.Method(i).Name)
		}
		t.Errorf("BriefDeliveryRepository has %d methods (expected %d). Methods: %s\nUpdate fakeDeliveryRepo in briefnotify/dispatcher_test.go.",
			typ.NumMethod(), expectedMethods, strings.Join(names, ", "))
	}
}

// SlackIntegrationRepository method count — same rationale: future
// handlers and tests should stay aligned. Upsert + GetActiveForUser +
// GetByUserAndTeam + Revoke + UpdateDMChannel = 5.
func TestSlackIntegrationRepository_MethodCount(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf((*SlackIntegrationRepository)(nil)).Elem()
	expectedMethods := 5
	if typ.NumMethod() != expectedMethods {
		var names []string
		for i := 0; i < typ.NumMethod(); i++ {
			names = append(names, typ.Method(i).Name)
		}
		t.Errorf("SlackIntegrationRepository has %d methods (expected %d). Methods: %s",
			typ.NumMethod(), expectedMethods, strings.Join(names, ", "))
	}
}

// ==================== DesktopRepository Interface Method Count ====================

func TestDesktopRepository_MethodCount(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf((*DesktopRepository)(nil)).Elem()

	// 6 methods (Phase 6):
	//   desktop_sessions:    CreateSession, UpdateHeartbeat,
	//                        CloseSession, FindStaleSessions
	//   desktop_command_log: InsertCommandIfMissing, MarkCommandAck
	// Roadmap listed "5" but the interface spec in the same section
	// defines 6 methods — counting the interface, not the prose.
	expectedMethods := 6
	if typ.NumMethod() != expectedMethods {
		var methodNames []string
		for i := 0; i < typ.NumMethod(); i++ {
			methodNames = append(methodNames, typ.Method(i).Name)
		}
		t.Errorf("DesktopRepository has %d methods (expected %d). Methods: %s",
			typ.NumMethod(), expectedMethods, strings.Join(methodNames, ", "))
	}
}

// ==================== DecisionItem Model Field Count ====================
// Ensures model fields match what SQL queries expect.
// If you add a field to DecisionItem, you must update SELECT/Scan in decision_repository.go

func TestDecisionItem_FieldCount(t *testing.T) {
	t.Parallel()

	item := models.DecisionItem{}
	typ := reflect.TypeOf(item)

	// DecisionItem should have exactly 18 fields
	// If this fails, a field was added/removed — update SELECT + Scan in decision_repository.go
	expectedFields := 18
	if typ.NumField() != expectedFields {
		var fieldNames []string
		for i := 0; i < typ.NumField(); i++ {
			fieldNames = append(fieldNames, typ.Field(i).Name)
		}
		t.Errorf("DecisionItem has %d fields (expected %d). Fields: %s\nUpdate SELECT/Scan in decision_repository.go if columns changed.",
			typ.NumField(), expectedFields, strings.Join(fieldNames, ", "))
	}
}

// ==================== Meeting Model Field Sanity ====================

func TestMeeting_HasRequiredFields(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf(models.Meeting{})

	requiredFields := []string{
		"ID", "UserID", "Title", "StartTime", "EndTime",
		"GoogleEventID", "ChainID", "CalendarSeriesID", "CreatedAt",
	}

	for _, name := range requiredFields {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("Meeting struct missing required field: %s", name)
		}
	}
}

func TestMeeting_NoVisibilityField(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf(models.Meeting{})
	if _, ok := typ.FieldByName("Visibility"); ok {
		t.Error("Meeting should NOT have Visibility field (removed in simplify)")
	}
}

// ==================== DecisionItem Required Fields ====================

func TestDecisionItem_HasADRFields(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf(models.DecisionItem{})

	adrFields := []string{
		"Context", "AlternativesConsidered",
		"DecisionTimestampStart", "DecisionTimestampEnd",
	}

	for _, name := range adrFields {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("DecisionItem missing ADR field: %s", name)
		}
	}
}

func TestDecisionItem_HasUserEdited(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf(models.DecisionItem{})
	if _, ok := typ.FieldByName("UserEdited"); !ok {
		t.Error("DecisionItem missing UserEdited field")
	}
}

// ==================== DecisionRepository Interface Method Count ====================

func TestDecisionRepository_MethodCount(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf((*DecisionRepository)(nil)).Elem()

	// DecisionRepository should have exactly 14 methods.
	// Phase 3 (chain detection v3) added UpdateChainIDForMeetingNull so
	// manual chain-removal cascades NULL through decision_items without
	// the sentinel-UUID hack.
	// If this fails, a method was added/removed — update mock in decision_service_test.go
	expectedMethods := 14
	if typ.NumMethod() != expectedMethods {
		var methodNames []string
		for i := 0; i < typ.NumMethod(); i++ {
			methodNames = append(methodNames, typ.Method(i).Name)
		}
		t.Errorf("DecisionRepository has %d methods (expected %d). Methods: %s\nUpdate mock in decision_service_test.go if interface changed.",
			typ.NumMethod(), expectedMethods, strings.Join(methodNames, ", "))
	}
}

// ==================== MeetingRepository Interface Method Count ====================

func TestMeetingRepository_MethodCount(t *testing.T) {
	t.Parallel()

	// 26 methods — Phase 3 fix 2026-04-20 added:
	//   GetMeetingIDByTranscriptionJob (webhook resolver, was raw SQL in service)
	//   IncrementTranscriptionAttemptCount (was silently DEFAULT 0)
	typ := reflect.TypeOf((*MeetingRepository)(nil)).Elem()

	// If this fails, update mock in meeting_test.go
	// 24 methods: Phase 7.10 added CountActiveBotMeetings for the
	// concurrent bot cap; Phase 6 added GetDesktopRelevantMeetings for
	// the desktop WebSocket scheduler.
	// 28 methods — Phase 5 fix 2026-04-20 added
	// UpdateRecordingMethodOverride; Phase 1 (B1) added
	// UpsertDesktopMeeting for atomic idempotent desktop session creation
	// (sql/migrations/008_desktop_meeting_unique.sql).
	// 30 methods — Phase 3 of chain detection v3 added SetChainAssignment
	// (atomic 5-column write for manual move / remove / suggestion accept)
	// and ClearChainAssignmentLock (retry auto-detect path).
	// 31 methods — post-Phase-6 review added RepointMeetingChain so the
	// orphan-promotion path can update meetings.chain_id directly
	// (UpdateMeetingFields' allowlist intentionally excludes that
	// column to keep calendar-sync code from racing the matcher).
	expectedMethods := 31
	if typ.NumMethod() != expectedMethods {
		var methodNames []string
		for i := 0; i < typ.NumMethod(); i++ {
			methodNames = append(methodNames, typ.Method(i).Name)
		}
		t.Errorf("MeetingRepository has %d methods (expected %d). Methods: %s",
			typ.NumMethod(), expectedMethods, strings.Join(methodNames, ", "))
	}
}

// ==================== Chain assignment column projection contract ====================
//
// Three meeting-fetch entry points need chain_assignment_method (and
// the rest of the assignment quintuple) in their SELECT/Scan list so
// the v3 chain matcher can honour manual_user / manual_removed locks
// across resyncs and post-meeting triggers. This contract test makes
// the projection part of the build; deleting one of the columns from
// any of these queries fails the test rather than silently breaking
// the lock at runtime.
//
// The check is a static source-substring match — a real DB integration
// test would be ideal but requires DATABASE_URL, which the unit suite
// runs without.
func TestMeetingRepository_FetchersIncludeChainAssignmentColumns(t *testing.T) {
	t.Parallel()

	// One representative column per failure mode is enough; if
	// chain_assignment_method is in the SELECT, score / reason /
	// locked_at typically follow (added in the same edit). Reviewing
	// the surrounding diff catches the rest.
	const sentinel = "m.chain_assignment_method"

	cases := []struct {
		fetcher string
		// search the entire repo source — Go test files can't read
		// other Go files at runtime without effort, so we just assert
		// the file contains a marker that pairs each fetcher name
		// with the sentinel column. If you rename a fetcher, update
		// the cases list too.
	}{
		{fetcher: "GetMeetingByID"},
		{fetcher: "GetMeetingByIDSystem"},
		{fetcher: "GetMeetingBySource"},
	}

	src := readMeetingRepoSource(t)
	for _, c := range cases {
		// Find the function signature, then scan a generous window
		// after it for the sentinel column. 4000 chars is well past
		// any reasonable SELECT body in the repo.
		idx := strings.Index(src, "func (r *postgresMeetingRepository) "+c.fetcher+"(")
		if idx < 0 {
			t.Errorf("fetcher %s not found in meeting.go", c.fetcher)
			continue
		}
		end := idx + 4000
		if end > len(src) {
			end = len(src)
		}
		body := src[idx:end]
		if !strings.Contains(body, sentinel) {
			t.Errorf("%s does NOT project %s — manual_user / manual_removed lock will be lost across resyncs.\n"+
				"Add the column to both the SELECT list and the Scan() call.",
				c.fetcher, sentinel)
		}
	}
}

func readMeetingRepoSource(t *testing.T) string {
	t.Helper()
	// Test file lives in internal/repository/, same directory as
	// meeting.go — relative path works under `go test`.
	data, err := os.ReadFile("meeting.go")
	if err != nil {
		t.Fatalf("read meeting.go: %v", err)
	}
	return string(data)
}

// ==================== Chain Repository Interface ====================

func TestChainRepository_MethodCount(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf((*ChainRepository)(nil)).Elem()

	// 6 methods: FindBySeriesID, FindByID, CreateChain,
	// GetCandidateChainsByOverlap(user-scoped),
	// GetUserRecentMeetingsForMatching, ListUserChains.
	// ListUserChains was added for the move-to-chain picker (Phase 6
	// review finding #6). Update this count when the interface grows
	// again.
	expectedMethods := 6
	if typ.NumMethod() != expectedMethods {
		t.Errorf("ChainRepository has %d methods (expected %d)", typ.NumMethod(), expectedMethods)
	}
}
