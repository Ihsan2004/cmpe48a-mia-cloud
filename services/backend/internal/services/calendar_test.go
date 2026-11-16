package services

import (
	"testing"
)

// ==================== extractURLFromText ====================

func TestExtractURLFromText_GoogleMeet(t *testing.T) {
	t.Parallel()

	url := extractURLFromText("Join at https://meet.google.com/abc-defg-hij")
	if url == nil {
		t.Fatal("expected Google Meet URL")
	}
	if *url != "https://meet.google.com/abc-defg-hij" {
		t.Errorf("expected Google Meet URL, got %s", *url)
	}
}

func TestExtractURLFromText_Zoom(t *testing.T) {
	t.Parallel()

	url := extractURLFromText("Meeting link: https://zoom.us/j/123456789?pwd=abc123")
	if url == nil {
		t.Fatal("expected Zoom URL")
	}
	if *url != "https://zoom.us/j/123456789?pwd=abc123" {
		t.Errorf("expected Zoom URL, got %s", *url)
	}
}

func TestExtractURLFromText_ZoomSubdomain(t *testing.T) {
	t.Parallel()

	url := extractURLFromText("https://company.zoom.us/j/987654321")
	if url == nil {
		t.Fatal("expected Zoom subdomain URL")
	}
}

func TestExtractURLFromText_Teams(t *testing.T) {
	t.Parallel()

	url := extractURLFromText("Join Teams meeting: https://teams.microsoft.com/l/meetup-join/abc123")
	if url == nil {
		t.Fatal("expected Teams URL")
	}
}

func TestExtractURLFromText_Webex(t *testing.T) {
	t.Parallel()

	url := extractURLFromText("https://company.webex.com/meet/johndoe")
	if url == nil {
		t.Fatal("expected Webex URL")
	}
}

func TestExtractURLFromText_NoURL(t *testing.T) {
	t.Parallel()

	url := extractURLFromText("This is a regular description with no meeting link")
	if url != nil {
		t.Errorf("expected nil for no meeting URL, got %s", *url)
	}
}

func TestExtractURLFromText_NonMeetingURL(t *testing.T) {
	t.Parallel()

	url := extractURLFromText("Check https://google.com/search?q=hello for details")
	if url != nil {
		t.Errorf("expected nil for non-meeting URL, got %s", *url)
	}
}

func TestExtractURLFromText_Empty(t *testing.T) {
	t.Parallel()

	url := extractURLFromText("")
	if url != nil {
		t.Error("expected nil for empty string")
	}
}

func TestExtractURLFromText_CaseInsensitive(t *testing.T) {
	t.Parallel()

	url := extractURLFromText("HTTPS://MEET.GOOGLE.COM/abc-defg-hij")
	if url == nil {
		t.Fatal("expected case-insensitive match for Google Meet")
	}
}

// ==================== GetWebhookURL ====================

func TestGetWebhookURL_WithWebhookBaseURL(t *testing.T) {
	t.Setenv("WEBHOOK_BASE_URL", "https://api.example.com")
	t.Setenv("FRONTEND_URL", "")

	url := GetWebhookURL()
	if url != "https://api.example.com/api/webhooks/google-calendar" {
		t.Errorf("expected webhook URL, got %s", url)
	}
}

func TestGetWebhookURL_WithTrailingSlash(t *testing.T) {
	t.Setenv("WEBHOOK_BASE_URL", "https://api.example.com/")
	t.Setenv("FRONTEND_URL", "")

	url := GetWebhookURL()
	if url != "https://api.example.com/api/webhooks/google-calendar" {
		t.Errorf("expected no double slash, got %s", url)
	}
}

func TestGetWebhookURL_StripsDoubleApi(t *testing.T) {
	t.Setenv("WEBHOOK_BASE_URL", "https://api.example.com/api")
	t.Setenv("FRONTEND_URL", "")

	url := GetWebhookURL()
	if url != "https://api.example.com/api/webhooks/google-calendar" {
		t.Errorf("expected no /api/api, got %s", url)
	}
}

func TestGetWebhookURL_FallsBackToFrontendURL(t *testing.T) {
	t.Setenv("WEBHOOK_BASE_URL", "")
	t.Setenv("FRONTEND_URL", "https://console.getmia.live")

	url := GetWebhookURL()
	if url != "https://console.getmia.live/api/webhooks/google-calendar" {
		t.Errorf("expected frontend URL fallback, got %s", url)
	}
}

func TestGetWebhookURL_FallsBackFromSyncrunConsoleToAPI(t *testing.T) {
	t.Setenv("WEBHOOK_BASE_URL", "")
	t.Setenv("FRONTEND_URL", "https://console.syncrun.net")

	url := GetWebhookURL()
	if url != "https://api.syncrun.net/api/webhooks/google-calendar" {
		t.Errorf("expected Syncrun API webhook URL, got %s", url)
	}
}

func TestGetWebhookURL_NoEnvVars(t *testing.T) {
	t.Setenv("WEBHOOK_BASE_URL", "")
	t.Setenv("FRONTEND_URL", "")

	url := GetWebhookURL()
	// Should still produce a path (empty base + /api/webhooks/...)
	if url != "/api/webhooks/google-calendar" {
		t.Errorf("expected path-only fallback, got %s", url)
	}
}

// ==================== Google OAuth refresh config ====================

func TestGetRefreshOAuthConfigs_IncludesDesktopFallback(t *testing.T) {
	t.Setenv("GOOGLE_CLIENT_ID", "web-client.apps.googleusercontent.com")
	t.Setenv("GOOGLE_CLIENT_SECRET", "web-secret")
	t.Setenv("GOOGLE_REDIRECT_URI", "https://console.syncrun.net/auth/callback")
	t.Setenv("GOOGLE_DESKTOP_CLIENT_ID", "desktop-client.apps.googleusercontent.com")
	t.Setenv("GOOGLE_DESKTOP_CLIENT_SECRET", "")
	t.Setenv("GOOGLE_LEGACY_CLIENT_ID", "")
	t.Setenv("GOOGLE_LEGACY_CLIENT_SECRET", "")

	configs, err := NewCalendarService().getRefreshOAuthConfigs()
	if err != nil {
		t.Fatalf("expected configs, got error: %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("expected web + desktop configs, got %d", len(configs))
	}
	if configs[0].name != "web" || configs[0].config.ClientID != "web-client.apps.googleusercontent.com" {
		t.Fatalf("expected web config first, got %+v", configs[0])
	}
	if configs[1].name != "desktop" || configs[1].config.ClientID != "desktop-client.apps.googleusercontent.com" {
		t.Fatalf("expected desktop fallback config, got %+v", configs[1])
	}
	if configs[1].config.ClientSecret != "" {
		t.Fatalf("expected empty desktop client secret, got %q", configs[1].config.ClientSecret)
	}
}

func TestGetRefreshOAuthConfigs_SkipsDuplicateDesktopClient(t *testing.T) {
	t.Setenv("GOOGLE_CLIENT_ID", "same-client.apps.googleusercontent.com")
	t.Setenv("GOOGLE_CLIENT_SECRET", "web-secret")
	t.Setenv("GOOGLE_REDIRECT_URI", "https://console.syncrun.net/auth/callback")
	t.Setenv("GOOGLE_DESKTOP_CLIENT_ID", "same-client.apps.googleusercontent.com")
	t.Setenv("GOOGLE_LEGACY_CLIENT_ID", "")
	t.Setenv("GOOGLE_LEGACY_CLIENT_SECRET", "")

	configs, err := NewCalendarService().getRefreshOAuthConfigs()
	if err != nil {
		t.Fatalf("expected configs, got error: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected only web config, got %d", len(configs))
	}
}

func TestGetRefreshOAuthConfigs_IncludesLegacyFallback(t *testing.T) {
	t.Setenv("GOOGLE_CLIENT_ID", "web-client.apps.googleusercontent.com")
	t.Setenv("GOOGLE_CLIENT_SECRET", "web-secret")
	t.Setenv("GOOGLE_REDIRECT_URI", "https://console.syncrun.net/auth/callback")
	t.Setenv("GOOGLE_DESKTOP_CLIENT_ID", "desktop-client.apps.googleusercontent.com")
	t.Setenv("GOOGLE_DESKTOP_CLIENT_SECRET", "desktop-secret")
	t.Setenv("GOOGLE_LEGACY_CLIENT_ID", "legacy-client.apps.googleusercontent.com")
	t.Setenv("GOOGLE_LEGACY_CLIENT_SECRET", "legacy-secret")

	configs, err := NewCalendarService().getRefreshOAuthConfigs()
	if err != nil {
		t.Fatalf("expected configs, got error: %v", err)
	}
	if len(configs) != 3 {
		t.Fatalf("expected web + desktop + legacy configs, got %d", len(configs))
	}
	if configs[2].name != "legacy" || configs[2].config.ClientID != "legacy-client.apps.googleusercontent.com" {
		t.Fatalf("expected legacy fallback config, got %+v", configs[2])
	}
	if configs[2].config.ClientSecret != "legacy-secret" {
		t.Fatalf("expected legacy client secret, got %q", configs[2].config.ClientSecret)
	}
}

func TestIsGoogleReconnectRequiredError_ScopeInsufficient(t *testing.T) {
	err := assertAnError("googleapi: Error 403: ACCESS_TOKEN_SCOPE_INSUFFICIENT insufficientPermissions")
	if !isGoogleReconnectRequiredError(err) {
		t.Fatal("expected insufficient calendar scope to require reconnect")
	}
}

type assertAnError string

func (e assertAnError) Error() string {
	return string(e)
}
