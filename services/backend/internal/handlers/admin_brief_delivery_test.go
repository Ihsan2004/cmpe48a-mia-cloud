package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// ==================== Minimal fake repo ====================

type fakeAuditRepo struct {
	rows []models.BriefDelivery
	last repository.BriefDeliveryFilter
}

func (f *fakeAuditRepo) Create(ctx context.Context, _ *models.BriefDelivery) error { return nil }
func (f *fakeAuditRepo) GetByID(ctx context.Context, _ uuid.UUID) (*models.BriefDelivery, error) {
	return nil, nil
}
func (f *fakeAuditRepo) GetByMeetingChannel(ctx context.Context, _ uuid.UUID, _ string) (*models.BriefDelivery, error) {
	return nil, nil
}
func (f *fakeAuditRepo) ListPendingDue(ctx context.Context, _ time.Time, _ int) ([]models.BriefDelivery, error) {
	return nil, nil
}
func (f *fakeAuditRepo) ListRecent(ctx context.Context, filter repository.BriefDeliveryFilter) ([]models.BriefDelivery, error) {
	f.last = filter
	return f.rows, nil
}
func (f *fakeAuditRepo) UpdateStatus(ctx context.Context, _ uuid.UUID, _, _, _ string) error {
	return nil
}
func (f *fakeAuditRepo) IncrementAttempts(ctx context.Context, _ uuid.UUID) error { return nil }

// ==================== Harness ====================

const auditAdminKey = "super-secret-admin-key-32-chars!!"

func newAuditApp(handler *AdminBriefDeliveryHandler) *fiber.App {
	app := fiber.New()
	app.Get("/api/admin/brief-deliveries", handler.List)
	return app
}

// ==================== Auth ====================

func TestAdminBriefDelivery_Unauthorized_MissingHeader(t *testing.T) {
	t.Parallel()
	repo := &fakeAuditRepo{}
	h := NewAdminBriefDeliveryHandler(repo, auditAdminKey)
	app := newAuditApp(h)

	req, _ := http.NewRequest(http.MethodGet, "/api/admin/brief-deliveries", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Errorf("status=%d, want 401", resp.StatusCode)
	}
}

func TestAdminBriefDelivery_Unauthorized_WrongKey(t *testing.T) {
	t.Parallel()
	repo := &fakeAuditRepo{}
	h := NewAdminBriefDeliveryHandler(repo, auditAdminKey)
	app := newAuditApp(h)

	req, _ := http.NewRequest(http.MethodGet, "/api/admin/brief-deliveries", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Errorf("status=%d, want 401", resp.StatusCode)
	}
}

func TestAdminBriefDelivery_Unauthorized_EmptyKeyConfig(t *testing.T) {
	t.Parallel()
	// Empty adminKey means the endpoint must fail closed — never accept
	// any bearer value, even an empty one.
	h := NewAdminBriefDeliveryHandler(&fakeAuditRepo{}, "")
	app := newAuditApp(h)
	req, _ := http.NewRequest(http.MethodGet, "/api/admin/brief-deliveries", nil)
	req.Header.Set("Authorization", "Bearer ")
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Errorf("status=%d, want 401 for empty admin key", resp.StatusCode)
	}
}

// ==================== Happy path ====================

func TestAdminBriefDelivery_List_ReturnsRows(t *testing.T) {
	t.Parallel()
	row := models.BriefDelivery{
		ID: uuid.New(), MeetingID: uuid.New(), UserID: uuid.New(),
		Channel: "email", Status: "sent", Attempts: 1,
	}
	repo := &fakeAuditRepo{rows: []models.BriefDelivery{row}}
	h := NewAdminBriefDeliveryHandler(repo, auditAdminKey)
	app := newAuditApp(h)

	req, _ := http.NewRequest(http.MethodGet, "/api/admin/brief-deliveries", nil)
	req.Header.Set("Authorization", "Bearer "+auditAdminKey)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	if parsed["count"].(float64) != 1 {
		t.Errorf("count=%v, want 1", parsed["count"])
	}
}

// ==================== Filter plumbing ====================

func TestAdminBriefDelivery_List_ForwardsFilters(t *testing.T) {
	t.Parallel()
	repo := &fakeAuditRepo{}
	h := NewAdminBriefDeliveryHandler(repo, auditAdminKey)
	app := newAuditApp(h)

	meetingID := uuid.New()
	userID := uuid.New()
	url := "/api/admin/brief-deliveries?meeting_id=" + meetingID.String() +
		"&user_id=" + userID.String() +
		"&channel=slack&status=failed&limit=25"
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+auditAdminKey)
	_, _ = app.Test(req, -1)

	if repo.last.MeetingID != meetingID {
		t.Errorf("meeting_id not forwarded: %v", repo.last.MeetingID)
	}
	if repo.last.UserID != userID {
		t.Errorf("user_id not forwarded: %v", repo.last.UserID)
	}
	if repo.last.Channel != "slack" {
		t.Errorf("channel not forwarded: %q", repo.last.Channel)
	}
	if repo.last.Status != "failed" {
		t.Errorf("status not forwarded: %q", repo.last.Status)
	}
	if repo.last.Limit != 25 {
		t.Errorf("limit not forwarded: %d", repo.last.Limit)
	}
}

// ==================== Input validation ====================

func TestAdminBriefDelivery_List_RejectsBadUUID(t *testing.T) {
	t.Parallel()
	h := NewAdminBriefDeliveryHandler(&fakeAuditRepo{}, auditAdminKey)
	app := newAuditApp(h)
	req, _ := http.NewRequest(http.MethodGet, "/api/admin/brief-deliveries?meeting_id=not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+auditAdminKey)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
}

func TestAdminBriefDelivery_List_RejectsBadChannel(t *testing.T) {
	t.Parallel()
	h := NewAdminBriefDeliveryHandler(&fakeAuditRepo{}, auditAdminKey)
	app := newAuditApp(h)
	req, _ := http.NewRequest(http.MethodGet, "/api/admin/brief-deliveries?channel=sms", nil)
	req.Header.Set("Authorization", "Bearer "+auditAdminKey)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
}

func TestAdminBriefDelivery_List_RejectsBadStatus(t *testing.T) {
	t.Parallel()
	h := NewAdminBriefDeliveryHandler(&fakeAuditRepo{}, auditAdminKey)
	app := newAuditApp(h)
	req, _ := http.NewRequest(http.MethodGet, "/api/admin/brief-deliveries?status=unknown", nil)
	req.Header.Set("Authorization", "Bearer "+auditAdminKey)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
}

// ==================== constantTimeEquals ====================

func TestConstantTimeEquals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"equal", "secret", "secret", true},
		{"differ", "secret", "secrep", false},
		{"length mismatch", "secret", "secreet", false},
		{"empty both", "", "", true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if got := constantTimeEquals(c.a, c.b); got != c.want {
				t.Errorf("constantTimeEquals(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}
