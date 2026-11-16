package services

import (
	"sync/atomic"
	"testing"
)

// ==================== IsConfigured Tests ====================

func TestEmailService_IsConfigured_AllSet(t *testing.T) {
	t.Parallel()

	svc := &EmailService{
		smtpHost:     "smtp.gmail.com",
		smtpPort:     "587",
		smtpUser:     "user@gmail.com",
		smtpPassword: "app-password",
		fromEmail:    "noreply@example.com",
	}

	if !svc.IsConfigured() {
		t.Error("expected IsConfigured=true when all SMTP fields set")
	}
}

func TestEmailService_IsConfigured_MissingHost(t *testing.T) {
	t.Parallel()

	svc := &EmailService{
		smtpHost:     "",
		smtpPort:     "587",
		smtpUser:     "user@gmail.com",
		smtpPassword: "app-password",
		fromEmail:    "noreply@example.com",
	}

	if svc.IsConfigured() {
		t.Error("expected IsConfigured=false when host is empty")
	}
}

func TestEmailService_IsConfigured_MissingPassword(t *testing.T) {
	t.Parallel()

	svc := &EmailService{
		smtpHost:     "smtp.gmail.com",
		smtpPort:     "587",
		smtpUser:     "user@gmail.com",
		smtpPassword: "",
		fromEmail:    "noreply@example.com",
	}

	if svc.IsConfigured() {
		t.Error("expected IsConfigured=false when password is empty")
	}
}

func TestEmailService_IsConfigured_AllEmpty(t *testing.T) {
	t.Parallel()

	svc := &EmailService{}
	if svc.IsConfigured() {
		t.Error("expected IsConfigured=false when all fields empty")
	}
}

// ==================== Email Queue Tests ====================

func TestEmailQueue_SendWhenFull(t *testing.T) {
	t.Parallel()

	svc := &EmailService{
		smtpHost:     "smtp.test.com",
		smtpPort:     "587",
		smtpUser:     "test",
		smtpPassword: "test",
		fromEmail:    "test@test.com",
		jobQueue:     make(chan EmailJob, 2), // tiny buffer
	}

	// Fill the queue
	svc.jobQueue <- EmailJob{To: "a@test.com", Subject: "1", Body: "1"}
	svc.jobQueue <- EmailJob{To: "b@test.com", Subject: "2", Body: "2"}

	// Next send should fail (queue full)
	err := svc.sendEmail("c@test.com", "3", "3")
	if err == nil {
		t.Error("expected error when queue is full")
	}

	// Verify dropped counter incremented
	if atomic.LoadInt64(&svc.droppedCount) != 1 {
		t.Errorf("expected droppedCount=1, got %d", atomic.LoadInt64(&svc.droppedCount))
	}
}

func TestEmailQueue_SendWhenAvailable(t *testing.T) {
	t.Parallel()

	svc := &EmailService{
		smtpHost:     "smtp.test.com",
		smtpPort:     "587",
		smtpUser:     "test",
		smtpPassword: "test",
		fromEmail:    "test@test.com",
		jobQueue:     make(chan EmailJob, 10),
	}

	err := svc.sendEmail("test@test.com", "Test Subject", "<p>Hello</p>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify job was queued
	if len(svc.jobQueue) != 1 {
		t.Errorf("expected 1 job in queue, got %d", len(svc.jobQueue))
	}

	// Verify job contents
	job := <-svc.jobQueue
	if job.To != "test@test.com" {
		t.Errorf("expected to=test@test.com, got %s", job.To)
	}
	if job.Subject != "Test Subject" {
		t.Errorf("expected subject=Test Subject, got %s", job.Subject)
	}
}

// ==================== EmailJob Tests ====================

func TestEmailJob_RetryCount(t *testing.T) {
	t.Parallel()

	job := EmailJob{
		To:         "test@test.com",
		Subject:    "Test",
		Body:       "Body",
		RetryCount: 0,
	}

	if job.RetryCount != 0 {
		t.Errorf("expected initial retry count 0, got %d", job.RetryCount)
	}

	job.RetryCount++
	if job.RetryCount != 1 {
		t.Errorf("expected retry count 1 after increment, got %d", job.RetryCount)
	}
}

// ==================== Mock SMTP (sendEmailInternal) ====================

func TestSendEmailInternal_EmptyHost_MockSuccess(t *testing.T) {
	t.Parallel()

	// When smtpHost is empty, sendEmailInternal returns nil (mock mode)
	svc := &EmailService{
		smtpHost:  "",
		fromEmail: "test@test.com",
	}

	err := svc.sendEmailInternal("to@test.com", "Subject", "<p>Body</p>")
	if err != nil {
		t.Errorf("expected nil error in mock mode, got %v", err)
	}
}
