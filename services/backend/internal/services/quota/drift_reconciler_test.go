package quota

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeDriftRows implements the driftRows interface from drift_reconciler.go.
// Each row is (userID, tier, before, after).
type fakeDriftRows struct {
	rows [][4]any
	i    int
	err  error
}

func (f *fakeDriftRows) Next() bool { return f.i < len(f.rows) }

func (f *fakeDriftRows) Scan(dest ...any) error {
	if len(dest) != 4 {
		return errors.New("expected 4 scan targets")
	}
	row := f.rows[f.i]
	f.i++
	*(dest[0].(*string)) = row[0].(string)
	*(dest[1].(*string)) = row[1].(string)
	*(dest[2].(*float64)) = row[2].(float64)
	*(dest[3].(*float64)) = row[3].(float64)
	return nil
}

func (f *fakeDriftRows) Close()     {}
func (f *fakeDriftRows) Err() error { return f.err }

// fakeDriftDB satisfies driftDB without touching pgx.
type fakeDriftDB struct {
	rows    *fakeDriftRows
	gotArgs []any
	queryErr error
}

func (d *fakeDriftDB) Query(_ context.Context, _ string, args ...any) (driftRows, error) {
	d.gotArgs = args
	if d.queryErr != nil {
		return nil, d.queryErr
	}
	return d.rows, nil
}

func TestReconcile_NoDrift(t *testing.T) {
	db := &fakeDriftDB{rows: &fakeDriftRows{}} // empty rows
	r := &DriftReconciler{db: db, now: func() time.Time { return time.Now().UTC() }}

	n, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 corrections, got %d", n)
	}
}

func TestReconcile_DetectsBothDirections(t *testing.T) {
	// over   = counter 5.0, actual 3.0 → corrected to 3.0, direction "over"
	// under  = counter 1.0, actual 4.0 → corrected to 4.0, direction "under"
	rows := &fakeDriftRows{rows: [][4]any{
		{"user-1", TierFree, 5.0, 3.0},
		{"user-2", TierPOC, 1.0, 4.0},
	}}
	db := &fakeDriftDB{rows: rows}
	r := &DriftReconciler{db: db, now: func() time.Time { return time.Now().UTC() }}

	n, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 corrections, got %d", n)
	}
}

func TestReconcile_PassesNowAndThresholdToQuery(t *testing.T) {
	fixed := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	db := &fakeDriftDB{rows: &fakeDriftRows{}}
	r := &DriftReconciler{db: db, now: func() time.Time { return fixed }}

	_, _ = r.Reconcile(context.Background())
	if len(db.gotArgs) != 2 {
		t.Fatalf("expected 2 query args (now + threshold), got %d", len(db.gotArgs))
	}
	gotTime, ok := db.gotArgs[0].(time.Time)
	if !ok || !gotTime.Equal(fixed) {
		t.Errorf("first arg should be fixed now=%v, got %v", fixed, db.gotArgs[0])
	}
	gotThreshold, ok := db.gotArgs[1].(float64)
	if !ok || gotThreshold != driftThresholdUSD {
		t.Errorf("second arg should be driftThresholdUSD=%v, got %v", driftThresholdUSD, db.gotArgs[1])
	}
}

func TestReconcile_PropagatesQueryError(t *testing.T) {
	db := &fakeDriftDB{queryErr: errors.New("boom")}
	r := &DriftReconciler{db: db, now: func() time.Time { return time.Now().UTC() }}

	_, err := r.Reconcile(context.Background())
	if err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestAbsFloat(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{0, 0},
		{1.5, 1.5},
		{-1.5, 1.5},
	}
	for _, c := range cases {
		if got := absFloat(c.in); got != c.want {
			t.Errorf("absFloat(%v)=%v want %v", c.in, got, c.want)
		}
	}
}
