package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services"
	"mia-website/backend/internal/services/teamdetection"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// teamsStubRepo — only the methods TeamsHandler calls have real bodies;
// the rest panic on accidental call.
type teamsStubRepo struct {
	listResult    []repository.TeamWithMembers
	listErr       error
	deleted       bool
	deleteErr     error
	skipErr       error
	saveErr       error
	detectLockErr error // returned by WithDetectLock to drive the contention branch
}

func (s *teamsStubRepo) ListTeamsByUserWithMembers(ctx context.Context, u uuid.UUID) ([]repository.TeamWithMembers, error) {
	return s.listResult, s.listErr
}
func (s *teamsStubRepo) DeleteMemberScoped(ctx context.Context, u, m uuid.UUID) (bool, error) {
	return s.deleted, s.deleteErr
}
func (s *teamsStubRepo) UpdateOnboardingStatus(ctx context.Context, u uuid.UUID, st string) error {
	return s.skipErr
}
func (s *teamsStubRepo) WithDetectLock(ctx context.Context, u uuid.UUID, fn func(context.Context) error) error {
	if s.detectLockErr != nil {
		return s.detectLockErr
	}
	return fn(ctx)
}
func (s *teamsStubRepo) SaveTeamsAndMembers(ctx context.Context, u uuid.UUID, t []models.Team, m []models.TeamMember) error {
	return s.saveErr
}

// Unused — panic if reached.
func (s *teamsStubRepo) CreateTeam(ctx context.Context, t *models.Team) error { panic("unused") }
func (s *teamsStubRepo) ListTeamsForUser(ctx context.Context, u uuid.UUID) ([]models.Team, error) {
	panic("unused")
}
func (s *teamsStubRepo) GetTeam(ctx context.Context, u, t uuid.UUID) (*models.Team, error) {
	panic("unused")
}
func (s *teamsStubRepo) UpdateTeamName(ctx context.Context, u, t uuid.UUID, n string) (bool, error) {
	panic("unused")
}
func (s *teamsStubRepo) DeleteTeam(ctx context.Context, u, t uuid.UUID) (bool, error) {
	panic("unused")
}
func (s *teamsStubRepo) AddMember(ctx context.Context, m *models.TeamMember) error { panic("unused") }
func (s *teamsStubRepo) ListMembersForTeam(ctx context.Context, t uuid.UUID) ([]models.TeamMember, error) {
	panic("unused")
}
func (s *teamsStubRepo) DeleteMember(ctx context.Context, m uuid.UUID) (bool, error) {
	panic("unused")
}
func (s *teamsStubRepo) CreateDetectionRun(ctx context.Context, r *models.TeamDetectionRun) error {
	panic("unused")
}
func (s *teamsStubRepo) UpdateDetectionRunFinish(ctx context.Context, runID uuid.UUID, status string, inputAttendeeCount, inputEdgeCount, outputTeamCount, outputMemberCount, outputLargestTeamSize *int, stageDurationsJSONB json.RawMessage, errorMessage *string) error {
	panic("unused")
}
func (s *teamsStubRepo) GetLatestDetectionRun(ctx context.Context, u uuid.UUID) (*models.TeamDetectionRun, error) {
	panic("unused")
}

// Compile-time conformance guard — keeps the stub honest if the interface widens.
var _ repository.TeamRepository = (*teamsStubRepo)(nil)

// fakeDetector implements teamdetection.Detector.
type fakeDetector struct {
	result *teamdetection.DetectionResult
	err    error
}

func (f *fakeDetector) Detect(ctx context.Context, u uuid.UUID, e string) (*teamdetection.DetectionResult, error) {
	return f.result, f.err
}

// newAppWithTeamsHandler builds a Fiber app with the handler routes mounted
// AND a stub middleware that injects userID into c.Locals — simulating
// the real AuthMiddleware.
func newAppWithTeamsHandler(t *testing.T, h *TeamsHandler) (*fiber.App, uuid.UUID) {
	t.Helper()
	userID := uuid.New()
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("userID", userID.String())
		c.Locals("userEmail", "user@example.com")
		return c.Next()
	})
	app.Post("/api/teams/detect", h.Detect)
	app.Post("/api/teams/save", h.Save)
	app.Post("/api/teams/skip", h.Skip)
	app.Get("/api/teams", h.List)
	app.Delete("/api/teams/members/:id", h.DeleteMember)
	return app, userID
}

func TestTeamsDetect_HappyPath(t *testing.T) {
	repo := &teamsStubRepo{}
	fd := &fakeDetector{result: &teamdetection.DetectionResult{Status: "success", Teams: []teamdetection.DetectedTeam{}, RunID: uuid.New()}}
	h := NewTeamsHandler(repo, fd)
	app, _ := newAppWithTeamsHandler(t, h)

	req := httptest.NewRequest(http.MethodPost, "/api/teams/detect", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 200 {
		t.Fatalf("status: want 200, got %d", resp.StatusCode)
	}
}

func TestTeamsDetect_AlreadyRunning(t *testing.T) {
	repo := &teamsStubRepo{detectLockErr: repository.ErrDetectAlreadyRunning}
	fd := &fakeDetector{}
	h := NewTeamsHandler(repo, fd)
	app, _ := newAppWithTeamsHandler(t, h)

	req := httptest.NewRequest(http.MethodPost, "/api/teams/detect", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 409 {
		t.Fatalf("status: want 409, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"code":"DETECT_RUNNING"`) {
		t.Errorf("body missing code DETECT_RUNNING: %s", body)
	}
}

func TestTeamsDetect_BackfillAuthExpired(t *testing.T) {
	repo := &teamsStubRepo{}
	// The detector's err surfaces through WithDetectLock's fn return.
	fd := &fakeDetector{err: &services.BackfillError{Class: services.ClassOAuthExpired}}
	h := NewTeamsHandler(repo, fd)
	app, _ := newAppWithTeamsHandler(t, h)

	req := httptest.NewRequest(http.MethodPost, "/api/teams/detect", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 401 {
		t.Fatalf("status: want 401, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"code":"GOOGLE_AUTH_EXPIRED"`) {
		t.Errorf("body: %s", body)
	}
}

func TestTeamsDetect_BackfillTransient(t *testing.T) {
	repo := &teamsStubRepo{}
	fd := &fakeDetector{err: &services.BackfillError{Class: services.ClassNetwork}}
	h := NewTeamsHandler(repo, fd)
	app, _ := newAppWithTeamsHandler(t, h)

	req := httptest.NewRequest(http.MethodPost, "/api/teams/detect", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 503 {
		t.Fatalf("status: want 503, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"code":"FETCH_TRANSIENT"`) {
		t.Errorf("body: %s", body)
	}
}

func TestTeamsDetect_BackfillPermanent(t *testing.T) {
	repo := &teamsStubRepo{}
	fd := &fakeDetector{err: &services.BackfillError{Class: services.ClassPermanent}}
	h := NewTeamsHandler(repo, fd)
	app, _ := newAppWithTeamsHandler(t, h)

	req := httptest.NewRequest(http.MethodPost, "/api/teams/detect", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 502 {
		t.Fatalf("status: want 502, got %d", resp.StatusCode)
	}
}

func TestTeamsSave_HappyPath(t *testing.T) {
	repo := &teamsStubRepo{listResult: []repository.TeamWithMembers{}}
	h := NewTeamsHandler(repo, &fakeDetector{})
	app, _ := newAppWithTeamsHandler(t, h)

	body := bytes.NewBufferString(`{"teams":[{"name":"A","members":[{"name":"ada","email":"ada@e.com"}]}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/teams/save", body)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: want 200, got %d; body=%s", resp.StatusCode, b)
	}
}

func TestTeamsSave_AlreadyCompleted(t *testing.T) {
	repo := &teamsStubRepo{saveErr: repository.ErrAlreadyCompleted}
	h := NewTeamsHandler(repo, &fakeDetector{})
	app, _ := newAppWithTeamsHandler(t, h)

	body := bytes.NewBufferString(`{"teams":[{"name":"A","members":[]}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/teams/save", body)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 409 {
		t.Fatalf("status: want 409, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `"code":"ALREADY_COMPLETED"`) {
		t.Errorf("body: %s", b)
	}
}

func TestTeamsSave_ValidationFail(t *testing.T) {
	repo := &teamsStubRepo{}
	h := NewTeamsHandler(repo, &fakeDetector{})
	app, _ := newAppWithTeamsHandler(t, h)

	body := bytes.NewBufferString(`{"teams":[{"name":"A","members":[{"name":"x","email":"no-at"}]}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/teams/save", body)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 400 {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `"code":"INVALID_EMAIL"`) {
		t.Errorf("body: %s", b)
	}
}

func TestTeamsSave_BadJSON(t *testing.T) {
	repo := &teamsStubRepo{}
	h := NewTeamsHandler(repo, &fakeDetector{})
	app, _ := newAppWithTeamsHandler(t, h)
	req := httptest.NewRequest(http.MethodPost, "/api/teams/save", bytes.NewBufferString("not-json"))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 400 {
		t.Errorf("want 400, got %d", resp.StatusCode)
	}
}

func TestTeamsSkip_HappyPath(t *testing.T) {
	repo := &teamsStubRepo{}
	h := NewTeamsHandler(repo, &fakeDetector{})
	app, _ := newAppWithTeamsHandler(t, h)

	req := httptest.NewRequest(http.MethodPost, "/api/teams/skip", bytes.NewBufferString(`{"empty_state":false}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 200 {
		t.Errorf("want 200, got %d", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "skipped" {
		t.Errorf("status: want skipped, got %q", body["status"])
	}
}

func TestTeamsSkip_EmptyState(t *testing.T) {
	repo := &teamsStubRepo{}
	h := NewTeamsHandler(repo, &fakeDetector{})
	app, _ := newAppWithTeamsHandler(t, h)

	req := httptest.NewRequest(http.MethodPost, "/api/teams/skip", bytes.NewBufferString(`{"empty_state":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "empty_state_skipped" {
		t.Errorf("status: want empty_state_skipped, got %q", body["status"])
	}
}

func TestTeamsSkip_AlreadyCompleted(t *testing.T) {
	repo := &teamsStubRepo{skipErr: repository.ErrAlreadyCompleted}
	h := NewTeamsHandler(repo, &fakeDetector{})
	app, _ := newAppWithTeamsHandler(t, h)
	req := httptest.NewRequest(http.MethodPost, "/api/teams/skip", bytes.NewBufferString(`{"empty_state":false}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 409 {
		t.Errorf("want 409, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `"code":"ALREADY_COMPLETED"`) {
		t.Errorf("body: %s", b)
	}
}

func TestTeamsList_HappyPath(t *testing.T) {
	repo := &teamsStubRepo{listResult: []repository.TeamWithMembers{
		{Team: models.Team{ID: uuid.New(), Name: "A"}, Members: []models.TeamMember{}},
	}}
	h := NewTeamsHandler(repo, &fakeDetector{})
	app, _ := newAppWithTeamsHandler(t, h)
	req := httptest.NewRequest(http.MethodGet, "/api/teams", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `"teams":[`) {
		t.Errorf("body: %s", b)
	}
}

func TestTeamsList_Empty(t *testing.T) {
	repo := &teamsStubRepo{listResult: []repository.TeamWithMembers{}}
	h := NewTeamsHandler(repo, &fakeDetector{})
	app, _ := newAppWithTeamsHandler(t, h)
	req := httptest.NewRequest(http.MethodGet, "/api/teams", nil)
	resp, _ := app.Test(req, -1)
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `"teams":[]`) {
		t.Errorf("empty list should marshal as []: %s", b)
	}
}

func TestTeamsList_NilFromRepo(t *testing.T) {
	repo := &teamsStubRepo{listResult: nil}
	h := NewTeamsHandler(repo, &fakeDetector{})
	app, _ := newAppWithTeamsHandler(t, h)
	req := httptest.NewRequest(http.MethodGet, "/api/teams", nil)
	resp, _ := app.Test(req, -1)
	b, _ := io.ReadAll(resp.Body)
	// Defensive guard must convert nil → []TeamWithMembers{} so JSON is "[]".
	if !strings.Contains(string(b), `"teams":[]`) {
		t.Errorf("nil repo result should still marshal as []: %s", b)
	}
}

func TestTeamsDeleteMember_HappyPath(t *testing.T) {
	repo := &teamsStubRepo{deleted: true}
	h := NewTeamsHandler(repo, &fakeDetector{})
	app, _ := newAppWithTeamsHandler(t, h)
	req := httptest.NewRequest(http.MethodDelete, "/api/teams/members/"+uuid.New().String(), nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 204 {
		t.Errorf("want 204, got %d", resp.StatusCode)
	}
}

func TestTeamsDeleteMember_NotFound(t *testing.T) {
	repo := &teamsStubRepo{deleted: false}
	h := NewTeamsHandler(repo, &fakeDetector{})
	app, _ := newAppWithTeamsHandler(t, h)
	req := httptest.NewRequest(http.MethodDelete, "/api/teams/members/"+uuid.New().String(), nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 404 {
		t.Errorf("want 404, got %d", resp.StatusCode)
	}
}

func TestTeamsDeleteMember_InvalidID(t *testing.T) {
	repo := &teamsStubRepo{}
	h := NewTeamsHandler(repo, &fakeDetector{})
	app, _ := newAppWithTeamsHandler(t, h)
	req := httptest.NewRequest(http.MethodDelete, "/api/teams/members/not-a-uuid", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 400 {
		t.Errorf("want 400, got %d", resp.StatusCode)
	}
}
