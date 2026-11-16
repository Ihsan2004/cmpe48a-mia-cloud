package teamdetection

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"mia-website/backend/internal/services"
)

// expectedTeam is the per-team expected output (D-06). Emails are stored
// sorted for stable set-equality comparison.
type expectedTeam struct {
	Members []string `json:"members"`
}

// fixtureFile is the on-disk shape for a testdata/*.json file (D-06).
type fixtureFile struct {
	Input               []services.BackfillEvent `json:"input"`
	ExpectedStatus      string                   `json:"expected_status"`
	ExpectedEmptyReason string                   `json:"expected_empty_reason"`
	ExpectedTeams       []expectedTeam           `json:"expected_teams"`
}

// loadFixture reads a fixture JSON file by name (without .json suffix)
// from testdata/ and returns the parsed input + expected halves. Each
// expectedTeam's Members slice is sorted on read so callers can compare
// with set-equality.
//
// This single loader is used by every fixture-driven test in the
// package (correctness in cluster_test.go / score_test.go, sweep in
// params_sweep_test.go). Future fixture-format changes are one-place.
func loadFixture(t *testing.T, name string) ([]services.BackfillEvent, fixtureFile) {
	t.Helper()
	path := filepath.Join("testdata", name+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("loadFixture %s: %v", name, err)
	}
	var f fixtureFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("loadFixture %s: parse: %v", name, err)
	}
	for i := range f.ExpectedTeams {
		sort.Strings(f.ExpectedTeams[i].Members)
	}
	return f.Input, f
}

// allFixtures is the canonical list. Update when adding a new fixture.
var allFixtures = []string{
	"small_dense",
	"all_hands_plus_small",
	"interview_panel_noise",
	"cold_start",
	"one_on_one_only",
	"single_large_component",
	"multi_team_overlap",
	"boundary_78pct",
}

func TestAllFixturesParse(t *testing.T) {
	for _, name := range allFixtures {
		t.Run(name, func(t *testing.T) {
			input, expected := loadFixture(t, name)
			if len(input) == 0 {
				t.Fatalf("fixture %s has empty input", name)
			}
			if expected.ExpectedStatus == "" {
				t.Fatalf("fixture %s missing expected_status", name)
			}
		})
	}
}

func TestFixtureColdStart_HasExactlyThreeEvents(t *testing.T) {
	input, expected := loadFixture(t, "cold_start")
	if len(input) != 3 {
		t.Fatalf("cold_start: want 3 events, got %d", len(input))
	}
	if expected.ExpectedStatus != StatusDegenerate ||
		expected.ExpectedEmptyReason != EmptyReasonInsufficientMeetings {
		t.Fatalf("cold_start: want degenerate/insufficient_meetings, got %s/%s",
			expected.ExpectedStatus, expected.ExpectedEmptyReason)
	}
}

func TestFixtureMultiTeamOverlap_ContractorInBothTeams(t *testing.T) {
	_, expected := loadFixture(t, "multi_team_overlap")
	if expected.ExpectedStatus != StatusSuccess {
		t.Fatalf("multi_team_overlap: want success, got %s", expected.ExpectedStatus)
	}
	if len(expected.ExpectedTeams) != 2 {
		t.Fatalf("multi_team_overlap: want 2 expected teams, got %d", len(expected.ExpectedTeams))
	}
	const contractor = "contractor@example.com"
	count := 0
	for _, team := range expected.ExpectedTeams {
		for _, m := range team.Members {
			if m == contractor {
				count++
				break
			}
		}
	}
	if count != 2 {
		t.Fatalf("multi_team_overlap: contractor must be in BOTH teams (DETECT-01), got %d", count)
	}
}

func TestFixtureSingleLargeComponent_RoutesToDegenerate(t *testing.T) {
	_, expected := loadFixture(t, "single_large_component")
	if expected.ExpectedStatus != StatusDegenerate ||
		expected.ExpectedEmptyReason != EmptyReasonDegenerateCluster {
		t.Fatalf("single_large_component: want degenerate/degenerate_cluster, got %s/%s",
			expected.ExpectedStatus, expected.ExpectedEmptyReason)
	}
}

func TestFixtureBoundary78pct_StaysSuccess(t *testing.T) {
	_, expected := loadFixture(t, "boundary_78pct")
	if expected.ExpectedStatus != StatusSuccess {
		t.Fatalf("boundary_78pct: 78%% must PASS the 80%% gate; want success, got %s",
			expected.ExpectedStatus)
	}
	if len(expected.ExpectedTeams) == 0 {
		t.Fatalf("boundary_78pct: must return ≥ 1 team")
	}
}
