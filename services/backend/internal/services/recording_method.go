// Package services — recording_method.go centralises the rules that decide
// whether a meeting should be recorded by the bot or by the desktop app.
//
// Rule (roadmap Phase 5):
//   meeting.recording_method_override > profile.recording_method > "bot"
//
// The scheduler consults this helper before dispatching a bot; the
// desktop WebSocket scheduler (Phase 6) will call it to pick up its own
// queue.
package services

import (
	"context"
	"fmt"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

// RecordingMethodResolver is the interface the scheduler needs. Kept small
// so tests can pass a mock without pulling in DB.
type RecordingMethodResolver interface {
	GetEffectiveRecordingMethod(ctx context.Context, meetingID uuid.UUID) (string, error)
}

// recordingMethodService reads meeting + profile fields to produce the
// effective method. It doesn't mutate anything — the PATCH handlers own
// the writes.
type recordingMethodService struct {
	deps recordingMethodDeps
}

type recordingMethodDeps interface {
	GetMeetingOverride(ctx context.Context, meetingID uuid.UUID) (override *string, userID uuid.UUID, err error)
	GetProfileMethod(ctx context.Context, userID uuid.UUID) (string, error)
}

// NewRecordingMethodService wires the default deps backed by the global DB
// pool. Exposed as an interface for tests.
func NewRecordingMethodService() RecordingMethodResolver {
	return &recordingMethodService{deps: &postgresRecordingMethodDeps{}}
}

// NewRecordingMethodServiceWithDeps lets tests inject fakes.
func NewRecordingMethodServiceWithDeps(d recordingMethodDeps) RecordingMethodResolver {
	return &recordingMethodService{deps: d}
}

// GetEffectiveRecordingMethod applies the override-then-profile-then-default
// precedence from RECORDER_ROADMAP §2.4 B5. Returns the raw string — callers
// compare against models.RecordingMethodBot / RecordingMethodDesktop /
// RecordingMethodSkip constants.
//
// Resolution order:
//
//	meeting.recording_method_override   (bot|desktop|skip)
//	?? profile.recording_method          (bot|desktop)
//	?? "bot"
//
// `skip` is meeting-level only. Profile-level skip is rejected at the
// PATCH endpoint and falls through to the legacy "bot" default if a
// legacy row somehow holds it.
func (s *recordingMethodService) GetEffectiveRecordingMethod(ctx context.Context, meetingID uuid.UUID) (string, error) {
	override, userID, err := s.deps.GetMeetingOverride(ctx, meetingID)
	if err != nil {
		return "", fmt.Errorf("recording method: load meeting: %w", err)
	}
	if override != nil && models.IsValidRecordingMethodOverride(*override) {
		return *override, nil
	}
	method, err := s.deps.GetProfileMethod(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("recording method: load profile: %w", err)
	}
	if !models.IsValidRecordingMethod(method) {
		// Defensive fallback: profile might be null on legacy rows that
		// haven't been touched since migration 005 added the column with
		// a default. Treat as bot rather than surfacing a validation error
		// — the scheduler would otherwise skip a legit calendar meeting.
		return models.RecordingMethodBot, nil
	}
	return method, nil
}
