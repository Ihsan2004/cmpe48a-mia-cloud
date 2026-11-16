package services

import (
	"context"
	"errors"
	"testing"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

// fakeRecordingMethodDeps is a tiny in-memory impl of recordingMethodDeps
// used by the precedence tests.
type fakeRecordingMethodDeps struct {
	override      *string
	overrideErr   error
	profileValue  string
	profileErr    error
	userID        uuid.UUID
}

func (f *fakeRecordingMethodDeps) GetMeetingOverride(_ context.Context, _ uuid.UUID) (*string, uuid.UUID, error) {
	if f.overrideErr != nil {
		return nil, uuid.Nil, f.overrideErr
	}
	return f.override, f.userID, nil
}

func (f *fakeRecordingMethodDeps) GetProfileMethod(_ context.Context, _ uuid.UUID) (string, error) {
	return f.profileValue, f.profileErr
}

func strPtr(s string) *string { return &s }

func TestEffectiveMethod_OverrideWins(t *testing.T) {
	deps := &fakeRecordingMethodDeps{
		override:     strPtr(models.RecordingMethodDesktop),
		profileValue: models.RecordingMethodBot, // opposite of override
	}
	svc := NewRecordingMethodServiceWithDeps(deps)
	got, err := svc.GetEffectiveRecordingMethod(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != models.RecordingMethodDesktop {
		t.Errorf("override should win, got %q", got)
	}
}

func TestEffectiveMethod_FallsBackToProfile(t *testing.T) {
	deps := &fakeRecordingMethodDeps{
		override:     nil, // no override set
		profileValue: models.RecordingMethodDesktop,
	}
	svc := NewRecordingMethodServiceWithDeps(deps)
	got, _ := svc.GetEffectiveRecordingMethod(context.Background(), uuid.New())
	if got != models.RecordingMethodDesktop {
		t.Errorf("should fall back to profile, got %q", got)
	}
}

func TestEffectiveMethod_BotDefault_WhenProfileEmpty(t *testing.T) {
	deps := &fakeRecordingMethodDeps{
		override:     nil,
		profileValue: "", // legacy profile row with null recording_method
	}
	svc := NewRecordingMethodServiceWithDeps(deps)
	got, _ := svc.GetEffectiveRecordingMethod(context.Background(), uuid.New())
	if got != models.RecordingMethodBot {
		t.Errorf("empty profile value should default to bot, got %q", got)
	}
}

func TestEffectiveMethod_InvalidOverrideIgnored(t *testing.T) {
	// A stray value in the DB (shouldn't happen thanks to CHECK constraint,
	// but defensively honour the profile value).
	bogus := "rubbish"
	deps := &fakeRecordingMethodDeps{
		override:     &bogus,
		profileValue: models.RecordingMethodDesktop,
	}
	svc := NewRecordingMethodServiceWithDeps(deps)
	got, _ := svc.GetEffectiveRecordingMethod(context.Background(), uuid.New())
	if got != models.RecordingMethodDesktop {
		t.Errorf("invalid override should be ignored, got %q", got)
	}
}

// Phase 2.4 B5: skip is meeting-level only and must be honored as a
// valid override that the scheduler interprets as no-op.
func TestEffectiveMethod_SkipOverrideHonored(t *testing.T) {
	deps := &fakeRecordingMethodDeps{
		override:     strPtr(models.RecordingMethodSkip),
		profileValue: models.RecordingMethodDesktop, // would otherwise dispatch
	}
	svc := NewRecordingMethodServiceWithDeps(deps)
	got, err := svc.GetEffectiveRecordingMethod(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != models.RecordingMethodSkip {
		t.Errorf("skip override should win over profile, got %q", got)
	}
}

func TestEffectiveMethod_PropagatesMeetingLookupError(t *testing.T) {
	deps := &fakeRecordingMethodDeps{overrideErr: errors.New("db exploded")}
	svc := NewRecordingMethodServiceWithDeps(deps)
	if _, err := svc.GetEffectiveRecordingMethod(context.Background(), uuid.New()); err == nil {
		t.Error("expected error to propagate")
	}
}

func TestEffectiveMethod_PropagatesProfileLookupError(t *testing.T) {
	deps := &fakeRecordingMethodDeps{
		override:   nil,
		profileErr: errors.New("profile missing"),
	}
	svc := NewRecordingMethodServiceWithDeps(deps)
	if _, err := svc.GetEffectiveRecordingMethod(context.Background(), uuid.New()); err == nil {
		t.Error("expected error to propagate")
	}
}

func TestIsValidRecordingMethod(t *testing.T) {
	cases := map[string]bool{
		"bot":     true,
		"desktop": true,
		"":        false,
		"Bot":     false, // case-sensitive
		"auto":    false,
		"BOT":     false,
	}
	for input, want := range cases {
		if got := models.IsValidRecordingMethod(input); got != want {
			t.Errorf("IsValidRecordingMethod(%q) = %v, want %v", input, got, want)
		}
	}
}
