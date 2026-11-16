package handlers

import (
	"testing"
)

func TestParseContentRange_Valid(t *testing.T) {
	cases := []struct {
		header    string
		wantStart int64
		wantEnd   int64
		wantTotal *int64 // nil == "*"
	}{
		{"bytes 0-5242879/31457280", 0, 5242879, ptr(int64(31457280))},
		{"bytes 5242880-10485759/31457280", 5242880, 10485759, ptr(int64(31457280))},
		{"bytes 0-1023/*", 0, 1023, nil},
		{"bytes 0-0/1", 0, 0, ptr(int64(1))},
	}
	for _, tc := range cases {
		cr, err := parseContentRange(tc.header)
		if err != nil {
			t.Errorf("%q: unexpected error %v", tc.header, err)
			continue
		}
		if cr.Start != tc.wantStart || cr.End != tc.wantEnd {
			t.Errorf("%q: range = %d-%d, want %d-%d", tc.header, cr.Start, cr.End, tc.wantStart, tc.wantEnd)
		}
		if (cr.Total == nil) != (tc.wantTotal == nil) {
			t.Errorf("%q: total presence mismatch", tc.header)
			continue
		}
		if cr.Total != nil && *cr.Total != *tc.wantTotal {
			t.Errorf("%q: total = %d, want %d", tc.header, *cr.Total, *tc.wantTotal)
		}
	}
}

func TestParseContentRange_Invalid(t *testing.T) {
	cases := []string{
		"",
		"bytes",
		"bytes 0/100",
		"items 0-10/100",             // wrong unit
		"bytes abc-def/100",          // non-numeric
		"bytes 100-50/100",           // end before start
		"bytes 0-100/-1",             // negative total doesn't match regex
	}
	for _, h := range cases {
		if _, err := parseContentRange(h); err == nil {
			t.Errorf("%q: expected error, got nil", h)
		}
	}
}

func TestErrIsMeetingNotFound(t *testing.T) {
	if !errIsMeetingNotFound(errStr("meeting not found")) {
		t.Error("string 'meeting not found' should be recognised")
	}
	if errIsMeetingNotFound(errStr("something else")) {
		t.Error("unrelated errors should not match")
	}
	if errIsMeetingNotFound(nil) {
		t.Error("nil should not match")
	}
}

func TestMax64(t *testing.T) {
	if max64(3, 5) != 5 || max64(5, 3) != 5 || max64(-1, 0) != 0 {
		t.Error("max64 incorrect")
	}
}

// --- helpers ---

func ptr[T any](v T) *T { return &v }

type errStr string

func (e errStr) Error() string { return string(e) }
