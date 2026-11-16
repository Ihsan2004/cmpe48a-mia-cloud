package teamdetection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/pkg/metrics"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services"

	"github.com/google/uuid"
	"gonum.org/v1/gonum/graph/simple"
)

// Service is the request-scoped orchestrator for v1.1 team detection.
// Phase 7's onboarding handler calls Service.Detect synchronously
// (CONTEXT.md <code_context> — no goroutines, no background jobs).
//
// The Service depends ONLY on:
//   - services.BackfillFetcher (Phase 5's fetcher interface)
//   - repository.TeamRepository (Phase 4's repo interface; only two
//     methods used: CreateDetectionRun, UpdateDetectionRunFinish — NOT
//     CreateTeam/AddMember. Phase 6 does NOT write to teams or
//     team_members. CONTEXT.md D-19.)
//   - a clock function (testable; production passes time.Now)
//
// Concurrency: Service.Detect is goroutine-safe per-call (it owns its
// state). Two concurrent calls for different userIDs are safe — they
// write two distinct team_detection_runs rows with different IDs. The
// fake repo and fetcher in tests are NOT goroutine-safe; tests run
// sequentially.
//
// Prometheus integration: each call increments TeamDetectionRunsTotal
// EXACTLY ONCE in the defer (so panics are still counted). Each stage
// observes one duration on TeamDetectionDurationSeconds (graph_build,
// cluster, score, persist are always observed even on degenerate paths;
// fetch is observed only when the fetcher was called).
//
// Run-row trade-off (documented for plan-checker visibility):
// If the fetch itself fails (probe-failure or network), no run row is
// written — Phase 4's CreateDetectionRun signature requires
// InputMeetingCount + InputAttendeeCount to be populated at INSERT time,
// and those values are only known AFTER a successful fetch. The
// BackfillError still propagates to the caller; Phase 7's handler logs
// and translates it to a retry/skip UX path per FETCH-04.
// Counter-state: TeamDetectionRunsTotal does NOT increment on fetch
// failure (no terminal state reached). This is consistent with D-18's
// description of input counts populating "AFTER the fetch returns but
// BEFORE the algorithm runs."
type Service struct {
	fetcher     services.BackfillFetcher
	repo        repository.TeamRepository
	clock       func() time.Time
	clusterFunc func(g *simple.WeightedUndirectedGraph, idx *nodeIndex) ([][]string, string) // test-injection seam (default = production ClusterCommunities)
	scoreFunc   func(communities [][]string, events []services.BackfillEvent, g *simple.WeightedUndirectedGraph, idx *nodeIndex) []DetectedTeam
}

// NewService is the production wire. cmd/server/main.go calls this once
// at startup and reuses the returned *Service across all onboarding
// requests. Fetcher and repo are concrete types; clock defaults to
// time.Now when nil.
func NewService(fetcher services.BackfillFetcher, repo repository.TeamRepository, clock func() time.Time) *Service {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		fetcher: fetcher,
		repo:    repo,
		clock:   clock,
	}
}

// Detect runs the full pipeline. Returns a fully-populated
// *DetectionResult on every terminal status (including "degenerate" and
// "error" — Teams is always non-nil, EmptyReason carries the cause).
// Returns (nil, err) only on fetch-stage failure (no row written —
// documented trade-off above).
//
// Pipeline (D-04, D-14):
//
//  1. fetch                       (stage="fetch")
//  2. MinMeetings gate            → returns (degenerate, insufficient_meetings) if < 10
//  3. CreateDetectionRun          (status="running", parameters snapshot)
//  4. graph build                 (stage="graph_build")
//                                 → captures inputAttendeeCount=idx.Size(),
//                                   inputEdgeCount=g.Edges().Len() for the row
//  5. MinAttendees gate           → degenerate if graph < 5 nodes (insufficient_attendees)
//  6. cluster                     (stage="cluster") → degenerate if zero_communities OR degenerate_cluster OR "error"
//  7. score                       (stage="score") → []DetectedTeam
//                                 → captures outputMemberCount=union(team.Members.Email),
//                                   outputLargestTeamSize=max(len(team.Members))
//  8. FinalizeDetectionRun        (stage="persist", deferred so panics finalize too)
//                                 → writes all metrics + stage_durations_jsonb to the row
//  9. emit TeamDetectionRunsTotal once for terminal state
//
// stage_durations_jsonb shape (Phase 6 gap-closure / migration 018):
//
//	{"fetch":<sec>,"graph_build":<sec>,"cluster":<sec>,"score":<sec>}
//
// The "persist" stage's duration is intentionally OMITTED from the
// JSONB because it would be self-referential (the JSONB is part of
// the UPDATE the persist stage is timing). The persist duration is
// observable only via the Prometheus histogram.
func (s *Service) Detect(ctx context.Context, userID uuid.UUID, userEmail string) (result *DetectionResult, err error) {
	startedAt := s.clock()

	// Phase 6 gap-closure (migration 018) — accumulators for metrics
	// persisted to team_detection_runs by finalizePersist. All four
	// pointers are nil until the corresponding stage runs; the JSONB
	// shape OMITS unobserved stages (NOT zero-valued).
	stageDurations := make(map[string]float64, 5)
	var (
		inputAttendeeCount    *int
		inputEdgeCount        *int
		outputMemberCount     = intPtr(0) // overwritten on success path
		outputLargestTeamSize = intPtr(0) // overwritten on success path
	)

	// 1. fetch
	fetchStart := time.Now()
	events, fetchErr := s.fetcher.FetchPast30DaysWithAttendees(ctx, userID, userEmail)
	fetchSec := time.Since(fetchStart).Seconds()
	stageDurations[metrics.StageFetch] = fetchSec
	metrics.TeamDetectionDurationSeconds.WithLabelValues(metrics.StageFetch).Observe(fetchSec)
	if fetchErr != nil {
		// No row written — see Run-row trade-off in the doc-comment.
		// Log with userID + class only (PII safety per T-06-01).
		var be *services.BackfillError
		class := "unknown"
		if errors.As(fetchErr, &be) {
			class = string(be.Class)
		}
		slog.Warn("team detection: fetch failed",
			"user_id", userID,
			"class", class,
		)
		return nil, fetchErr
	}

	// 2. MinMeetings gate (DETECT-06 / P8 — pre-algorithm short-circuit)
	if len(events) < MinMeetings {
		return s.finalizeWithEmptyReason(ctx, userID, startedAt, events, 0, stageDurations, EmptyReasonInsufficientMeetings)
	}

	// 3. CreateDetectionRun (we now know InputMeetingCount; will set
	//    InputAttendeeCount after graph build).
	inputMeetings := len(events)
	runID, createErr := s.createRunRow(ctx, userID, startedAt, &inputMeetings, nil /* attendee count fills after graph */)
	if createErr != nil {
		// Persistence at the START failed (e.g., DB unreachable).
		// Treat as a service-level error; no DetectionResult returned.
		slog.Error("team detection: CreateDetectionRun failed", "user_id", userID, "err", createErr)
		// We never reached a terminal state we could write back, so the
		// runs_total counter is NOT incremented. Phase 7's handler
		// translates the bare error to its UX path.
		return nil, fmt.Errorf("team detection: create run: %w", createErr)
	}

	// Track terminal state for the defer.
	terminalState := metrics.StateError
	// defer-Finalize (D-18). Captures all named returns + the recover()
	// path so a panic anywhere downstream still writes a terminal row.
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			panicMsg := fmt.Sprintf("panic: %v", r)
			// PII safety: %v on the panic value is fine because the panic
			// is from gonum (int IDs) or our own code (no emails in
			// panic values by construction). The stack trace MAY include
			// function names that reference helper types — none of which
			// carry PII. We persist panicMsg ONLY (not the stack) to
			// keep error_message bounded; the stack goes to slog.
			slog.Error("team detection: panic recovered",
				"user_id", userID,
				"run_id", runID,
				"panic", panicMsg,
				"stack", string(stack),
			)
			terminalState = metrics.StateError
			result = &DetectionResult{
				Status:      StatusError,
				EmptyReason: "",
				Teams:       []DetectedTeam{},
				RunID:       runID,
			}
			err = fmt.Errorf("team detection: %s", panicMsg)
			// Phase 6 gap-closure: persist whatever metric values the
			// pipeline accumulated before the panic. inputAttendeeCount
			// / inputEdgeCount are non-nil iff BuildGraph already ran;
			// outputMemberCount / outputLargestTeamSize stay at &0 (the
			// initializers set them) because score never completed.
			_ = s.finalizePersist(
				ctx, runID, StatusError,
				inputAttendeeCount, inputEdgeCount,
				nil, outputMemberCount, outputLargestTeamSize,
				stageDurations, &panicMsg,
			)
		}
		metrics.TeamDetectionRunsTotal.WithLabelValues(terminalState).Inc()
	}()

	// 4. graph build
	graphStart := time.Now()
	g, idx := BuildGraph(events)
	graphSec := time.Since(graphStart).Seconds()
	stageDurations[metrics.StageGraphBuild] = graphSec
	metrics.TeamDetectionDurationSeconds.WithLabelValues(metrics.StageGraphBuild).Observe(graphSec)

	// Phase 6 gap-closure: capture input attendee + edge counts now that
	// the graph exists. These pointers are read by every downstream
	// finalize call (success, degenerate-after-graph, panic-recovery).
	attendeeCount := idx.Size()
	edgeCount := g.Edges().Len()
	inputAttendeeCount = &attendeeCount
	inputEdgeCount = &edgeCount

	// 5. MinAttendees gate (DETECT-06)
	if idx.Size() < MinAttendees {
		terminalState = metrics.StateDegenerate
		em := EmptyReasonInsufficientAttendees
		if perr := s.finalizePersist(
			ctx, runID, StatusDegenerate,
			inputAttendeeCount, inputEdgeCount,
			intPtr(0), outputMemberCount, outputLargestTeamSize,
			stageDurations, &em,
		); perr != nil {
			slog.Error("team detection: persist (insufficient_attendees) failed", "user_id", userID, "err", perr)
		}
		return &DetectionResult{
			Status:      StatusDegenerate,
			EmptyReason: EmptyReasonInsufficientAttendees,
			Teams:       []DetectedTeam{},
			RunID:       runID,
		}, nil
	}

	// 6. cluster
	clusterStart := time.Now()
	var (
		communities [][]string
		emptyReason string
	)
	if s.clusterFunc != nil {
		// Test-only injection seam (see SetClusterFuncForTest in
		// service_testhooks.go). Production code always takes the
		// else-branch.
		communities, emptyReason = s.clusterFunc(g, idx)
	} else {
		communities, emptyReason = ClusterCommunities(g, idx)
	}
	clusterSec := time.Since(clusterStart).Seconds()
	stageDurations[metrics.StageCluster] = clusterSec
	metrics.TeamDetectionDurationSeconds.WithLabelValues(metrics.StageCluster).Observe(clusterSec)

	if emptyReason == "error" {
		// gonum panicked inside ClusterCommunities; treated as StatusError.
		terminalState = metrics.StateError
		errMsg := "cluster: gonum invariant violation"
		_ = s.finalizePersist(
			ctx, runID, StatusError,
			inputAttendeeCount, inputEdgeCount,
			nil, outputMemberCount, outputLargestTeamSize,
			stageDurations, &errMsg,
		)
		return &DetectionResult{
			Status:      StatusError,
			EmptyReason: "",
			Teams:       []DetectedTeam{},
			RunID:       runID,
		}, fmt.Errorf("%s", errMsg)
	}
	if emptyReason != "" {
		// EmptyReasonZeroCommunities or EmptyReasonDegenerateCluster.
		terminalState = metrics.StateDegenerate
		em := emptyReason
		if perr := s.finalizePersist(
			ctx, runID, StatusDegenerate,
			inputAttendeeCount, inputEdgeCount,
			intPtr(0), outputMemberCount, outputLargestTeamSize,
			stageDurations, &em,
		); perr != nil {
			slog.Error("team detection: persist (cluster-empty) failed", "user_id", userID, "err", perr)
		}
		return &DetectionResult{
			Status:      StatusDegenerate,
			EmptyReason: emptyReason,
			Teams:       []DetectedTeam{},
			RunID:       runID,
		}, nil
	}

	// 7. score
	scoreStart := time.Now()
	var teams []DetectedTeam
	if s.scoreFunc != nil {
		teams = s.scoreFunc(communities, events, g, idx)
	} else {
		teams = ScoreCommunities(communities, events, g, idx)
	}
	scoreSec := time.Since(scoreStart).Seconds()
	stageDurations[metrics.StageScore] = scoreSec
	metrics.TeamDetectionDurationSeconds.WithLabelValues(metrics.StageScore).Observe(scoreSec)

	// Phase 6 gap-closure: distinct-email union across all teams +
	// largest team size. Both deterministic (sorted iteration not
	// required — output_member_count is a count; output_largest_team_size
	// is a max). Computed on the success path; stays at 0 for
	// degenerate/error paths (assigned via outputMemberCount initializer).
	memberSet := make(map[string]struct{})
	largest := 0
	for _, team := range teams {
		if len(team.Members) > largest {
			largest = len(team.Members)
		}
		for _, m := range team.Members {
			memberSet[m.Email] = struct{}{}
		}
	}
	outMemberCt := len(memberSet)
	outputMemberCount = &outMemberCt
	outputLargestTeamSize = &largest

	// 8. persist + 9. metric (defer handles metric)
	outCount := len(teams)
	if perr := s.finalizePersist(
		ctx, runID, StatusSuccess,
		inputAttendeeCount, inputEdgeCount,
		&outCount, outputMemberCount, outputLargestTeamSize,
		stageDurations, nil,
	); perr != nil {
		slog.Error("team detection: persist (success) failed", "user_id", userID, "err", perr)
	}
	terminalState = metrics.StateSuccess

	return &DetectionResult{
		Status:      StatusSuccess,
		EmptyReason: "",
		Teams:       teams,
		RunID:       runID,
	}, nil
}

// createRunRow inserts the team_detection_runs row at status="running".
// CONTEXT.md D-18 — parameters_jsonb + algorithm_version snapshot here.
func (s *Service) createRunRow(ctx context.Context, userID uuid.UUID, startedAt time.Time, inputMeetingCount, inputAttendeeCount *int) (uuid.UUID, error) {
	ver := AlgorithmVersion()
	run := &models.TeamDetectionRun{
		UserID:             userID,
		StartedAt:          startedAt,
		Status:             StatusRunning,
		InputMeetingCount:  inputMeetingCount,
		InputAttendeeCount: inputAttendeeCount,
		ParametersJSONB:    json.RawMessage(SnapshotParameters()),
		AlgorithmVersion:   &ver,
	}
	// Propagate userID to the model (acceptance: T-06-06 — verifies
	// per-user scoping survives the helper boundary).
	run.UserID = userID
	if err := s.repo.CreateDetectionRun(ctx, run); err != nil {
		return uuid.Nil, err
	}
	return run.ID, nil
}

// finalizePersist wraps repo.UpdateDetectionRunFinish with stage
// timing and the gap-closure metric values. It marshals the
// stageDurations map to JSON inside; callers pass the live map.
//
// Phase 6 gap-closure stage_durations_jsonb shape (migration 018):
//
//	{"fetch":<sec>,"graph_build":<sec>,"cluster":<sec>,"score":<sec>}
//
// The persist stage is observed via the defer below ON THE PROMETHEUS
// HISTOGRAM but is NOT included in the JSONB written to the row —
// doing so would be self-referential because this UPDATE is the
// persist stage. Live with the trade-off: persist duration lives
// only on the histogram; the four pre-persist stages live on both.
//
// error_message dual-purpose convention (CONTEXT.md D-14):
//
//	status="success"    → errorMessage = nil
//	status="degenerate" → errorMessage = &EmptyReason
//	status="error"      → errorMessage = &errString
func (s *Service) finalizePersist(
	ctx context.Context,
	runID uuid.UUID,
	status string,
	inputAttendeeCount *int,
	inputEdgeCount *int,
	outputTeamCount *int,
	outputMemberCount *int,
	outputLargestTeamSize *int,
	stageDurations map[string]float64,
	errorMessage *string,
) error {
	persistStart := time.Now()
	defer func() {
		metrics.TeamDetectionDurationSeconds.WithLabelValues(metrics.StagePersist).Observe(time.Since(persistStart).Seconds())
	}()
	var stageJSON json.RawMessage
	if len(stageDurations) > 0 {
		b, err := json.Marshal(stageDurations)
		if err == nil {
			stageJSON = b
		}
		// If marshal fails (impossible for map[string]float64 in
		// practice), we degrade gracefully by leaving stageJSON nil
		// — the column writes NULL.
	}
	return s.repo.UpdateDetectionRunFinish(
		ctx, runID, status,
		inputAttendeeCount,
		inputEdgeCount,
		outputTeamCount,
		outputMemberCount,
		outputLargestTeamSize,
		stageJSON,
		errorMessage,
	)
}

// finalizeWithEmptyReason handles the MinMeetings gate (which fires
// BEFORE CreateDetectionRun and BEFORE BuildGraph). Per
// CONTEXT.md D-13 + D-18: when the data-quality gate fires before
// any run row exists, we STILL want a row for reproducibility — so
// we insert + immediately finalize.
//
// Phase 6 gap-closure: stageDurations passed in from Detect carries
// ONLY the {"fetch": <sec>} key here because BuildGraph hasn't run
// yet. inputAttendeeCount + inputEdgeCount are both nil for the
// same reason. outputMemberCount + outputLargestTeamSize default
// to &0 (initialized in Detect).
func (s *Service) finalizeWithEmptyReason(
	ctx context.Context,
	userID uuid.UUID,
	startedAt time.Time,
	events []services.BackfillEvent,
	attendeeCount int,
	stageDurations map[string]float64,
	reason string,
) (*DetectionResult, error) {
	inputMeetings := len(events)
	var inputAttendees *int
	if attendeeCount > 0 {
		ia := attendeeCount
		inputAttendees = &ia
	}
	runID, createErr := s.createRunRow(ctx, userID, startedAt, &inputMeetings, inputAttendees)
	if createErr != nil {
		slog.Error("team detection: CreateDetectionRun (pre-algorithm gate) failed", "user_id", userID, "err", createErr)
		return nil, fmt.Errorf("team detection: create run: %w", createErr)
	}
	em := reason
	if perr := s.finalizePersist(
		ctx, runID, StatusDegenerate,
		inputAttendees, // pre-graph: nil unless caller already knew
		nil,            // inputEdgeCount: BuildGraph hasn't run
		intPtr(0),      // outputTeamCount
		intPtr(0),      // outputMemberCount
		intPtr(0),      // outputLargestTeamSize
		stageDurations,
		&em,
	); perr != nil {
		slog.Error("team detection: persist (pre-algorithm gate) failed", "user_id", userID, "err", perr)
	}
	// Increment the terminal counter here since the defer in Detect is
	// not in scope for this helper. NOTE: this is the ONLY place outside Detect's defer
	// that emits the runs_total metric — keep it in lockstep with the defer logic.
	metrics.TeamDetectionRunsTotal.WithLabelValues(metrics.StateDegenerate).Inc()
	return &DetectionResult{
		Status:      StatusDegenerate,
		EmptyReason: reason,
		Teams:       []DetectedTeam{},
		RunID:       runID,
	}, nil
}

// intPtr is a tiny convenience for the gap-closure pointer fields.
func intPtr(n int) *int { return &n }

// Compile-time interface satisfaction check — Service implements Detector.
var _ Detector = (*Service)(nil)
