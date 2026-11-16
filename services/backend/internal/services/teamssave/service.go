// Package teamssave orchestrates the Phase 7 atomic save endpoint. It
// owns request-body validation, model wiring (assigning member.TeamID to
// the matching freshly-minted team.ID), and dispatch to
// repository.TeamRepository.SaveTeamsAndMembers.
//
// Why a dedicated package? The Phase 6 teamdetection package already owns
// the "teams" service namespace; mixing save concerns in there would
// blur responsibilities. Mirrors the briefnotify/ precedent — one package
// per concern.
//
// What this package does NOT do:
//   - it does NOT call gonum or anything algorithm-related (Phase 6 owns that)
//   - it does NOT touch SQL directly (the repo handles that)
//   - it does NOT manage advisory locks (TEAMS-04 idempotency is the
//     conditional UPDATE inside repo.SaveTeamsAndMembers — no service-level
//     mutex needed)
package teamssave

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// Validation length caps (RESEARCH Security Domain V5 + Endpoint Spec 2).
const (
	maxTeamNameLen   = 80
	maxMemberNameLen = 120
	maxTitleLen      = 80
)

// SaveRequest mirrors the POST /api/teams/save JSON body. The handler
// (handlers/teams.go) BodyParse'd payload arrives here unchanged.
type SaveRequest struct {
	Teams []SaveTeam `json:"teams"`
}

type SaveTeam struct {
	ID      *string      `json:"id,omitempty"` // optional; server mints if absent
	Name    string       `json:"name"`
	Members []SaveMember `json:"members"`
}

type SaveMember struct {
	Name  string  `json:"name"`
	Title *string `json:"title,omitempty"`
	Email string  `json:"email"`
}

// SaveResponse is the 200 body. Mirrors the GET /api/teams shape so the
// frontend can cache-prime without an extra fetch.
type SaveResponse struct {
	Status string                       `json:"status"` // "completed"
	Teams  []repository.TeamWithMembers `json:"teams"`
}

// ValidationError is the 400 surface. Handler maps Field + Code into a
// 400 JSON body.
type ValidationError struct {
	Field   string
	Code    string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation: %s: %s", e.Field, e.Message)
}

// Service is the package's only exported type. Construct once at startup
// (in routes.Setup) and share across requests — it is stateless.
type Service struct {
	repo repository.TeamRepository
}

// New is the production constructor.
func New(repo repository.TeamRepository) *Service {
	return &Service{repo: repo}
}

// Save validates the request, wires member.TeamID to the matching team's
// ID, and delegates the atomic insert + status flip to the repo. Returns
// repository.ErrAlreadyCompleted unchanged when the conditional UPDATE
// fails (handler maps to 409). Returns *ValidationError on body issues
// (handler maps to 400). Returns *SaveResponse on success.
func (s *Service) Save(ctx context.Context, userID uuid.UUID, req SaveRequest) (*SaveResponse, error) {
	if err := validate(req); err != nil {
		return nil, err
	}

	teams := make([]models.Team, 0, len(req.Teams))
	members := make([]models.TeamMember, 0)
	for _, st := range req.Teams {
		var teamID uuid.UUID
		if st.ID != nil && *st.ID != "" {
			parsed, perr := uuid.Parse(*st.ID)
			if perr != nil {
				return nil, &ValidationError{Field: "teams[].id", Code: "INVALID_TEAM_ID", Message: "team id is not a valid UUID"}
			}
			teamID = parsed
		} else {
			teamID = uuid.New()
		}

		teams = append(teams, models.Team{
			ID:     teamID,
			UserID: userID, // server-authoritative — caller-provided value ignored
			Name:   strings.TrimSpace(st.Name),
		})

		for _, sm := range st.Members {
			members = append(members, models.TeamMember{
				ID:     uuid.New(),
				TeamID: teamID,
				Name:   strings.TrimSpace(sm.Name),
				Title:  trimmedPtr(sm.Title),
				Email:  strings.ToLower(strings.TrimSpace(sm.Email)),
			})
		}
	}

	if err := s.repo.SaveTeamsAndMembers(ctx, userID, teams, members); err != nil {
		if errors.Is(err, repository.ErrAlreadyCompleted) {
			return nil, repository.ErrAlreadyCompleted
		}
		return nil, fmt.Errorf("teamssave: %w", err)
	}

	list, err := s.repo.ListTeamsByUserWithMembers(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("teamssave: list after save: %w", err)
	}
	return &SaveResponse{Status: "completed", Teams: list}, nil
}

// validate enforces the body rules (RESEARCH §"Endpoint Specifications 2").
func validate(req SaveRequest) error {
	seenTeamNames := make(map[string]struct{}, len(req.Teams))
	for ti, t := range req.Teams {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			return &ValidationError{Field: fmt.Sprintf("teams[%d].name", ti), Code: "INVALID_NAME", Message: "team name is required"}
		}
		if len(name) > maxTeamNameLen {
			return &ValidationError{Field: fmt.Sprintf("teams[%d].name", ti), Code: "INVALID_NAME", Message: "team name too long"}
		}
		lowered := strings.ToLower(name)
		if _, dup := seenTeamNames[lowered]; dup {
			return &ValidationError{Field: fmt.Sprintf("teams[%d].name", ti), Code: "DUPLICATE_TEAM_NAME", Message: "duplicate team name in request"}
		}
		seenTeamNames[lowered] = struct{}{}

		seenEmails := make(map[string]struct{}, len(t.Members))
		for mi, m := range t.Members {
			mName := strings.TrimSpace(m.Name)
			if mName == "" {
				return &ValidationError{Field: fmt.Sprintf("teams[%d].members[%d].name", ti, mi), Code: "INVALID_NAME", Message: "member name is required"}
			}
			if len(mName) > maxMemberNameLen {
				return &ValidationError{Field: fmt.Sprintf("teams[%d].members[%d].name", ti, mi), Code: "INVALID_NAME", Message: "member name too long"}
			}
			mEmail := strings.ToLower(strings.TrimSpace(m.Email))
			if !strings.Contains(mEmail, "@") {
				return &ValidationError{Field: fmt.Sprintf("teams[%d].members[%d].email", ti, mi), Code: "INVALID_EMAIL", Message: "email must contain @"}
			}
			if _, dup := seenEmails[mEmail]; dup {
				return &ValidationError{Field: fmt.Sprintf("teams[%d].members[%d].email", ti, mi), Code: "DUPLICATE_MEMBER_EMAIL", Message: "duplicate email in team"}
			}
			seenEmails[mEmail] = struct{}{}
			if m.Title != nil && len(strings.TrimSpace(*m.Title)) > maxTitleLen {
				return &ValidationError{Field: fmt.Sprintf("teams[%d].members[%d].title", ti, mi), Code: "INVALID_TITLE", Message: "title too long"}
			}
		}
	}
	return nil
}

func trimmedPtr(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	return &t
}
