package repository

import "testing"

// TestDesktopRepository_CompileTimeInterfaceCheck mirrors the contract_test
// pattern used by chain_repository et al. — the actual interface satisfaction
// check is a var _ declaration that would fail at build time. This test
// exists so the assertion shows up as a named entry in `go test` output.
//
// Per Phase 6 spec: no DB integration test here; integration coverage lives
// in integration_test.go alongside the other repositories.
func TestDesktopRepository_CompileTimeInterfaceCheck(t *testing.T) {
	t.Parallel()
	var _ DesktopRepository = (*postgresDesktopRepository)(nil)
}

// Phase 6 fix 2026-04-20 — enum validators guard both the handler and
// (via migration 006b) the DB. Keep the Go + SQL enums in sync; if you
// add a new command_type or ack status, update:
//   - ValidDesktopCommandTypes / ValidDesktopAckStatuses (this file)
//   - sql/migrations/006b_desktop_command_checks.sql (CHECK lists)
//   - handlers/desktop_ws.go message parsing
//   - docs/backend/desktop_ws.md if/when it exists

func TestIsValidDesktopCommandType(t *testing.T) {
	t.Parallel()
	allowed := []string{"start_recording", "stop_recording", "force_stop"}
	for _, c := range allowed {
		if !IsValidDesktopCommandType(c) {
			t.Errorf("%q should be valid", c)
		}
	}
	denied := []string{"", "START_RECORDING", "restart", "recording_start", "forcestop", "ping"}
	for _, c := range denied {
		if IsValidDesktopCommandType(c) {
			t.Errorf("%q should NOT be valid", c)
		}
	}
}

func TestIsValidDesktopAckStatus(t *testing.T) {
	t.Parallel()
	allowed := []string{"accepted", "declined", "no_ack"}
	for _, s := range allowed {
		if !IsValidDesktopAckStatus(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	denied := []string{"", "ACCEPTED", "ok", "yes", "pending", "success"}
	for _, s := range denied {
		if IsValidDesktopAckStatus(s) {
			t.Errorf("%q should NOT be valid", s)
		}
	}
}
