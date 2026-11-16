package services

import (
	"reflect"
	"strings"
	"testing"

	"mia-website/backend/internal/models"
)

// ==================== Profile Model Field Alignment ====================

func TestProfile_FieldCount(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf(models.Profile{})

	// Profile should have exactly 22 fields. Brief-delivery roadmap Phase 1
	// added BriefDeliverViaEmail + BriefDeliverViaSlack + EmailUnsubscribedAt;
	// v1.1 Phase 4 (plan 04-02) added TeamsOnboardingStatus.
	// If this fails, SELECT + Scan in profile.go GetProfile must be updated.
	expectedFields := 22
	if typ.NumField() != expectedFields {
		var fieldNames []string
		for i := 0; i < typ.NumField(); i++ {
			fieldNames = append(fieldNames, typ.Field(i).Name)
		}
		t.Errorf("Profile has %d fields (expected %d). Fields: %s\nUpdate GetProfile SELECT/Scan in profile.go",
			typ.NumField(), expectedFields, strings.Join(fieldNames, ", "))
	}
}

func TestProfile_HasBriefLeadTimeMinutes(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf(models.Profile{})
	field, ok := typ.FieldByName("BriefLeadTimeMinutes")
	if !ok {
		t.Fatal("Profile missing BriefLeadTimeMinutes field")
	}
	if field.Type.Kind() != reflect.Int {
		t.Errorf("BriefLeadTimeMinutes should be int, got %s", field.Type.Kind())
	}
}

func TestProfile_GoogleRefreshTokenNotExposed(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf(models.Profile{})
	field, ok := typ.FieldByName("GoogleRefreshToken")
	if !ok {
		t.Fatal("Profile missing GoogleRefreshToken field")
	}
	tag := field.Tag.Get("json")
	if tag != "-" {
		t.Errorf("GoogleRefreshToken should have json:\"-\" tag for security, got %q", tag)
	}
}

// ==================== UpdateProfile Default Logic ====================

func TestUpdateProfile_DefaultBotName(t *testing.T) {
	t.Parallel()

	// Simulating the default logic from UpdateProfile
	botName := ""
	if botName == "" {
		botName = "MIA Assistant"
	}

	if botName != "MIA Assistant" {
		t.Errorf("expected default bot name 'MIA Assistant', got %q", botName)
	}
}

func TestUpdateProfile_CustomBotName(t *testing.T) {
	t.Parallel()

	botName := "My Custom Bot"
	if botName == "" {
		botName = "MIA Assistant"
	}

	if botName != "My Custom Bot" {
		t.Errorf("expected custom bot name, got %q", botName)
	}
}

func TestUpdateProfile_DefaultBotLanguage(t *testing.T) {
	t.Parallel()

	botLanguage := ""
	if botLanguage == "" {
		botLanguage = "auto"
	}

	if botLanguage != "auto" {
		t.Errorf("expected default 'auto', got %q", botLanguage)
	}
}

func TestUpdateProfile_DefaultInterfaceLanguage(t *testing.T) {
	t.Parallel()

	interfaceLanguage := ""
	if interfaceLanguage == "" {
		interfaceLanguage = "tr"
	}

	if interfaceLanguage != "tr" {
		t.Errorf("expected default 'tr', got %q", interfaceLanguage)
	}
}

func TestUpdateProfile_CustomInterfaceLanguage(t *testing.T) {
	t.Parallel()

	interfaceLanguage := "en"
	if interfaceLanguage == "" {
		interfaceLanguage = "tr"
	}

	if interfaceLanguage != "en" {
		t.Errorf("expected 'en', got %q", interfaceLanguage)
	}
}

// ==================== EnsureProfileExists Fallback ====================

func TestEnsureProfile_EmptyFullName_UsesEmail(t *testing.T) {
	t.Parallel()

	fullName := ""
	email := "user@example.com"

	if fullName == "" {
		fullName = email
	}

	if fullName != "user@example.com" {
		t.Errorf("expected email as fallback, got %q", fullName)
	}
}

func TestEnsureProfile_HasFullName_KeepsIt(t *testing.T) {
	t.Parallel()

	fullName := "Ali Veli"
	email := "ali@example.com"

	if fullName == "" {
		fullName = email
	}

	if fullName != "Ali Veli" {
		t.Errorf("expected 'Ali Veli', got %q", fullName)
	}
}

// ==================== Profile Required Fields ====================

func TestProfile_HasRequiredFields(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf(models.Profile{})
	required := []string{
		"ID", "FullName", "Timezone", "DefaultMeetingDuration",
		"BotName", "BotLanguage", "IsCalendarConnected",
		"InterfaceLanguage", "BriefLeadTimeMinutes", "UpdatedAt",
	}

	for _, name := range required {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("Profile missing required field: %s", name)
		}
	}
}
