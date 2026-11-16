package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ensureProfileExists is a defensive verifier that confirms the
// public.profiles row created via the handle_new_user() trigger landed
// before the test does anything that depends on the
// teams.user_id → profiles(id) FK. If the trigger ever changes (or the
// test DB is bootstrapped without it), tests fail loudly with a clear
// message rather than silently violating the FK.
func ensureProfileExists(t *testing.T, ctx context.Context, userID uuid.UUID) {
	t.Helper()
	var exists bool
	err := DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.profiles WHERE id = $1)", userID).Scan(&exists)
	if err != nil {
		t.Fatalf("check profile exists: %v", err)
	}
	if !exists {
		t.Fatalf("profile row missing for user %s — handle_new_user trigger broken or not run", userID)
	}
}

// ============================================================================
// (a) Duplicate (user_id, name) on teams returns unique-violation
// ============================================================================

func TestIntegration_Teams_UniqueUserIDName(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)

	ctx := context.Background()
	userID := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, userID)
	repo := NewTeamRepository()

	first := &models.Team{UserID: userID, Name: "Backend"}
	if err := repo.CreateTeam(ctx, first); err != nil {
		t.Fatalf("first CreateTeam: %v", err)
	}
	t.Cleanup(func() { repo.DeleteTeam(context.Background(), userID, first.ID) })

	second := &models.Team{UserID: userID, Name: "Backend"}
	err := repo.CreateTeam(ctx, second)
	if err == nil {
		t.Cleanup(func() { repo.DeleteTeam(context.Background(), userID, second.ID) })
		t.Fatalf("expected unique-violation error on duplicate (user_id, name); got nil")
	}
	if !IsUniqueViolation(err, "teams_user_id_name_unique") {
		t.Fatalf("expected IsUniqueViolation(err, \"teams_user_id_name_unique\")=true; err=%v", err)
	}
}

// ============================================================================
// (b) Duplicate (team_id, email) on team_members returns unique-violation
// ============================================================================

func TestIntegration_TeamMembers_UniqueTeamIDEmail(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)

	ctx := context.Background()
	userID := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, userID)
	repo := NewTeamRepository()

	team := &models.Team{UserID: userID, Name: "Engineering"}
	if err := repo.CreateTeam(ctx, team); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	t.Cleanup(func() { repo.DeleteTeam(context.Background(), userID, team.ID) })

	first := &models.TeamMember{TeamID: team.ID, Name: "Ada Lovelace", Email: "ada@example.com"}
	if err := repo.AddMember(ctx, first); err != nil {
		t.Fatalf("first AddMember: %v", err)
	}

	second := &models.TeamMember{TeamID: team.ID, Name: "Ada L.", Email: "ada@example.com"}
	err := repo.AddMember(ctx, second)
	if err == nil {
		t.Fatalf("expected unique-violation error on duplicate (team_id, email); got nil")
	}
	if !IsUniqueViolation(err, "team_members_team_id_email_unique") {
		t.Fatalf("expected IsUniqueViolation(err, \"team_members_team_id_email_unique\")=true; err=%v", err)
	}
}

// ============================================================================
// (c) Deleting a team CASCADEs to team_members
// ============================================================================

func TestIntegration_Teams_DeleteCascadesToMembers(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)

	ctx := context.Background()
	userID := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, userID)
	repo := NewTeamRepository()

	team := &models.Team{UserID: userID, Name: "Marketing"}
	if err := repo.CreateTeam(ctx, team); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	for i, email := range []string{"a@x.com", "b@x.com", "c@x.com"} {
		m := &models.TeamMember{TeamID: team.ID, Name: "Member " + string(rune('A'+i)), Email: email}
		if err := repo.AddMember(ctx, m); err != nil {
			t.Fatalf("AddMember %d: %v", i, err)
		}
	}

	deleted, err := repo.DeleteTeam(ctx, userID, team.ID)
	if err != nil {
		t.Fatalf("DeleteTeam: %v", err)
	}
	if !deleted {
		t.Fatalf("expected deleted=true")
	}

	var count int
	if err := DB.QueryRow(ctx,
		"SELECT COUNT(*) FROM public.team_members WHERE team_id = $1", team.ID,
	).Scan(&count); err != nil {
		t.Fatalf("COUNT after delete: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 members after team delete (CASCADE); got %d", count)
	}
}

// ============================================================================
// (d) ListTeamsForUser returns only the requesting user's teams
// ============================================================================

func TestIntegration_Teams_PerUserScoping(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)

	ctx := context.Background()
	userA := createTestUser(t, ctx)
	userB := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, userA)
	ensureProfileExists(t, ctx, userB)
	repo := NewTeamRepository()

	teamA := &models.Team{UserID: userA, Name: "Shared Name"}
	teamB := &models.Team{UserID: userB, Name: "Shared Name"}
	if err := repo.CreateTeam(ctx, teamA); err != nil {
		t.Fatalf("CreateTeam A: %v", err)
	}
	t.Cleanup(func() { repo.DeleteTeam(context.Background(), userA, teamA.ID) })
	if err := repo.CreateTeam(ctx, teamB); err != nil {
		t.Fatalf("CreateTeam B: %v", err)
	}
	t.Cleanup(func() { repo.DeleteTeam(context.Background(), userB, teamB.ID) })

	listA, err := repo.ListTeamsForUser(ctx, userA)
	if err != nil {
		t.Fatalf("ListTeamsForUser A: %v", err)
	}
	if len(listA) != 1 {
		t.Fatalf("expected 1 team for userA; got %d", len(listA))
	}
	if listA[0].UserID != userA {
		t.Fatalf("expected team.UserID == userA; got %s", listA[0].UserID)
	}
	if listA[0].ID == teamB.ID {
		t.Fatalf("userA's list contained userB's team — per-user scoping broken")
	}
	if listA[0].ID != teamA.ID {
		t.Fatalf("expected userA's list to contain teamA.ID=%s; got %s", teamA.ID, listA[0].ID)
	}
}

// ============================================================================
// Bonus: TeamDetectionRun round-trip
// ============================================================================

func TestIntegration_TeamDetectionRun_RoundTrip(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)

	ctx := context.Background()
	userID := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, userID)
	repo := NewTeamRepository()

	params := json.RawMessage(`{"k":3,"weight_decay":"1/(n-1)"}`)
	algoV := "cpm-k3-v1"
	inMeet := 42
	inAtt := 17
	run := &models.TeamDetectionRun{
		UserID:             userID,
		StartedAt:          time.Now().UTC(),
		Status:             "running",
		InputMeetingCount:  &inMeet,
		InputAttendeeCount: &inAtt,
		ParametersJSONB:    params,
		AlgorithmVersion:   &algoV,
	}
	if err := repo.CreateDetectionRun(ctx, run); err != nil {
		t.Fatalf("CreateDetectionRun: %v", err)
	}
	// Cleanup: explicit row delete (also covered by user-CASCADE, but
	// explicit makes the test resilient to test-runner ordering).
	t.Cleanup(func() {
		DB.Exec(context.Background(), "DELETE FROM public.team_detection_runs WHERE id = $1", run.ID)
	})

	out := 2
	// Phase 6 gap-closure (migration 018) extended the signature with five
	// new metric pointers + stage durations JSONB. This integration test
	// pre-dates Phase 6 and only asserts the legacy success-row shape;
	// the new metrics are passed as nil/zero to preserve the prior
	// assertion surface (latest.OutputTeamCount remains the focus).
	if err := repo.UpdateDetectionRunFinish(ctx, run.ID, "success", nil, nil, &out, nil, nil, nil, nil); err != nil {
		t.Fatalf("UpdateDetectionRunFinish: %v", err)
	}

	latest, err := repo.GetLatestDetectionRun(ctx, userID)
	if err != nil {
		t.Fatalf("GetLatestDetectionRun: %v", err)
	}
	if latest.Status != "success" {
		t.Fatalf("expected status=success; got %s", latest.Status)
	}
	if latest.OutputTeamCount == nil || *latest.OutputTeamCount != 2 {
		t.Fatalf("expected output_team_count=2; got %v", latest.OutputTeamCount)
	}
	if latest.FinishedAt == nil {
		t.Fatalf("expected finished_at to be set")
	}
	var got map[string]any
	if err := json.Unmarshal(latest.ParametersJSONB, &got); err != nil {
		t.Fatalf("unmarshal parameters_jsonb: %v", err)
	}
	if got["k"].(float64) != 3 {
		t.Fatalf("expected k=3 in parameters_jsonb; got %v", got["k"])
	}
}

// ============================================================================
// Bonus: GetTeam for a foreign user returns ErrNoRows
// ============================================================================

func TestIntegration_Teams_GetTeam_ForeignUserReturnsNoRows(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)

	ctx := context.Background()
	userA := createTestUser(t, ctx)
	userB := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, userA)
	ensureProfileExists(t, ctx, userB)
	repo := NewTeamRepository()

	teamA := &models.Team{UserID: userA, Name: "Owned by A"}
	if err := repo.CreateTeam(ctx, teamA); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	t.Cleanup(func() { repo.DeleteTeam(context.Background(), userA, teamA.ID) })

	_, err := repo.GetTeam(ctx, userB, teamA.ID)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected pgx.ErrNoRows for foreign-user GetTeam; got %v", err)
	}
}

// ============================================================================
// Bonus: IsUniqueViolation false-positive guards
// ============================================================================

func TestIntegration_IsUniqueViolation_NotAUniqueError(t *testing.T) {
	// Pure unit test — does not need INTEGRATION_TEST or DB connection,
	// so deliberately omits skipIfNoIntegration. Always runs in CI.
	if IsUniqueViolation(errors.New("plain error"), "") {
		t.Fatal("plain error must not be reported as unique-violation")
	}
	if IsUniqueViolation(nil, "") {
		t.Fatal("nil error must not be reported as unique-violation")
	}
	if IsUniqueViolation(errors.New("plain error"), "teams_user_id_name_unique") {
		t.Fatal("plain error must not be reported as unique-violation even with constraint name")
	}
}

// ============================================================================
// Phase 7 — SaveTeamsAndMembers + ListTeamsByUserWithMembers + DeleteMemberScoped
//           + UpdateOnboardingStatus + WithDetectLock integration tests
// ============================================================================

func TestSaveTeamsAndMembers_HappyPath(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)
	ctx := context.Background()

	userID := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, userID)
	repo := NewTeamRepository()

	teamA := models.Team{ID: uuid.New(), UserID: userID, Name: "Backend"}
	teamB := models.Team{ID: uuid.New(), UserID: userID, Name: "Design"}
	members := []models.TeamMember{
		{ID: uuid.New(), TeamID: teamA.ID, Name: "Ada", Email: "ada@example.com"},
		{ID: uuid.New(), TeamID: teamA.ID, Name: "Bea", Email: "bea@example.com"},
		{ID: uuid.New(), TeamID: teamB.ID, Name: "Cem", Email: "cem@example.com"},
	}

	if err := repo.SaveTeamsAndMembers(ctx, userID, []models.Team{teamA, teamB}, members); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Status flipped?
	var status string
	if err := DB.QueryRow(ctx, `SELECT teams_onboarding_status FROM public.profiles WHERE id=$1`, userID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != models.TeamsOnboardingStatusCompleted {
		t.Errorf("status: want completed, got %q", status)
	}

	// Teams + members present?
	listed, err := repo.ListTeamsByUserWithMembers(ctx, userID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("teams: want 2, got %d", len(listed))
	}
}

func TestSaveTeamsAndMembers_AdditiveOnCompleted(t *testing.T) {
	// v1.1 post-onboarding redetect: when a user is already onboarded
	// (status='completed'), SaveTeamsAndMembers must additively insert
	// the new teams instead of failing. Status stays 'completed'.
	skipIfNoIntegration(t)
	setupTestDB(t)
	ctx := context.Background()

	userID := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, userID)
	repo := NewTeamRepository()

	// Pre-flip status to simulate a user who already finished onboarding.
	if _, err := DB.Exec(ctx, `UPDATE public.profiles SET teams_onboarding_status='completed' WHERE id=$1`, userID); err != nil {
		t.Fatalf("preflip: %v", err)
	}

	team := models.Team{ID: uuid.New(), UserID: userID, Name: "Post-onboarding-added"}
	if err := repo.SaveTeamsAndMembers(ctx, userID, []models.Team{team}, nil); err != nil {
		t.Fatalf("err: want success on already-completed profile, got %v", err)
	}

	// The team must have been inserted (the additive contract).
	var count int
	if err := DB.QueryRow(ctx, `SELECT count(*) FROM public.teams WHERE user_id=$1`, userID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("teams inserted on already-completed: want 1 (additive), got %d", count)
	}

	// Status must remain 'completed' (no-op flip per the CASE expression).
	var status string
	if err := DB.QueryRow(ctx, `SELECT teams_onboarding_status FROM public.profiles WHERE id=$1`, userID).Scan(&status); err != nil {
		t.Fatalf("status query: %v", err)
	}
	if status != "completed" {
		t.Errorf("status after additive save: want 'completed', got %q", status)
	}
}

func TestSaveTeamsAndMembers_OverridesUserID(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)
	ctx := context.Background()

	realUser := createTestUser(t, ctx)
	attacker := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, realUser)
	ensureProfileExists(t, ctx, attacker)
	repo := NewTeamRepository()

	// Attacker-controlled team body carries attacker's UserID.
	poisoned := models.Team{ID: uuid.New(), UserID: attacker, Name: "Smuggled"}
	if err := repo.SaveTeamsAndMembers(ctx, realUser, []models.Team{poisoned}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	// The row must be owned by realUser, not attacker.
	var owner uuid.UUID
	if err := DB.QueryRow(ctx, `SELECT user_id FROM public.teams WHERE id=$1`, poisoned.ID).Scan(&owner); err != nil {
		t.Fatalf("read: %v", err)
	}
	if owner != realUser {
		t.Errorf("user_id override broke: want %s, got %s", realUser, owner)
	}
}

func TestSaveTeamsAndMembers_RejectsOrphanMember(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)
	ctx := context.Background()

	userID := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, userID)
	repo := NewTeamRepository()

	team := models.Team{ID: uuid.New(), UserID: userID, Name: "Real"}
	orphan := models.TeamMember{ID: uuid.New(), TeamID: uuid.New(), Email: "ghost@example.com", Name: "Ghost"}

	err := repo.SaveTeamsAndMembers(ctx, userID, []models.Team{team}, []models.TeamMember{orphan})
	if err == nil || !strings.Contains(err.Error(), "unknown team id") {
		t.Fatalf("err: want unknown-team-id, got %v", err)
	}

	// Tx rolled back — no team row.
	var count int
	if err := DB.QueryRow(ctx, `SELECT count(*) FROM public.teams WHERE user_id=$1`, userID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("rollback failed: want 0 teams, got %d", count)
	}
}

func TestListTeamsByUserWithMembers_NestedShape(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)
	ctx := context.Background()

	userID := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, userID)
	repo := NewTeamRepository()

	teamA := models.Team{ID: uuid.New(), UserID: userID, Name: "A"}
	teamB := models.Team{ID: uuid.New(), UserID: userID, Name: "B"}
	members := []models.TeamMember{
		{ID: uuid.New(), TeamID: teamA.ID, Name: "x", Email: "x@e.com"},
		{ID: uuid.New(), TeamID: teamA.ID, Name: "y", Email: "y@e.com"},
		{ID: uuid.New(), TeamID: teamA.ID, Name: "z", Email: "z@e.com"},
	}
	if err := repo.SaveTeamsAndMembers(ctx, userID, []models.Team{teamA, teamB}, members); err != nil {
		t.Fatalf("setup save: %v", err)
	}

	got, err := repo.ListTeamsByUserWithMembers(ctx, userID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("teams: want 2, got %d", len(got))
	}
	// Team A: 3 members.
	var foundA, foundB *TeamWithMembers
	for i := range got {
		switch got[i].Name {
		case "A":
			foundA = &got[i]
		case "B":
			foundB = &got[i]
		}
	}
	if foundA == nil || len(foundA.Members) != 3 {
		t.Errorf("A members: want 3, got %v", foundA)
	}
	if foundB == nil || foundB.Members == nil {
		t.Errorf("B.Members must be non-nil empty slice, got %#v", foundB)
	}
	if foundB != nil && len(foundB.Members) != 0 {
		t.Errorf("B members: want 0, got %d", len(foundB.Members))
	}

	// JSON shape: empty Members must encode as [] not null.
	payload, err := json.Marshal(foundB)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(payload), `"members":[]`) {
		t.Errorf("empty members must marshal as []: %s", payload)
	}
}

func TestListTeamsByUserWithMembers_ScopedByUser(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)
	ctx := context.Background()

	userA := createTestUser(t, ctx)
	userB := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, userA)
	ensureProfileExists(t, ctx, userB)
	repo := NewTeamRepository()

	teamA := models.Team{ID: uuid.New(), UserID: userA, Name: "A-team"}
	if err := repo.SaveTeamsAndMembers(ctx, userA, []models.Team{teamA}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := repo.ListTeamsByUserWithMembers(ctx, userB)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("cross-user leak: userB sees %d teams (should be 0)", len(got))
	}
}

func TestDeleteMemberScoped_OwnerDeletes(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)
	ctx := context.Background()

	userID := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, userID)
	repo := NewTeamRepository()

	team := models.Team{ID: uuid.New(), UserID: userID, Name: "T"}
	member := models.TeamMember{ID: uuid.New(), TeamID: team.ID, Name: "n", Email: "n@e.com"}
	if err := repo.SaveTeamsAndMembers(ctx, userID, []models.Team{team}, []models.TeamMember{member}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	deleted, err := repo.DeleteMemberScoped(ctx, userID, member.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted {
		t.Fatal("delete: want true, got false")
	}
}

func TestDeleteMemberScoped_OtherUserBlocked(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)
	ctx := context.Background()

	userA := createTestUser(t, ctx)
	userB := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, userA)
	ensureProfileExists(t, ctx, userB)
	repo := NewTeamRepository()

	team := models.Team{ID: uuid.New(), UserID: userA, Name: "T"}
	member := models.TeamMember{ID: uuid.New(), TeamID: team.ID, Name: "n", Email: "n@e.com"}
	if err := repo.SaveTeamsAndMembers(ctx, userA, []models.Team{team}, []models.TeamMember{member}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	deleted, err := repo.DeleteMemberScoped(ctx, userB, member.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted {
		t.Error("IDOR: userB deleted userA's member")
	}
	// Row still present?
	var count int
	if err := DB.QueryRow(ctx, `SELECT count(*) FROM public.team_members WHERE id=$1`, member.ID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("member row should still exist: count=%d", count)
	}
}

func TestDeleteMemberScoped_NotFound(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)
	ctx := context.Background()

	userID := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, userID)
	repo := NewTeamRepository()

	deleted, err := repo.DeleteMemberScoped(ctx, userID, uuid.New())
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted {
		t.Error("delete of nonexistent member returned true")
	}
}

func TestUpdateOnboardingStatus_Skipped(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)
	ctx := context.Background()

	userID := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, userID)
	repo := NewTeamRepository()

	if err := repo.UpdateOnboardingStatus(ctx, userID, models.TeamsOnboardingStatusSkipped); err != nil {
		t.Fatalf("flip: %v", err)
	}
	var status string
	if err := DB.QueryRow(ctx, `SELECT teams_onboarding_status FROM public.profiles WHERE id=$1`, userID).Scan(&status); err != nil {
		t.Fatalf("read: %v", err)
	}
	if status != models.TeamsOnboardingStatusSkipped {
		t.Errorf("status: want skipped, got %q", status)
	}
}

func TestUpdateOnboardingStatus_AlreadyCompleted(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)
	ctx := context.Background()

	userID := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, userID)
	repo := NewTeamRepository()

	if _, err := DB.Exec(ctx, `UPDATE public.profiles SET teams_onboarding_status='completed' WHERE id=$1`, userID); err != nil {
		t.Fatalf("preflip: %v", err)
	}
	err := repo.UpdateOnboardingStatus(ctx, userID, models.TeamsOnboardingStatusSkipped)
	if !errors.Is(err, ErrAlreadyCompleted) {
		t.Errorf("err: want ErrAlreadyCompleted, got %v", err)
	}
}

func TestWithDetectLock_ConcurrentCollision(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)
	ctx := context.Background()

	userID := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, userID)
	repo := NewTeamRepository()

	started := make(chan struct{})
	release := make(chan struct{})
	holdErr := make(chan error, 1)
	rivalErr := make(chan error, 1)

	go func() {
		holdErr <- repo.WithDetectLock(ctx, userID, func(ctx context.Context) error {
			close(started)
			<-release // hold the lock until the rival has tried + failed
			return nil
		})
	}()

	// Wait for the holder to actually enter fn (lock acquired).
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("holder never started")
	}

	go func() {
		rivalErr <- repo.WithDetectLock(ctx, userID, func(ctx context.Context) error {
			t.Error("rival fn should NOT run while holder owns the lock")
			return nil
		})
	}()

	// Give the rival a moment to attempt + fail.
	select {
	case e := <-rivalErr:
		if !errors.Is(e, ErrDetectAlreadyRunning) {
			t.Errorf("rival err: want ErrDetectAlreadyRunning, got %v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rival never returned")
	}

	close(release) // release the holder

	if e := <-holdErr; e != nil {
		t.Errorf("holder err: %v", e)
	}

	// After release, a fresh call should succeed.
	if err := repo.WithDetectLock(ctx, userID, func(ctx context.Context) error { return nil }); err != nil {
		t.Errorf("post-release: %v", err)
	}
}

func TestWithDetectLock_DifferentUsers(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)
	ctx := context.Background()

	userA := createTestUser(t, ctx)
	userB := createTestUser(t, ctx)
	ensureProfileExists(t, ctx, userA)
	ensureProfileExists(t, ctx, userB)
	repo := NewTeamRepository()

	var wg sync.WaitGroup
	wg.Add(2)
	errA := make(chan error, 1)
	errB := make(chan error, 1)

	go func() {
		defer wg.Done()
		errA <- repo.WithDetectLock(ctx, userA, func(ctx context.Context) error {
			time.Sleep(50 * time.Millisecond) // overlap with B
			return nil
		})
	}()
	go func() {
		defer wg.Done()
		errB <- repo.WithDetectLock(ctx, userB, func(ctx context.Context) error {
			time.Sleep(50 * time.Millisecond)
			return nil
		})
	}()
	wg.Wait()
	if e := <-errA; e != nil {
		t.Errorf("userA: %v", e)
	}
	if e := <-errB; e != nil {
		t.Errorf("userB: %v", e)
	}
}
