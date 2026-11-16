package services

import (
	"strings"
	"unicode"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

// Pure scoring helpers + key builders shared between the v3 matcher
// (chain_matcher_v3.go) and Stage 3 decision-binding
// (chain_matcher_v3_stage3.go).

// chainMatchWindowDays bounds the historical window the v3 matcher
// scans when looking for similar meetings (chain'd or orphan).
const chainMatchWindowDays = 45

// chainMinAttendees is the minimum attendee count required on each
// side of the v3 metadata score.
const chainMinAttendees = 3

// Suppression-key prefixes for chain_decisions_v2.suppression_key.
// Stage 1+2 (metadata) and Stage 3 (decision-embedding) namespace
// their declines independently so a streak of metadata declines
// doesn't gate Stage 3 evaluations and vice versa.
const (
	suppressionPrefixMetadata = "metadata:"
	suppressionPrefixDecision = "decision:"
)

// chainCandidateKey returns the canonical `chain:<uuid>` form used
// across both stages as the row's CandidateKey.
func chainCandidateKey(chainID uuid.UUID) string {
	return "chain:" + chainID.String()
}

// metadataSuppressionKey wraps a CandidateKey with the Stage 1+2
// `metadata:` namespace.
func metadataSuppressionKey(candidateKey string) string {
	return suppressionPrefixMetadata + candidateKey
}

// decisionSuppressionKey wraps a CandidateKey with the Stage 3
// `decision:` namespace.
func decisionSuppressionKey(candidateKey string) string {
	return suppressionPrefixDecision + candidateKey
}

// TitleSimilarity calculates word-level similarity between two meeting titles.
// Returns a ratio [0, 1] of common words / max(words_a, words_b).
// Case-insensitive, stop words removed.
func TitleSimilarity(a, b string) float64 {
	wordsA := normalizeTitle(a)
	wordsB := normalizeTitle(b)

	if len(wordsA) == 0 && len(wordsB) == 0 {
		return 1.0 // Both empty = match
	}
	if len(wordsA) == 0 || len(wordsB) == 0 {
		return 0.0 // One empty = no match
	}

	common := wordIntersection(wordsA, wordsB)
	maxLen := len(wordsA)
	if len(wordsB) > maxLen {
		maxLen = len(wordsB)
	}

	return float64(len(common)) / float64(maxLen)
}

// normalizeTitle converts a title to lowercase words with stop words removed
func normalizeTitle(title string) []string {
	title = strings.ToLower(strings.TrimSpace(title))
	words := strings.FieldsFunc(title, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	var filtered []string
	for _, w := range words {
		if !isStopWord(w) {
			filtered = append(filtered, w)
		}
	}
	return filtered
}

// Turkish + English stop words
var stopWords = map[string]bool{
	// Turkish
	"ve": true, "ile": true, "bir": true, "bu": true, "da": true, "de": true,
	"için": true, "ama": true, "ancak": true, "ya": true, "veya": true,
	"gibi": true, "kadar": true, "sonra": true, "önce": true,
	"haftalık": true, "aylık": true, "günlük": true,
	// English
	"the": true, "a": true, "an": true, "and": true, "or": true, "but": true,
	"in": true, "on": true, "at": true, "to": true, "for": true, "of": true,
	"with": true, "by": true, "from": true, "is": true, "are": true, "was": true,
	"weekly": true, "monthly": true, "daily": true, "bi": true,
}

func isStopWord(w string) bool {
	return stopWords[w]
}

// wordIntersection returns words present in both slices
func wordIntersection(a, b []string) []string {
	set := make(map[string]bool)
	for _, w := range a {
		set[w] = true
	}

	var common []string
	seen := make(map[string]bool)
	for _, w := range b {
		if set[w] && !seen[w] {
			common = append(common, w)
			seen[w] = true
		}
	}
	return common
}

// ParticipantOverlap calculates the overlap ratio between two attendee lists.
// Returns (commonCount, overlapRatio).
func ParticipantOverlap(attendeesA, attendeesB []models.Attendee) (int, float64) {
	return participantOverlapWithSet(attendeeEmailSet(attendeesA), attendeesB)
}

// participantOverlapWithSet is the hot-loop variant of ParticipantOverlap:
// the caller passes A's email set already built, so a Decide pass that
// scores N candidates against the same new meeting only normalises A
// once.
func participantOverlapWithSet(emailsA map[string]bool, attendeesB []models.Attendee) (int, float64) {
	emailsB := attendeeEmailSet(attendeesB)

	common := 0
	for email := range emailsA {
		if emailsB[email] {
			common++
		}
	}

	maxLen := len(emailsA)
	if len(emailsB) > maxLen {
		maxLen = len(emailsB)
	}

	if maxLen == 0 {
		return 0, 0
	}

	return common, float64(common) / float64(maxLen)
}

func attendeeEmailSet(attendees []models.Attendee) map[string]bool {
	set := make(map[string]bool)
	for _, att := range attendees {
		email := strings.ToLower(strings.TrimSpace(att.Email))
		if email != "" && !isResourceEmail(email) {
			set[email] = true
		}
	}
	return set
}

// isResourceEmail detects room/resource attendees that should be excluded
func isResourceEmail(email string) bool {
	lower := strings.ToLower(email)
	return strings.Contains(lower, "resource.calendar.google.com") ||
		strings.Contains(lower, "room@") ||
		strings.Contains(lower, "conference@")
}

