package main

import (
	"fmt"
	"log"
	"os"

	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run trigger_correction.go <meeting_id>")
		return
	}

	meetingIDStr := os.Args[1]
	meetingID, err := uuid.Parse(meetingIDStr)
	if err != nil {
		log.Fatalf("Invalid meeting ID: %v", err)
	}

	// Load .env
	_ = godotenv.Load()

	if err := repository.Connect(); err != nil {
		log.Fatalf("Failed to initialize DB: %v", err)
	}

	correctionService := services.NewCorrectionService()
	log.Printf("🚀 Manually triggering correction for meeting: %s", meetingID)

	err = correctionService.ProcessMeetingTranscript(meetingID)
	if err != nil {
		log.Fatalf("❌ Correction failed: %v", err)
	}

	log.Println("✅ Correction completed successfully!")
}
