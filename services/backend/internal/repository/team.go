package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"time"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrAlreadyCompleted is returned by SaveTeamsAndMembers and
// UpdateOnboardingStatus when the conditional UPDATE writes zero rows —
// i.e., teams_onboarding_status is no longer 'pending'. Phase 7's handler
// translates this to HTTP 409 (TEAMS-04 idempotency).
var ErrAlreadyCompleted = errors.New("teams onboarding already finalized")

// ErrDetectAlreadyRunning is returned by WithDetectLock when
// pg_try_advisory_lock returns false. Phase 7's handler translates this
// to HTTP 409 with code=DETECT_RUNNING (CONTEXT.md D-26). The frontend
// silently no-ops on this code — the previous detect call is still
// running.
var ErrDetectAlreadyRunning = errors.New("team detection already running for this user")

// TeamWithMembers is the wire shape for GET /api/teams (TEAMS-05). The
// handler JSON-encodes []TeamWithMembers directly. Members are sorted by
// created_at ASC inside each team; teams sorted by their own created_at.
type TeamWithMembers struct {
	models.Team
	Members []models.TeamMember `json:"members"`
}

// TeamRepository defines data access for v1.1 Phase 4 team-detection tables:
// teams, team_members, team_detection_runs. All operations are scoped to a
// userID where applicable; the repository never returns data across user
// boundaries (per-user scoping per CONTEXT.md `<specifics>` (d)).
type TeamRepository interface {
	// ---- teams ----
	CreateTeam(ctx context.Context, team *models.Team) error
	ListTeamsForUser(ctx context.Context, userID uuid.UUID) ([]models.Team, error)
	GetTeam(ctx context.Context, userID, teamID uuid.UUID) (*models.Team, error)
	UpdateTeamName(ctx context.Context, userID, teamID uuid.UUID, newName string) (updated bool, err error)
	DeleteTeam(ctx context.Context, userID, teamID uuid.UUID) (deleted bool, err error)

	// ---- team_members ----
	AddMember(ctx context.Context, member *models.TeamMember) error
	ListMembersForTeam(ctx context.Context, teamID uuid.UUID) ([]models.TeamMember, error)
	DeleteMember(ctx context.Context, memberID uuid.UUID) (deleted bool, err error)

	// ---- team_detection_runs ----
	CreateDetectionRun(ctx context.Context, run *models.TeamDetectionRun) error
	UpdateDetectionRunFinish(
		ctx context.Context,
		runID uuid.UUID,
		status string,
		inputAttendeeCount *int,
		inputEdgeCount *int,
		outputTeamCount *int,
		outputMemberCount *int,
		outputLargestTeamSize *int,
		stageDurationsJSONB json.RawMessage,
		errorMessage *string,
	) error
	GetLatestDetectionRun(ctx context.Context, userID uuid.UUID) (*models.TeamDetectionRun, error)

	// ---- Phase 7 save / list / scoped-delete / status / detect-mutex ----

	// SaveTeamsAndMembers performs the atomic Phase 7 save. See doc-comment
	// on the impl in this file for full semantics. Returns ErrAlreadyCompleted
	// when teams_onboarding_status is not 'pending'.
	SaveTeamsAndMembers(ctx context.Context, userID uuid.UUID, teams []models.Team, members []models.TeamMember) error

	// ListTeamsByUserWithMembers returns saved teams with members nested
	// (single LEFT JOIN query, both ordered by created_at ASC).
	ListTeamsByUserWithMembers(ctx context.Context, userID uuid.UUID) ([]TeamWithMembers, error)

	// DeleteMemberScoped hard-deletes a member only if its team is owned
	// by userID. (deleted=false, err=nil) for both not-found and not-owned
	// cases (caller maps both to 404 to avoid IDOR signal).
	DeleteMemberScoped(ctx context.Context, userID uuid.UUID, memberID uuid.UUID) (deleted bool, err error)

	// UpdateOnboardingStatus flips profiles.teams_onboarding_status to
	// newStatus only when current value is 'pending'. Returns
	// ErrAlreadyCompleted otherwise. newStatus must be one of
	// 'completed' | 'skipped' | 'empty_state_skipped' (caller validates).
	UpdateOnboardingStatus(ctx context.Context, userID uuid.UUID, newStatus string) error

	// WithDetectLock runs fn under a session-scoped advisory lock keyed
	// by (phase7DetectNamespace, crc32(userID)). Lock + release on the
	// same *pgxpool.Conn. Returns ErrDetectAlreadyRunning if the lock
	// is held by another caller. RESEARCH Pitfall 1.
	WithDetectLock(ctx context.Context, userID uuid.UUID, fn func(ctx context.Context) error) error
}

type postgresTeamRepository struct{}

func NewTeamRepository() TeamRepository {
	return &postgresTeamRepository{}
}

// IsUniqueViolation reports whether err is a Postgres unique-violation
// (SQLState 23505). When constraintName != "" the check is narrowed to that
// specific constraint — useful for distinguishing
// teams_user_id_name_unique from team_members_team_id_email_unique.
//
// Exported so handlers (Phase 7) can translate constraint violations to
// HTTP 409 deterministically.
func IsUniqueViolation(err error, constraintName string) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	if constraintName == "" {
		return true
	}
	return pgErr.ConstraintName == constraintName
}

// ============================================================================
// teams
// ============================================================================

func (r *postgresTeamRepository) CreateTeam(ctx context.Context, team *models.Team) error {
	if team.ID == uuid.Nil {
		team.ID = uuid.New()
	}
	query := `
		INSERT INTO public.teams (id, user_id, name)
		VALUES ($1, $2, $3)
		RETURNING created_at, updated_at
	`
	err := DB.QueryRow(ctx, query, team.ID, team.UserID, team.Name).
		Scan(&team.CreatedAt, &team.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create team: %w", err)
	}
	return nil
}

func (r *postgresTeamRepository) ListTeamsForUser(ctx context.Context, userID uuid.UUID) ([]models.Team, error) {
	query := `
		SELECT id, user_id, name, created_at, updated_at
		FROM public.teams
		WHERE user_id = $1
		ORDER BY created_at ASC
	`
	rows, err := DB.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	defer rows.Close()

	var out []models.Team
	for rows.Next() {
		var t models.Team
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan team: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate teams: %w", err)
	}
	return out, nil
}

func (r *postgresTeamRepository) GetTeam(ctx context.Context, userID, teamID uuid.UUID) (*models.Team, error) {
	query := `
		SELECT id, user_id, name, created_at, updated_at
		FROM public.teams
		WHERE user_id = $1 AND id = $2
	`
	var t models.Team
	err := DB.QueryRow(ctx, query, userID, teamID).
		Scan(&t.ID, &t.UserID, &t.Name, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("get team: %w", err)
	}
	return &t, nil
}

func (r *postgresTeamRepository) UpdateTeamName(ctx context.Context, userID, teamID uuid.UUID, newName string) (bool, error) {
	query := `
		UPDATE public.teams
		SET name = $1, updated_at = NOW()
		WHERE user_id = $2 AND id = $3
	`
	cmd, err := DB.Exec(ctx, query, newName, userID, teamID)
	if err != nil {
		return false, fmt.Errorf("update team name: %w", err)
	}
	return cmd.RowsAffected() == 1, nil
}

func (r *postgresTeamRepository) DeleteTeam(ctx context.Context, userID, teamID uuid.UUID) (bool, error) {
	query := `DELETE FROM public.teams WHERE user_id = $1 AND id = $2`
	cmd, err := DB.Exec(ctx, query, userID, teamID)
	if err != nil {
		return false, fmt.Errorf("delete team: %w", err)
	}
	return cmd.RowsAffected() == 1, nil
}

// ============================================================================
// team_members
// ============================================================================

func (r *postgresTeamRepository) AddMember(ctx context.Context, member *models.TeamMember) error {
	if member.ID == uuid.Nil {
		member.ID = uuid.New()
	}
	query := `
		INSERT INTO public.team_members (id, team_id, name, title, email)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at, updated_at
	`
	err := DB.QueryRow(ctx, query,
		member.ID, member.TeamID, member.Name, member.Title, member.Email,
	).Scan(&member.CreatedAt, &member.UpdatedAt)
	if err != nil {
		return fmt.Errorf("add member: %w", err)
	}
	return nil
}

func (r *postgresTeamRepository) ListMembersForTeam(ctx context.Context, teamID uuid.UUID) ([]models.TeamMember, error) {
	query := `
		SELECT id, team_id, name, title, email, created_at, updated_at
		FROM public.team_members
		WHERE team_id = $1
		ORDER BY created_at ASC
	`
	rows, err := DB.Query(ctx, query, teamID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	var out []models.TeamMember
	for rows.Next() {
		var m models.TeamMember
		if err := rows.Scan(&m.ID, &m.TeamID, &m.Name, &m.Title, &m.Email, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate members: %w", err)
	}
	return out, nil
}

func (r *postgresTeamRepository) DeleteMember(ctx context.Context, memberID uuid.UUID) (bool, error) {
	// PRIVACY-02 right-to-erasure: hard delete by member id. Caller (Phase 7
	// handler) MUST verify the member's team belongs to the requesting user
	// before invoking this — repo does not enforce that scope because Phase 7
	// already does an ownership check before allowing the delete (see
	// threat T-04-15 in plan 04-04).
	query := `DELETE FROM public.team_members WHERE id = $1`
	cmd, err := DB.Exec(ctx, query, memberID)
	if err != nil {
		return false, fmt.Errorf("delete member: %w", err)
	}
	return cmd.RowsAffected() == 1, nil
}

// ============================================================================
// team_detection_runs
// ============================================================================

func (r *postgresTeamRepository) CreateDetectionRun(ctx context.Context, run *models.TeamDetectionRun) error {
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	query := `
		INSERT INTO public.team_detection_runs (
			id, user_id, started_at, status,
			input_meeting_count, input_attendee_count,
			parameters_jsonb, algorithm_version
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at
	`
	err := DB.QueryRow(ctx, query,
		run.ID, run.UserID, run.StartedAt, run.Status,
		run.InputMeetingCount, run.InputAttendeeCount,
		run.ParametersJSONB, run.AlgorithmVersion,
	).Scan(&run.CreatedAt)
	if err != nil {
		return fmt.Errorf("create detection run: %w", err)
	}
	return nil
}

// UpdateDetectionRunFinish flips a team_detection_runs row from
// status='running' to one of the terminal statuses ('success' /
// 'degenerate' / 'error'). Called by teamdetection.Service.Detect in a
// defer so panics still finalize the row (CONTEXT.md D-18).
//
// Phase 6 error_message DUAL-PURPOSE CONVENTION:
//
// The error_message column is reused to carry the EmptyReason string
// when status='degenerate'. This is intentional and SQL-friendly: a
// query like
//
//	SELECT * FROM team_detection_runs
//	WHERE status = 'degenerate' AND error_message = 'insufficient_meetings';
//
// finds every cold-start run. Phase 6 owns this convention; Phase 4's
// migration 017 left error_message TEXT NULL precisely to allow this
// reuse. The four legal EmptyReason values are:
//   - "insufficient_meetings"   (data-quality gate, < MinMeetings)
//   - "insufficient_attendees"  (data-quality gate, < MinAttendees)
//   - "zero_communities"        (algorithm returned empty)
//   - "degenerate_cluster"      (largest community > DegenerateRatio)
//
// When status='error', error_message carries a free-form error string
// (e.g., "panic: ..." from a recovered gonum invariant). When
// status='success', error_message MUST be nil.
//
// See:
//   - services/teamdetection/types.go EmptyReason* constants
//   - services/teamdetection/service.go finalizePersist
//   - sql/migrations/017_team_detection_runs.sql
//
// Phase 6 gap-closure (migration 018) extends the signature with five
// additional metric values that Service.Detect populates from
// in-memory pipeline state on every terminal path:
//
//	inputAttendeeCount   — backfilled from idx.Size() after BuildGraph
//	                       (was promised in 06-03 plan but never wired;
//	                       06-VERIFICATION flagged it as the #1 gap)
//	inputEdgeCount       — g.Edges().Len() after BuildGraph
//	outputMemberCount    — distinct-email union across all DetectedTeam.Members
//	                       (0 on degenerate/error paths)
//	outputLargestTeamSize — max(len(team.Members)) on success (0 elsewhere)
//	stageDurationsJSONB  — {"fetch":<sec>,"graph_build":<sec>,
//	                       "cluster":<sec>,"score":<sec>} JSON object;
//	                       persist stage NOT included (self-referential
//	                       — see service.go finalizePersist).
//
// All five are nil-safe. The COALESCE on input_attendee_count is
// defensive forward-compat: the v1.1 callers always pass nil from
// the pre-graph path and a real pointer from the post-graph path,
// so the COALESCE is a no-op for the current caller set.
func (r *postgresTeamRepository) UpdateDetectionRunFinish(
	ctx context.Context,
	runID uuid.UUID,
	status string,
	inputAttendeeCount *int,
	inputEdgeCount *int,
	outputTeamCount *int,
	outputMemberCount *int,
	outputLargestTeamSize *int,
	stageDurationsJSONB json.RawMessage,
	errorMessage *string,
) error {
	query := `
		UPDATE public.team_detection_runs
		SET finished_at = NOW(),
		    status = $1,
		    input_attendee_count = COALESCE($2, input_attendee_count),
		    input_edge_count = $3,
		    output_team_count = $4,
		    output_member_count = $5,
		    output_largest_team_size = $6,
		    stage_durations_jsonb = $7,
		    error_message = $8
		WHERE id = $9
	`
	_, err := DB.Exec(ctx, query,
		status,
		inputAttendeeCount,
		inputEdgeCount,
		outputTeamCount,
		outputMemberCount,
		outputLargestTeamSize,
		stageDurationsJSONB,
		errorMessage,
		runID,
	)
	if err != nil {
		return fmt.Errorf("update detection run: %w", err)
	}
	return nil
}

// GetLatestDetectionRun returns the most-recent run row for a user.
//
// Phase 6 gap-closure note (migration 018): the SELECT below currently
// scans the 12 original columns from migration 017. The four new
// columns added by migration 018 (input_edge_count, output_member_count,
// output_largest_team_size, stage_durations_jsonb) are intentionally
// NOT scanned here — Phase 6 does not consume this method, and
// adding them without a Phase 7 caller would be dead code. Phase 7
// owns extending the SELECT when it adds a UI consumer.
func (r *postgresTeamRepository) GetLatestDetectionRun(ctx context.Context, userID uuid.UUID) (*models.TeamDetectionRun, error) {
	query := `
		SELECT id, user_id, started_at, finished_at, status,
		       input_meeting_count, input_attendee_count, output_team_count,
		       parameters_jsonb, algorithm_version, error_message, created_at
		FROM public.team_detection_runs
		WHERE user_id = $1
		ORDER BY started_at DESC
		LIMIT 1
	`
	var run models.TeamDetectionRun
	err := DB.QueryRow(ctx, query, userID).Scan(
		&run.ID, &run.UserID, &run.StartedAt, &run.FinishedAt, &run.Status,
		&run.InputMeetingCount, &run.InputAttendeeCount, &run.OutputTeamCount,
		&run.ParametersJSONB, &run.AlgorithmVersion, &run.ErrorMessage, &run.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("get latest detection run: %w", err)
	}
	return &run, nil
}

// ============================================================================
// Phase 7 — atomic save / joined list / scoped delete / conditional status /
//           advisory-lock detect mutex
// ============================================================================

// SaveTeamsAndMembers performs the atomic Phase 7 save:
//
//  1. UPDATE profiles SET teams_onboarding_status='completed' WHERE id=$userID
//     AND teams_onboarding_status='pending' RETURNING id
//     — zero rows ⇒ tx.Rollback + ErrAlreadyCompleted (TEAMS-04).
//  2. INSERT every team (sets team.UserID=userID overriding any caller value;
//     RETURNING created_at, updated_at backfilled).
//  3. INSERT every member (uses member.TeamID as provided by caller — caller
//     wires TeamID from the freshly-inserted team's ID in the same slice).
//  4. tx.Commit.
//
// The status flip happens FIRST so the 409 short-circuits before any inserts
// run. This is the canonical idempotency boundary for TEAMS-04: a duplicate
// save attempt fails fast with no side effects.
func (r *postgresTeamRepository) SaveTeamsAndMembers(ctx context.Context, userID uuid.UUID, teams []models.Team, members []models.TeamMember) error {
	tx, err := DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("save teams: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. Status flip. Originally a conditional UPDATE that required
	//    teams_onboarding_status='pending' (TEAMS-04 dupe-tab idempotency).
	//    Loosened in v1.1 post-onboarding flow: users who re-detect from
	//    /teams must be able to add new teams without hitting a 409.
	//    Status flips pending → completed; for any other current status,
	//    it stays put. Two-tab race during onboarding is still defended
	//    at the INSERT layer via teams_user_id_name_unique, so duplicate
	//    suggestion saves fail cleanly with DUPLICATE_TEAM_NAME instead.
	//    The "user doesn't exist" edge is impossible — auth middleware
	//    guarantees a profile row.
	var flippedID uuid.UUID
	err = tx.QueryRow(ctx, `
		UPDATE public.profiles
		SET teams_onboarding_status = CASE
			WHEN teams_onboarding_status = 'pending' THEN 'completed'
			ELSE teams_onboarding_status
		END, updated_at = NOW()
		WHERE id = $1
		RETURNING id
	`, userID).Scan(&flippedID)
	if err != nil {
		return fmt.Errorf("save teams: status flip: %w", err)
	}

	// 2. INSERT teams. Override UserID from JWT — never trust request body.
	for i := range teams {
		if teams[i].ID == uuid.Nil {
			teams[i].ID = uuid.New()
		}
		teams[i].UserID = userID
		err := tx.QueryRow(ctx, `
			INSERT INTO public.teams (id, user_id, name)
			VALUES ($1, $2, $3)
			RETURNING created_at, updated_at
		`, teams[i].ID, teams[i].UserID, teams[i].Name).
			Scan(&teams[i].CreatedAt, &teams[i].UpdatedAt)
		if err != nil {
			return fmt.Errorf("save teams: insert team %q: %w", teams[i].Name, err)
		}
	}

	// 3. INSERT members. TeamID is trusted from caller; caller is the
	//    save service which already wired members[i].TeamID to one of the
	//    teams[*].ID values just inserted above. Defensive check: refuse
	//    member rows whose TeamID does NOT appear in teams[*].ID.
	teamIDSet := make(map[uuid.UUID]struct{}, len(teams))
	for _, t := range teams {
		teamIDSet[t.ID] = struct{}{}
	}
	for i := range members {
		if members[i].ID == uuid.Nil {
			members[i].ID = uuid.New()
		}
		if _, ok := teamIDSet[members[i].TeamID]; !ok {
			return fmt.Errorf("save teams: member references unknown team id %s", members[i].TeamID)
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO public.team_members (id, team_id, name, title, email)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING created_at, updated_at
		`, members[i].ID, members[i].TeamID, members[i].Name, members[i].Title, members[i].Email).
			Scan(&members[i].CreatedAt, &members[i].UpdatedAt)
		if err != nil {
			return fmt.Errorf("save teams: insert member %q: %w", members[i].Email, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("save teams: commit: %w", err)
	}
	return nil
}

// ListTeamsByUserWithMembers reads saved teams + members in ONE LEFT JOIN
// query. Returns []TeamWithMembers{} (NOT nil) for users with no teams.
// Each TeamWithMembers.Members is `[]models.TeamMember{}` (NOT nil) for
// teams with zero members — so JSON encoding emits `[]`, not `null`.
func (r *postgresTeamRepository) ListTeamsByUserWithMembers(ctx context.Context, userID uuid.UUID) ([]TeamWithMembers, error) {
	rows, err := DB.Query(ctx, `
		SELECT
			t.id, t.user_id, t.name, t.created_at, t.updated_at,
			m.id, m.team_id, m.name, m.title, m.email, m.created_at, m.updated_at
		FROM public.teams t
		LEFT JOIN public.team_members m ON m.team_id = t.id
		WHERE t.user_id = $1
		ORDER BY t.created_at ASC, m.created_at ASC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list teams with members: %w", err)
	}
	defer rows.Close()

	// Accumulate by team id preserving insertion order.
	order := make([]uuid.UUID, 0)
	byID := make(map[uuid.UUID]*TeamWithMembers)

	for rows.Next() {
		var (
			t      models.Team
			mID    *uuid.UUID
			mTeam  *uuid.UUID
			mName  *string
			mTitle *string
			mEmail *string
			mCAt   *time.Time
			mUAt   *time.Time
		)
		if err := rows.Scan(
			&t.ID, &t.UserID, &t.Name, &t.CreatedAt, &t.UpdatedAt,
			&mID, &mTeam, &mName, &mTitle, &mEmail, &mCAt, &mUAt,
		); err != nil {
			return nil, fmt.Errorf("list teams with members: scan: %w", err)
		}

		if _, ok := byID[t.ID]; !ok {
			byID[t.ID] = &TeamWithMembers{Team: t, Members: []models.TeamMember{}}
			order = append(order, t.ID)
		}

		if mID != nil {
			byID[t.ID].Members = append(byID[t.ID].Members, models.TeamMember{
				ID:        *mID,
				TeamID:    *mTeam,
				Name:      *mName,
				Title:     mTitle,
				Email:     *mEmail,
				CreatedAt: *mCAt,
				UpdatedAt: *mUAt,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list teams with members: iterate: %w", err)
	}

	out := make([]TeamWithMembers, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// DeleteMemberScoped hard-deletes a member only when its team belongs to
// userID. Single SQL statement using DELETE … USING (the join filter
// happens server-side). For both "not found" and "not owned" cases the
// method returns (false, nil) — caller maps both to 404 (IDOR mitigation
// per RESEARCH security domain).
func (r *postgresTeamRepository) DeleteMemberScoped(ctx context.Context, userID, memberID uuid.UUID) (bool, error) {
	cmd, err := DB.Exec(ctx, `
		DELETE FROM public.team_members m
		USING public.teams t
		WHERE m.id = $1
		  AND m.team_id = t.id
		  AND t.user_id = $2
	`, memberID, userID)
	if err != nil {
		return false, fmt.Errorf("delete member scoped: %w", err)
	}
	return cmd.RowsAffected() == 1, nil
}

// UpdateOnboardingStatus flips profiles.teams_onboarding_status only when
// the current value is 'pending'. Caller validates newStatus is one of
// 'completed' | 'skipped' | 'empty_state_skipped' before calling — the
// SQL CHECK constraint enforces this as a backstop.
func (r *postgresTeamRepository) UpdateOnboardingStatus(ctx context.Context, userID uuid.UUID, newStatus string) error {
	cmd, err := DB.Exec(ctx, `
		UPDATE public.profiles
		SET teams_onboarding_status = $1, updated_at = NOW()
		WHERE id = $2 AND teams_onboarding_status = 'pending'
	`, newStatus, userID)
	if err != nil {
		return fmt.Errorf("update onboarding status: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrAlreadyCompleted
	}
	return nil
}

// phase7DetectNamespace is the high int4 key for Phase 7's per-user detect
// advisory locks. Chosen as a feature-distinct constant so future advisory-
// lock domains (none currently exist in the repo) cannot collide with us.
const phase7DetectNamespace int32 = 0x07_DE_7E_C7

// userIDToInt32 hashes the userID into an int32 advisory-lock key. CRC32 is
// fine — we need uniform distribution across int32, not crypto strength.
// CRC32 is stable across Go versions (unlike Postgres hashtext which is
// documented as internal-use).
func userIDToInt32(userID uuid.UUID) int32 {
	return int32(crc32.ChecksumIEEE([]byte(userID.String())))
}

// WithDetectLock acquires a session-scoped Postgres advisory lock keyed by
// (phase7DetectNamespace, hash32(userID)) on a SINGLE *pgxpool.Conn, runs
// fn, and releases the lock on the SAME connection. RESEARCH Pitfall 1 —
// pool-level Exec would risk lock dangling because pgxpool may return a
// different connection for the release call.
//
// Behavior on contention: pg_try_advisory_lock returns false immediately
// (non-blocking). The helper returns ErrDetectAlreadyRunning. Handler maps
// to HTTP 409 with code=DETECT_RUNNING; frontend treats as silent no-op
// (CONTEXT.md D-26).
func (r *postgresTeamRepository) WithDetectLock(ctx context.Context, userID uuid.UUID, fn func(ctx context.Context) error) error {
	conn, err := DB.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("detect lock: acquire conn: %w", err)
	}
	defer conn.Release()

	lowKey := userIDToInt32(userID)

	var locked bool
	if err := conn.QueryRow(ctx,
		`SELECT pg_try_advisory_lock($1, $2)`,
		phase7DetectNamespace, lowKey,
	).Scan(&locked); err != nil {
		return fmt.Errorf("detect lock: try-acquire: %w", err)
	}
	if !locked {
		return ErrDetectAlreadyRunning
	}

	// IMPORTANT: release on the SAME *pgxpool.Conn via the conn-bound Exec.
	// Use a fresh, very short context so the release runs even when the
	// caller's ctx has been cancelled.
	defer func() {
		relCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(relCtx,
			`SELECT pg_advisory_unlock($1, $2)`,
			phase7DetectNamespace, lowKey,
		)
	}()

	return fn(ctx)
}
