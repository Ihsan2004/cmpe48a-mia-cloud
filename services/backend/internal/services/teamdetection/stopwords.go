package teamdetection

import "strings"

// Stopwords is the TR + EN starter set for SuggestedName tokenization
// (CONTEXT.md D-10). Exported as a var (not a const list) so future
// fixture observations can expand it without touching score.go.
//
// All entries are lowercase. The matcher in score.go lowercases tokens
// before comparison.
//
// Phase 6 starts with this set; plan 06-02's stopword unit test verifies
// every TR + EN word listed in CONTEXT.md D-10 is present.
//
// MAINTAINER NOTE: Go's strings.ToLower is Unicode-default, not Turkish-locale-aware.
// Tokens starting with İ (dotted capital I) will NOT match a lowercase 'i'-prefixed entry.
// Prefer entries without İ where possible, or use golang.org/x/text/cases for Turkish casing.
var Stopwords = map[string]bool{
	// Turkish
	"haftalık": true,
	"günlük":   true,
	"toplantı": true,
	"ile":      true,
	"ve":       true,
	"sync":     true, // appears in both TR and EN as a loan-word

	// English
	"weekly":  true,
	"daily":   true,
	"meeting": true,
	"standup": true,
	"1:1":     true,
	"review":  true,
	"call":    true,
	"chat":    true,
	"the":     true,
	"a":       true,
	"and":     true,
	"or":      true,
	"with":    true,
	"of":      true,
	"for":     true,
	"to":      true,
	"in":      true,
}

// IsStopword returns true if token (after lowercasing) is in the
// Stopwords set. Single-char tokens are also stopwords by convention
// (D-10 — "common single-char tokens").
func IsStopword(token string) bool {
	t := strings.ToLower(token)
	if len(t) <= 1 {
		return true
	}
	return Stopwords[t]
}
