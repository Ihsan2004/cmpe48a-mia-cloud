package models

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestConsentTypeParity_SQL_vs_Go asserts that the consent_type enum in
// the SQL migration's CHECK constraint exactly matches the set of
// ConsentType* constants defined in this package.
//
// The two lists are the source of truth for what consent_type values
// the system understands; if they drift, the symptoms are obscure
// (constraint violations on insert, repository validations passing
// values the DB rejects, or vice versa). Catching the drift in a unit
// test is cheap; catching it at runtime via a webhook is not.
//
// Parity claim location: docs/consents-and-kvkk.md §Consent Type'lar.
func TestConsentTypeParity_SQL_vs_Go(t *testing.T) {
	t.Parallel()

	// Walk up to repo root looking for the migration file. Tests run
	// from the package dir; the file lives at the repo root's
	// sql/migrations/ path.
	migrationPath := findRepoFile(t, "sql/migrations/020_user_consents.sql")
	raw, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}

	// Pull the CHECK list. The migration shape is:
	//   consent_type      TEXT NOT NULL CHECK (consent_type IN (
	//       'terms_of_service',
	//       'privacy_policy',
	//       ...
	//   )),
	// We extract everything between `consent_type IN (` and the
	// matching `)`, then pick out the quoted strings.
	matcher := regexp.MustCompile(`(?s)consent_type IN \(([^)]+)\)`)
	m := matcher.FindStringSubmatch(string(raw))
	if len(m) != 2 {
		t.Fatalf("could not locate consent_type CHECK list in %s", migrationPath)
	}

	sqlValues := extractQuoted(m[1])
	if len(sqlValues) == 0 {
		t.Fatal("CHECK list parsed empty — migration shape may have changed")
	}

	goValues := []string{
		ConsentTypeTermsOfService,
		ConsentTypePrivacyPolicy,
		ConsentTypeKVKKAydinlatma,
		ConsentTypeCrossBorderTransfer,
		ConsentTypeAIProcessing,
		ConsentTypePilotSozlesmesi,
		ConsentTypeMarketing,
	}

	sort.Strings(sqlValues)
	sort.Strings(goValues)

	if !equalStringSlices(sqlValues, goValues) {
		t.Errorf("consent_type parity drift between SQL migration and Go constants:\n"+
			"  SQL CHECK: %v\n"+
			"  Go constants: %v\n"+
			"Update sql/migrations/020_user_consents.sql AND models.go together.",
			sqlValues, goValues)
	}

	// Belt-and-braces: every value in goValues must also be accepted
	// by IsValidConsentType. Catches the case where the constant is
	// renamed but IsValidConsentType isn't updated.
	for _, v := range goValues {
		if !IsValidConsentType(v) {
			t.Errorf("IsValidConsentType rejects known constant %q", v)
		}
	}
}

// TestMandatoryConsentTypes_AllValid ensures every entry in
// MandatoryConsentTypes is itself a known consent type. Prevents
// typos in the slice literal that would silently let signup pass.
func TestMandatoryConsentTypes_AllValid(t *testing.T) {
	t.Parallel()
	for _, m := range MandatoryConsentTypes {
		if !IsValidConsentType(m) {
			t.Errorf("MandatoryConsentTypes contains unknown type %q", m)
		}
	}
	if len(MandatoryConsentTypes) == 0 {
		t.Error("MandatoryConsentTypes is empty — signup would accept anything")
	}
}

// findRepoFile walks upward from the package dir until it finds a
// directory containing the relative path. Lets the test stay
// location-independent (works when run from package, repo root, or
// CI's odd PWD).
func findRepoFile(t *testing.T, relPath string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 10; i++ {
		candidate := dir + "/" + relPath
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := dir[:strings.LastIndex(dir, "/")]
		if parent == dir || parent == "" {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate %s from any ancestor of %s", relPath, dir)
	return ""
}

// extractQuoted pulls single-quoted strings out of a SQL fragment.
// Trims whitespace; ignores empty matches. Robust against comments
// inside the CHECK list because the regex above already excludes any
// `)` characters.
func extractQuoted(s string) []string {
	re := regexp.MustCompile(`'([^']+)'`)
	matches := re.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) == 2 {
			out = append(out, m[1])
		}
	}
	return out
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
