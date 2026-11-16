package teamssave

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// fakeRepo implements repository.TeamRepository for unit tests. Only the
// two methods Save uses (SaveTeamsAndMembers + ListTeamsByUserWithMembers)
// have real bodies; the rest panic if called (tests should never reach them).
type fakeRepo struct {
	saveErr    error
	listResult []repository.TeamWithMembers
	listErr    error

	// Captures for assertions.
	gotUserID  uuid.UUID
	gotTeams   []models.Team
	gotMembers []models.TeamMember
	saveCalls  int
}

func (f *fakeRepo) SaveTeamsAndMembers(ctx context.Context, userID uuid.UUID, teams []models.Team, members []models.TeamMember) error {
	f.saveCalls++
	f.gotUserID = userID
	f.gotTeams = teams
	f.gotMembers = members
	return f.saveErr
}

func (f *fakeRepo) ListTeamsByUserWithMembers(ctx context.Context, userID uuid.UUID) ([]repository.TeamWithMembers, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listResult, nil
}

// Unused — panic on accidental call.
func (f *fakeRepo) CreateTeam(ctx context.Context, t *models.Team) error                       { panic("unused") }
func (f *fakeRepo) ListTeamsForUser(ctx context.Context, u uuid.UUID) ([]models.Team, error)   { panic("unused") }
func (f *fakeRepo) GetTeam(ctx context.Context, u, t uuid.UUID) (*models.Team, error)          { panic("unused") }
func (f *fakeRepo) UpdateTeamName(ctx context.Context, u, t uuid.UUID, n string) (bool, error) { panic("unused") }
func (f *fakeRepo) DeleteTeam(ctx context.Context, u, t uuid.UUID) (bool, error)               { panic("unused") }
func (f *fakeRepo) AddMember(ctx context.Context, m *models.TeamMember) error                  { panic("unused") }
func (f *fakeRepo) ListMembersForTeam(ctx context.Context, t uuid.UUID) ([]models.TeamMember, error) {
	panic("unused")
}
func (f *fakeRepo) DeleteMember(ctx context.Context, m uuid.UUID) (bool, error) { panic("unused") }
func (f *fakeRepo) CreateDetectionRun(ctx context.Context, r *models.TeamDetectionRun) error {
	panic("unused")
}
func (f *fakeRepo) UpdateDetectionRunFinish(ctx context.Context, runID uuid.UUID, status string, inputAttendeeCount, inputEdgeCount, outputTeamCount, outputMemberCount, outputLargestTeamSize *int, stageDurationsJSONB json.RawMessage, errorMessage *string) error {
	panic("unused")
}
func (f *fakeRepo) GetLatestDetectionRun(ctx context.Context, u uuid.UUID) (*models.TeamDetectionRun, error) {
	panic("unused")
}
func (f *fakeRepo) DeleteMemberScoped(ctx context.Context, u, m uuid.UUID) (bool, error) {
	panic("unused")
}
func (f *fakeRepo) UpdateOnboardingStatus(ctx context.Context, u uuid.UUID, s string) error {
	panic("unused")
}
func (f *fakeRepo) WithDetectLock(ctx context.Context, u uuid.UUID, fn func(ctx context.Context) error) error {
	panic("unused")
}

// Compile-time conformance guard.
var _ repository.TeamRepository = (*fakeRepo)(nil)

func TestSave_HappyPath(t *testing.T) {
	f := &fakeRepo{listResult: []repository.TeamWithMembers{}}
	s := New(f)
	userID := uuid.New()

	title := "Engineer"
	resp, err := s.Save(context.Background(), userID, SaveRequest{
		Teams: []SaveTeam{
			{Name: "Backend", Members: []SaveMember{{Name: "Ada", Email: "ada@example.com", Title: &title}}},
			{Name: "Design", Members: []SaveMember{{Name: "Bea", Email: "bea@example.com"}}},
		},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if resp == nil || resp.Status != "completed" {
		t.Fatalf("status: want completed, got %+v", resp)
	}
	if f.saveCalls != 1 {
		t.Errorf("saveCalls: want 1, got %d", f.saveCalls)
	}
	if f.gotUserID != userID {
		t.Errorf("userID: want %s, got %s", userID, f.gotUserID)
	}
	if len(f.gotTeams) != 2 || len(f.gotMembers) != 2 {
		t.Fatalf("teams=%d members=%d", len(f.gotTeams), len(f.gotMembers))
	}
	// Confirm UserID was overwritten from JWT-derived userID (T-07-09).
	for _, tm := range f.gotTeams {
		if tm.UserID != userID {
			t.Errorf("team %q UserID: want %s, got %s", tm.Name, userID, tm.UserID)
		}
	}
}

func TestSave_WiresMemberTeamIDFromInsertedTeam(t *testing.T) {
	f := &fakeRepo{listResult: []repository.TeamWithMembers{}}
	s := New(f)
	userID := uuid.New()

	_, err := s.Save(context.Background(), userID, SaveRequest{
		Teams: []SaveTeam{
			{Name: "A", Members: []SaveMember{{Name: "m1", Email: "m1@e.com"}, {Name: "m2", Email: "m2@e.com"}}},
			{Name: "B", Members: []SaveMember{{Name: "m3", Email: "m3@e.com"}}},
		},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	teamIDs := map[uuid.UUID]string{f.gotTeams[0].ID: "A", f.gotTeams[1].ID: "B"}
	for _, m := range f.gotMembers {
		if _, ok := teamIDs[m.TeamID]; !ok {
			t.Errorf("member %q: TeamID %s not in inserted teams", m.Email, m.TeamID)
		}
	}
}

func TestSave_OverridesCallerSuppliedTeamID(t *testing.T) {
	f := &fakeRepo{listResult: []repository.TeamWithMembers{}}
	s := New(f)
	suppliedID := uuid.New().String()
	_, err := s.Save(context.Background(), uuid.New(), SaveRequest{
		Teams: []SaveTeam{
			{ID: &suppliedID, Name: "A", Members: []SaveMember{{Name: "m1", Email: "m1@e.com"}}},
		},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if f.gotTeams[0].ID.String() != suppliedID {
		t.Errorf("supplied ID should be honored: want %s, got %s", suppliedID, f.gotTeams[0].ID)
	}
	if f.gotMembers[0].TeamID.String() != suppliedID {
		t.Errorf("member TeamID should match supplied team ID")
	}
}

func TestSave_AlreadyCompleted(t *testing.T) {
	f := &fakeRepo{saveErr: repository.ErrAlreadyCompleted}
	s := New(f)
	_, err := s.Save(context.Background(), uuid.New(), SaveRequest{
		Teams: []SaveTeam{{Name: "X", Members: nil}},
	})
	if !errors.Is(err, repository.ErrAlreadyCompleted) {
		t.Errorf("err: want ErrAlreadyCompleted, got %v", err)
	}
}

func TestSave_EmptyTeamName(t *testing.T) {
	s := New(&fakeRepo{})
	_, err := s.Save(context.Background(), uuid.New(), SaveRequest{
		Teams: []SaveTeam{{Name: "", Members: nil}},
	})
	var v *ValidationError
	if !errors.As(err, &v) || v.Code != "INVALID_NAME" {
		t.Errorf("want INVALID_NAME, got %v", err)
	}
}

func TestSave_TeamNameTooLong(t *testing.T) {
	s := New(&fakeRepo{})
	_, err := s.Save(context.Background(), uuid.New(), SaveRequest{
		Teams: []SaveTeam{{Name: strings.Repeat("x", 81), Members: nil}},
	})
	var v *ValidationError
	if !errors.As(err, &v) || v.Code != "INVALID_NAME" {
		t.Errorf("want INVALID_NAME, got %v", err)
	}
}

func TestSave_DuplicateTeamName(t *testing.T) {
	s := New(&fakeRepo{})
	_, err := s.Save(context.Background(), uuid.New(), SaveRequest{
		Teams: []SaveTeam{{Name: "X", Members: nil}, {Name: "x", Members: nil}},
	})
	var v *ValidationError
	if !errors.As(err, &v) || v.Code != "DUPLICATE_TEAM_NAME" {
		t.Errorf("want DUPLICATE_TEAM_NAME, got %v", err)
	}
}

func TestSave_InvalidEmail(t *testing.T) {
	s := New(&fakeRepo{})
	_, err := s.Save(context.Background(), uuid.New(), SaveRequest{
		Teams: []SaveTeam{{Name: "A", Members: []SaveMember{{Name: "n", Email: "no-at-here"}}}},
	})
	var v *ValidationError
	if !errors.As(err, &v) || v.Code != "INVALID_EMAIL" {
		t.Errorf("want INVALID_EMAIL, got %v", err)
	}
}

func TestSave_DuplicateMemberEmailWithinTeam(t *testing.T) {
	s := New(&fakeRepo{})
	_, err := s.Save(context.Background(), uuid.New(), SaveRequest{
		Teams: []SaveTeam{{Name: "A", Members: []SaveMember{
			{Name: "n1", Email: "x@e.com"}, {Name: "n2", Email: "X@e.com"}, // case-insensitive dedup
		}}},
	})
	var v *ValidationError
	if !errors.As(err, &v) || v.Code != "DUPLICATE_MEMBER_EMAIL" {
		t.Errorf("want DUPLICATE_MEMBER_EMAIL, got %v", err)
	}
}

func TestSave_AllowsSameEmailAcrossTeams(t *testing.T) {
	f := &fakeRepo{listResult: []repository.TeamWithMembers{}}
	s := New(f)
	_, err := s.Save(context.Background(), uuid.New(), SaveRequest{
		Teams: []SaveTeam{
			{Name: "A", Members: []SaveMember{{Name: "n", Email: "shared@e.com"}}},
			{Name: "B", Members: []SaveMember{{Name: "n", Email: "shared@e.com"}}},
		},
	})
	if err != nil {
		t.Errorf("shared email across teams should be allowed (TEAMS-02): %v", err)
	}
}

func TestSave_RejectsMemberWithEmptyName(t *testing.T) {
	s := New(&fakeRepo{})
	_, err := s.Save(context.Background(), uuid.New(), SaveRequest{
		Teams: []SaveTeam{{Name: "A", Members: []SaveMember{{Name: "", Email: "x@e.com"}}}},
	})
	var v *ValidationError
	if !errors.As(err, &v) || v.Code != "INVALID_NAME" {
		t.Errorf("want INVALID_NAME on member, got %v", err)
	}
}

func TestSave_RejectsMemberNameTooLong(t *testing.T) {
	s := New(&fakeRepo{})
	_, err := s.Save(context.Background(), uuid.New(), SaveRequest{
		Teams: []SaveTeam{{Name: "A", Members: []SaveMember{{Name: strings.Repeat("y", 121), Email: "x@e.com"}}}},
	})
	var v *ValidationError
	if !errors.As(err, &v) || v.Code != "INVALID_NAME" {
		t.Errorf("want INVALID_NAME on long member name, got %v", err)
	}
}

func TestSave_RejectsInvalidTeamID(t *testing.T) {
	s := New(&fakeRepo{})
	bad := "not-a-uuid"
	_, err := s.Save(context.Background(), uuid.New(), SaveRequest{
		Teams: []SaveTeam{{ID: &bad, Name: "A", Members: nil}},
	})
	var v *ValidationError
	if !errors.As(err, &v) || v.Code != "INVALID_TEAM_ID" {
		t.Errorf("want INVALID_TEAM_ID, got %v", err)
	}
}
