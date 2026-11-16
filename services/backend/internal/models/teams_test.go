package models

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Phase 4 v1.1 — Team / TeamMember / TeamDetectionRun struct shape tests.
// These tests are the in-code expression of:
//   - TEAMS-01 (teams: id, user_id, name, timestamps)
//   - TEAMS-02 + PRIVACY-04 (team_members: minimal-PII contract — no phone/photo/manager)
//   - D-08 (team_detection_runs: 12-column lean reproducibility schema)

func TestTeamJSONShape(t *testing.T) {
	t.Parallel()

	team := Team{
		ID:     uuid.New(),
		UserID: uuid.New(),
		Name:   "Engineering",
	}
	out, err := json.Marshal(team)
	if err != nil {
		t.Fatalf("json.Marshal(Team) error: %v", err)
	}
	got := string(out)

	expected := []string{`"id":`, `"user_id":`, `"name":`, `"created_at":`, `"updated_at":`}
	for _, key := range expected {
		if !strings.Contains(got, key) {
			t.Errorf("Team JSON missing expected key %s; got: %s", key, got)
		}
	}

	// No unexpected fields — Team contract is exactly five fields.
	forbidden := []string{
		`"members":`, `"description":`, `"confidence":`,
		`"team_id":`, // team_id belongs on TeamMember, not Team
	}
	for _, key := range forbidden {
		if strings.Contains(got, key) {
			t.Errorf("Team JSON contains forbidden key %s; got: %s", key, got)
		}
	}
}

func TestTeamMemberMinimalPIIJSONShape(t *testing.T) {
	t.Parallel()

	title := "Senior Engineer"
	member := TeamMember{
		ID:     uuid.New(),
		TeamID: uuid.New(),
		Name:   "Ada Lovelace",
		Title:  &title,
		Email:  "ada@example.com",
	}
	out, err := json.Marshal(member)
	if err != nil {
		t.Fatalf("json.Marshal(TeamMember) error: %v", err)
	}
	got := string(out)

	// PRIVACY-04 / TEAMS-02: exactly seven keys allowed.
	allowed := []string{
		`"id":`, `"team_id":`, `"name":`, `"title":`,
		`"email":`, `"created_at":`, `"updated_at":`,
	}
	for _, key := range allowed {
		if !strings.Contains(got, key) {
			t.Errorf("TeamMember JSON missing allowed key %s; got: %s", key, got)
		}
	}

	// PRIVACY-04 hard ceiling — these fields must NEVER appear on the wire.
	forbidden := []string{
		`"phone"`, `"photo"`, `"photo_url"`, `"manager"`, `"manager_email"`,
		`"linkedin"`, `"linkedin_url"`, `"is_external"`, `"domain"`,
		`"external_provider_id"`,
	}
	for _, key := range forbidden {
		if strings.Contains(got, key) {
			t.Errorf("TeamMember JSON contains forbidden PII key %s; got: %s", key, got)
		}
	}
}

func TestTeamMemberTitleNullable(t *testing.T) {
	t.Parallel()

	// Title is *string per EDIT-08 / migration 016 — must be assignable to nil.
	member := TeamMember{
		ID:     uuid.New(),
		TeamID: uuid.New(),
		Name:   "Grace Hopper",
		Title:  nil,
		Email:  "grace@example.com",
	}
	out, err := json.Marshal(member)
	if err != nil {
		t.Fatalf("json.Marshal(TeamMember) error: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `"title":null`) {
		t.Errorf("expected `\"title\":null` in JSON when Title is nil; got: %s", got)
	}
}

func TestTeamDetectionRunJSONShape(t *testing.T) {
	t.Parallel()

	run := TeamDetectionRun{
		ID:     uuid.New(),
		UserID: uuid.New(),
		Status: "running",
	}
	out, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("json.Marshal(TeamDetectionRun) error: %v", err)
	}
	got := string(out)

	// D-08: exactly 12 columns.
	expected := []string{
		`"id":`,
		`"user_id":`,
		`"started_at":`,
		`"finished_at":`,
		`"status":`,
		`"input_meeting_count":`,
		`"input_attendee_count":`,
		`"output_team_count":`,
		`"parameters_jsonb":`,
		`"algorithm_version":`,
		`"error_message":`,
		`"created_at":`,
	}
	for _, key := range expected {
		if !strings.Contains(got, key) {
			t.Errorf("TeamDetectionRun JSON missing expected key %s; got: %s", key, got)
		}
	}

	// Forbidden — explicitly rejected per D-11 / <deferred>:
	forbidden := []string{
		`"version":`, `"fetch_ms":`, `"cluster_ms":`, `"post_pass_ms":`,
		`"confidence":`,
	}
	for _, key := range forbidden {
		if strings.Contains(got, key) {
			t.Errorf("TeamDetectionRun JSON contains forbidden key %s; got: %s", key, got)
		}
	}
}

func TestTeamDetectionRunParametersJSONB(t *testing.T) {
	t.Parallel()

	run := TeamDetectionRun{
		ID:              uuid.New(),
		UserID:          uuid.New(),
		Status:          "success",
		ParametersJSONB: json.RawMessage([]byte(`{"k":3}`)),
	}
	out, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("json.Marshal(TeamDetectionRun) error: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `"parameters_jsonb":{"k":3}`) {
		t.Errorf("expected `\"parameters_jsonb\":{\"k\":3}` in JSON; got: %s", got)
	}
}
