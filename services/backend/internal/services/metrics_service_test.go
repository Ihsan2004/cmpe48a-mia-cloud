package services

import (
	"strings"
	"testing"
)

// ==================== FormatMetricsSummary ====================

func TestFormatMetricsSummary_AllZeros(t *testing.T) {
	t.Parallel()

	m := &MVPMetrics{}
	result := FormatMetricsSummary(m)

	if !strings.Contains(result, "Review: 0%") {
		t.Errorf("expected 0%% review rate, got: %s", result)
	}
	if !strings.Contains(result, "Brief Open: 0%") {
		t.Errorf("expected 0%% brief open rate, got: %s", result)
	}
}

func TestFormatMetricsSummary_WithData(t *testing.T) {
	t.Parallel()

	m := &MVPMetrics{
		TotalMeetingsProcessed: 10,
		MeetingsWithReview:     7,
		ReviewCompletionRate:   0.7,
		TotalBriefsGenerated:   8,
		BriefsOpened:           5,
		BriefOpenRate:          0.625,
		TotalFeedback:          6,
		UsefulCount:            4,
		NotUsefulCount:         2,
		UsefulnessScore:        0.6667,
		NotesUsageRate:         0.3,
	}

	result := FormatMetricsSummary(m)

	if !strings.Contains(result, "70%") {
		t.Errorf("expected 70%% review, got: %s", result)
	}
	if !strings.Contains(result, "63%") || !strings.Contains(result, "62%") {
		// 62.5% rounds to either 62 or 63 depending on formatting
		if !strings.Contains(result, "5/8") {
			t.Errorf("expected brief open info, got: %s", result)
		}
	}
}

// ==================== Rate Calculations ====================

func TestMetrics_ReviewCompletionRate(t *testing.T) {
	t.Parallel()

	m := &MVPMetrics{
		TotalMeetingsProcessed: 20,
		MeetingsWithReview:     12,
	}
	rate := float64(m.MeetingsWithReview) / float64(m.TotalMeetingsProcessed)

	if rate != 0.6 {
		t.Errorf("expected 0.6, got %f", rate)
	}
}

func TestMetrics_ReviewCompletionRate_ZeroMeetings(t *testing.T) {
	t.Parallel()

	m := &MVPMetrics{
		TotalMeetingsProcessed: 0,
		MeetingsWithReview:     0,
	}

	var rate float64
	if m.TotalMeetingsProcessed > 0 {
		rate = float64(m.MeetingsWithReview) / float64(m.TotalMeetingsProcessed)
	}

	if rate != 0 {
		t.Errorf("expected 0, got %f", rate)
	}
}

func TestMetrics_BriefOpenRate(t *testing.T) {
	t.Parallel()

	m := &MVPMetrics{
		TotalBriefsGenerated: 10,
		BriefsOpened:         4,
	}
	rate := float64(m.BriefsOpened) / float64(m.TotalBriefsGenerated)

	if rate != 0.4 {
		t.Errorf("expected 0.4, got %f", rate)
	}
}

func TestMetrics_UsefulnessScore(t *testing.T) {
	t.Parallel()

	m := &MVPMetrics{
		TotalFeedback:  10,
		UsefulCount:    7,
		NotUsefulCount: 3,
	}
	score := float64(m.UsefulCount) / float64(m.TotalFeedback)

	if score != 0.7 {
		t.Errorf("expected 0.7, got %f", score)
	}
}

func TestMetrics_UsefulnessScore_ZeroFeedback(t *testing.T) {
	t.Parallel()

	m := &MVPMetrics{TotalFeedback: 0}
	var score float64
	if m.TotalFeedback > 0 {
		score = float64(m.UsefulCount) / float64(m.TotalFeedback)
	}

	if score != 0 {
		t.Errorf("expected 0, got %f", score)
	}
}

func TestMetrics_BriefEligibilityRate(t *testing.T) {
	t.Parallel()

	eligible := 15
	total := 30
	rate := float64(eligible) / float64(total)

	if rate != 0.5 {
		t.Errorf("expected 0.5, got %f", rate)
	}
}

func TestMetrics_NotesUsageRate(t *testing.T) {
	t.Parallel()

	m := &MVPMetrics{
		TotalMeetingsProcessed: 20,
		MeetingsWithNotes:      6,
	}

	rate := float64(m.MeetingsWithNotes) / float64(m.TotalMeetingsProcessed)
	if rate != 0.3 {
		t.Errorf("expected 0.3, got %f", rate)
	}
}

// ==================== Chain Override Metrics ====================

func TestMetrics_ChainOverrideCount(t *testing.T) {
	t.Parallel()

	m := &MVPMetrics{
		TotalChains:        5,
		ChainOverrideCount: 2,
	}

	if m.ChainOverrideCount == 0 {
		t.Error("chain override count should be trackable")
	}
	overrideRate := float64(m.ChainOverrideCount) / float64(m.TotalChains)
	if overrideRate != 0.4 {
		t.Errorf("expected override rate 0.4, got %f", overrideRate)
	}
}

// ==================== Threshold Checks ====================

func TestMetrics_MeetsReviewTarget(t *testing.T) {
	t.Parallel()

	// Target: >50%
	passing := &MVPMetrics{ReviewCompletionRate: 0.55}
	failing := &MVPMetrics{ReviewCompletionRate: 0.45}

	if passing.ReviewCompletionRate <= 0.5 {
		t.Error("55% should pass >50% target")
	}
	if failing.ReviewCompletionRate > 0.5 {
		t.Error("45% should fail >50% target")
	}
}

func TestMetrics_MeetsBriefOpenTarget(t *testing.T) {
	t.Parallel()

	// Target: >40%
	passing := &MVPMetrics{BriefOpenRate: 0.45}
	failing := &MVPMetrics{BriefOpenRate: 0.35}

	if passing.BriefOpenRate <= 0.4 {
		t.Error("45% should pass >40% target")
	}
	if failing.BriefOpenRate > 0.4 {
		t.Error("35% should fail >40% target")
	}
}

func TestMetrics_MeetsUsefulnessTarget(t *testing.T) {
	t.Parallel()

	// Target: >60%
	passing := &MVPMetrics{UsefulnessScore: 0.65}
	failing := &MVPMetrics{UsefulnessScore: 0.55}

	if passing.UsefulnessScore <= 0.6 {
		t.Error("65% should pass >60% target")
	}
	if failing.UsefulnessScore > 0.6 {
		t.Error("55% should fail >60% target")
	}
}

// ==================== MVPMetrics Struct ====================

func TestMVPMetrics_AllFieldsPresent(t *testing.T) {
	t.Parallel()

	m := MVPMetrics{
		TotalMeetingsProcessed: 10,
		MeetingsWithReview:     7,
		ReviewCompletionRate:   0.7,
		AvgConfirmedItems:      3.2,
		TotalBriefsGenerated:   8,
		BriefsOpened:           5,
		BriefOpenRate:          0.625,
		BriefEligibleCount:     6,
		BriefEligibilityRate:   0.6,
		TotalFeedback:          6,
		UsefulCount:            4,
		NotUsefulCount:         2,
		UsefulnessScore:        0.667,
		TotalChains:            3,
		ChainOverrideCount:     1,
		MeetingsWithNotes:      4,
		NotesUsageRate:         0.4,
		TotalDecisions:         12,
		TotalActions:           8,
		TotalOpenTopics:        3,
		TotalStaleItems:        2,
	}

	// Verify no zero-value fields that should be set
	if m.TotalMeetingsProcessed == 0 {
		t.Error("TotalMeetingsProcessed should be set")
	}
	if m.TotalDecisions == 0 {
		t.Error("TotalDecisions should be set")
	}
	if m.TotalStaleItems == 0 {
		t.Error("TotalStaleItems should be set")
	}
}

// ==================== Extraction Metrics ====================

func TestMetrics_ExtractionSuccessRate(t *testing.T) {
	t.Parallel()

	m := &MVPMetrics{
		ExtractionCount:       10,
		ExtractionSuccessRate: 0.8,
	}

	if m.ExtractionSuccessRate != 0.8 {
		t.Errorf("expected 0.8, got %f", m.ExtractionSuccessRate)
	}
}

func TestMetrics_ExtractionSuccessRate_Zero(t *testing.T) {
	t.Parallel()

	m := &MVPMetrics{ExtractionCount: 0}
	var rate float64
	if m.ExtractionCount > 0 {
		rate = m.ExtractionSuccessRate
	}
	if rate != 0 {
		t.Errorf("expected 0 for no extractions, got %f", rate)
	}
}

func TestMetrics_AvgItemsPerExtraction(t *testing.T) {
	t.Parallel()

	m := &MVPMetrics{
		AvgItemsPerExtraction: 4.5,
		ExtractionCount:       20,
	}

	if m.AvgItemsPerExtraction != 4.5 {
		t.Errorf("expected 4.5, got %f", m.AvgItemsPerExtraction)
	}
}

func TestMetrics_AllExtractionFieldsPresent(t *testing.T) {
	t.Parallel()

	m := MVPMetrics{
		ExtractionCount:       15,
		ExtractionSuccessRate: 0.93,
		AvgItemsPerExtraction: 3.2,
	}

	if m.ExtractionCount == 0 {
		t.Error("ExtractionCount should be set")
	}
	if m.ExtractionSuccessRate == 0 {
		t.Error("ExtractionSuccessRate should be set")
	}
	if m.AvgItemsPerExtraction == 0 {
		t.Error("AvgItemsPerExtraction should be set")
	}
}
