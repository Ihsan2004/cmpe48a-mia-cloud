package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// TestMetricsEndpoint_Serves asserts that the promhttp-backed endpoint
// routed at /metrics replies 200 with a text/plain exposition and that
// our batch counters are visible by name. This guards the wiring in
// routes.go without depending on route registration.
func TestMetricsEndpoint_Serves(t *testing.T) {
	// Bump a counter so we know the exposition is non-empty. The exact
	// value doesn't matter — we only assert the name appears. CounterVec
	// metrics only surface after at least one label combination has been
	// incremented.
	BatchTranscriptionSubmitted.Inc()
	BatchTranscriptionCompleted.Inc()
	BatchTranscriptionFailed.WithLabelValues(FailStageSubmit).Inc()
	AudioUploadBytes.Add(1024)
	AudioStorageDeleted.Inc()

	app := fiber.New()
	app.Get("/metrics", adaptor.HTTPHandler(promhttp.Handler()))

	req := httptest.NewRequest("GET", "/metrics", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	text := string(body)

	wantNames := []string{
		"batch_transcription_submitted_total",
		"batch_transcription_completed_total",
		"batch_transcription_failed_total",
		"audio_upload_bytes_total",
		"audio_storage_deleted_total",
	}
	for _, n := range wantNames {
		if !strings.Contains(text, n) {
			t.Errorf("metric %q missing from /metrics output", n)
		}
	}
}
