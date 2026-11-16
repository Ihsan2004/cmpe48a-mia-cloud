package services

import (
	"bytes"
	"fmt"
	"html/template"
	"log/slog"
	"net/smtp"
	"os"
	"sync/atomic"
	"time"
)

// EmailJob represents an email sending task
type EmailJob struct {
	To         string
	Subject    string
	Body       string
	RetryCount int
	NextTry    time.Time
}

// EmailService handles sending email notifications
type EmailService struct {
	smtpHost     string
	smtpPort     string
	smtpUser     string
	smtpPassword string
	fromEmail    string
	fromName     string
	dashboardURL string

	jobQueue chan EmailJob
	done     chan bool

	// Tracking metrics for dropped/failed emails
	droppedCount int64 // Atomic counter for emails dropped due to full queue
	sentCount    int64 // Atomic counter for successfully sent emails
	failedCount  int64 // Atomic counter for failed emails (after all retries)
}

// NewEmailService creates a new EmailService instance
func NewEmailService() *EmailService {
	dashboardURL := os.Getenv("FRONTEND_URL")
	if dashboardURL == "" {
		slog.Warn("FRONTEND_URL not set, email links may not work correctly")
		dashboardURL = "" // Don't use localhost fallback in production
	}

	service := &EmailService{
		smtpHost:     os.Getenv("SMTP_HOST"),
		smtpPort:     os.Getenv("SMTP_PORT"),
		smtpUser:     os.Getenv("SMTP_USER"),
		smtpPassword: os.Getenv("SMTP_PASSWORD"),
		fromEmail:    os.Getenv("SMTP_FROM_EMAIL"),
		fromName:     os.Getenv("SMTP_FROM_NAME"),
		dashboardURL: dashboardURL,
		jobQueue:     make(chan EmailJob, 100), // Buffer size 100
		done:         make(chan bool),
	}

	// Start worker pool (3 workers)
	for i := 0; i < 3; i++ {
		go service.worker(i)
	}

	return service
}

// Close gracefully shuts down the email service
func (s *EmailService) Close() {
	close(s.jobQueue)
	// In a real app we might want to wait for workers
}

// IsConfigured checks if email service is properly configured
func (s *EmailService) IsConfigured() bool {

	result := s.smtpHost != "" && s.smtpPort != "" && s.smtpUser != "" && s.smtpPassword != "" && s.fromEmail != ""

	return result
}
func (s *EmailService) worker(id int) {
	slog.Info("email worker started", "worker_id", id)
	for job := range s.jobQueue {
		// Respect retry delay if needed
		if time.Now().Before(job.NextTry) {
			time.Sleep(time.Until(job.NextTry))
		}

		if err := s.sendEmailInternal(job.To, job.Subject, job.Body); err != nil {
			slog.Error("failed to send email", "worker_id", id, "to", job.To, "error", err)

			// Retry Logic
			if job.RetryCount < 3 {
				job.RetryCount++
				// Exponential backoff: 1m, 2m, 4m
				delay := time.Duration(1<<job.RetryCount) * time.Minute // 2^RetryCount minutes
				// For testing purposes, make it faster if in dev mode? No, keep logic simple.
				// Actually, huge delays block the worker if we sleep.
				// Better approach: Re-queue with Future Time?
				// Simple channel queue doesn't support future validation easily without blocking.
				// Hack: Launch a goroutine to requeue after delay
				go func(j EmailJob, d time.Duration) {
					slog.Info("re-queueing email", "to", j.To, "delay", d, "attempt", j.RetryCount)
					time.Sleep(d)
					j.NextTry = time.Now()
					s.jobQueue <- j
				}(job, delay)
			} else {
				slog.Error("max retries reached, dropping email", "worker_id", id, "to", job.To)
				// Track failed emails after max retries
				atomic.AddInt64(&s.failedCount, 1)
			}
		} else {
			// Track successful sends
			atomic.AddInt64(&s.sentCount, 1)
			slog.Info("email sent", "worker_id", id, "to", job.To)
		}
	}
}

// sendEmail queues an email for sending
func (s *EmailService) sendEmail(to, subject, htmlBody string) error {
	job := EmailJob{
		To:      to,
		Subject: subject,
		Body:    htmlBody,
		NextTry: time.Now(),
	}

	// Non-blocking enqueue if buffer is full, or blocking?
	// Blocking is safer to ensure we don't drop, but buffer 100 should be enough.
	select {
	case s.jobQueue <- job:
		return nil
	default:
		// Track dropped emails for monitoring
		dropped := atomic.AddInt64(&s.droppedCount, 1)
		slog.Warn("email queue full, dropping email", "to", to, "total_dropped", dropped, "queue_size", len(s.jobQueue))
		return fmt.Errorf("email queue full (capacity: 100, dropped total: %d), email to %s was not queued", dropped, to)
	}
}

// sendEmailInternal sends an HTML email via SMTP (Actual logic)
func (s *EmailService) sendEmailInternal(to, subject, htmlBody string) error {
	fromName := s.fromName
	if fromName == "" {
		fromName = "MIA Meeting Assistant"
	}

	// Build email headers
	headers := make(map[string]string)
	headers["From"] = fmt.Sprintf("%s <%s>", fromName, s.fromEmail)
	headers["To"] = to
	headers["Subject"] = subject
	headers["MIME-Version"] = "1.0"
	headers["Content-Type"] = "text/html; charset=UTF-8"

	// Build message
	var msg bytes.Buffer
	for k, v := range headers {
		msg.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
	}
	msg.WriteString("\r\n")
	msg.WriteString(htmlBody)

	// SMTP authentication
	auth := smtp.PlainAuth("", s.smtpUser, s.smtpPassword, s.smtpHost)

	// Send email
	addr := fmt.Sprintf("%s:%s", s.smtpHost, s.smtpPort)

	// In development/mock mode check
	if s.smtpHost == "" {
		// Mock success in logs
		// fmt.Printf("[MOCK SMTP] Sending to %s...\n", to)
		return nil
	}

	err := smtp.SendMail(addr, auth, s.fromEmail, []string{to}, msg.Bytes())

	if err != nil {
		return fmt.Errorf("SMTP send failed: %w", err)
	}

	return nil
}

// SendPasswordResetEmail sends a password reset link to the user
func (s *EmailService) SendPasswordResetEmail(email, resetToken, fullName string) error {
	resetLink := fmt.Sprintf("%s/reset-password?token=%s", s.dashboardURL, resetToken)

	if !s.IsConfigured() {
		slog.Info("Mock email - password reset", "to", email, "subject", "Şifre Sıfırlama İstedi", "reset_link", resetLink)
		return fmt.Errorf("email service not configured (check console for link)")
	}

	data := struct {
		FullName    string
		ResetLink   string
		CurrentYear int
	}{
		FullName:    fullName,
		ResetLink:   resetLink,
		CurrentYear: time.Now().Year(),
	}

	// Parse template
	tmpl, err := template.New("reset").Parse(passwordResetEmailTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse reset template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute reset template: %w", err)
	}

	subject := "Şifre Sıfırlama İstedi"
	return s.sendEmail(email, subject, buf.String())
}

const passwordResetEmailTemplate = `<!DOCTYPE html>
<html lang="tr">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Şifre Sıfırlama</title>
</head>
<body style="margin: 0; padding: 0; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif; background-color: #0f0f0f; color: #ffffff;">
    <table role="presentation" style="width: 100%; border-collapse: collapse;">
        <tr>
            <td style="padding: 40px 20px;">
                <table role="presentation" style="max-width: 600px; margin: 0 auto; background-color: #1a1a1a; border-radius: 16px; overflow: hidden; box-shadow: 0 4px 20px rgba(0, 0, 0, 0.3);">
                    <!-- Header -->
                    <tr>
                        <td style="background: linear-gradient(135deg, #8B5CF6 0%, #3B82F6 100%); padding: 30px 40px; text-align: center;">
                            <h1 style="margin: 0; color: #ffffff; font-size: 24px; font-weight: 700;">
                                🔒 Şifre Sıfırlama
                            </h1>
                        </td>
                    </tr>
                    
                    <!-- Content -->
                    <tr>
                        <td style="padding: 30px 40px; border-bottom: 1px solid #2a2a2a;">
                            <h2 style="margin: 0 0 15px 0; color: #ffffff; font-size: 20px; font-weight: 600;">
                                Merhaba {{.FullName}},
                            </h2>
                            <p style="margin: 0 0 20px 0; color: #d1d1d1; font-size: 15px; line-height: 1.7;">
                                Şifrenizi sıfırlamak için bir istek aldık. Eğer bu isteği siz yapmadıysanız, bu emaili görmezden gelebilirsiniz.
                            </p>
							<p style="margin: 0 0 20px 0; color: #d1d1d1; font-size: 15px; line-height: 1.7;">
                                Şifrenizi sıfırlamak için aşağıdaki butona tıklayın:
                            </p>
                        </td>
                    </tr>
                    
                    <!-- CTA Button -->
                    <tr>
                        <td style="padding: 10px 40px 30px 40px; text-align: center; border-bottom: 1px solid #2a2a2a;">
                            <a href="{{.ResetLink}}" 
                               style="display: inline-block; background: linear-gradient(135deg, #8B5CF6 0%, #7C3AED 100%); color: #ffffff; text-decoration: none; padding: 14px 32px; border-radius: 10px; font-size: 15px; font-weight: 600; box-shadow: 0 4px 15px rgba(139, 92, 246, 0.4);">
                                Şifremi Sıfırla →
                            </a>
							<p style="margin: 20px 0 0 0; color: #666666; font-size: 13px;">
                                Bu link 1 saat süreyle geçerlidir.
                            </p>
                        </td>
                    </tr>
                    
                    <!-- Footer -->
                    <tr>
                        <td style="padding: 25px 40px; background-color: #141414; text-align: center;">
                            <p style="margin: 0 0 10px 0; color: #666666; font-size: 13px;">
                                Bu email <strong style="color: #8B5CF6;">MIA Meeting Assistant</strong> tarafından otomatik olarak gönderilmiştir.
                            </p>
                            <p style="margin: 0; color: #444444; font-size: 12px;">
                                © {{.CurrentYear}} MIA. Tüm hakları saklıdır.
                            </p>
                        </td>
                    </tr>
                </table>
            </td>
        </tr>
    </table>
</body>
</html>`

// SendCalendarAuthEmail sends an onboarding email with calendar authorization link
func (s *EmailService) SendCalendarAuthEmail(email, fullName, authURL string) error {
	if !s.IsConfigured() {
		slog.Info("Mock email - calendar auth onboarding", "to", email, "subject", "MIA Onboarding", "auth_url", authURL)
		return fmt.Errorf("email service not configured (check console for link)")
	}

	data := struct {
		FullName    string
		AuthURL     string
		CurrentYear int
	}{
		FullName:    fullName,
		AuthURL:     authURL,
		CurrentYear: time.Now().Year(),
	}

	// Parse template
	tmpl, err := template.New("signup").Parse(calendarAuthEmailTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse signup template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute signup template: %w", err)
	}

	subject := "MIA'ya Hoş Geldin! 🚀 Hesabını Onayla ve Takvimini Bağla"
	return s.sendEmail(email, subject, buf.String())
}

const calendarAuthEmailTemplate = `<!DOCTYPE html>
<html lang="tr">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>MIA'ya Hoş Geldin</title>
</head>
<body style="margin: 0; padding: 0; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif; background-color: #0f0f0f; color: #ffffff;">
    <table role="presentation" style="width: 100%; border-collapse: collapse;">
        <tr>
            <td style="padding: 40px 20px;">
                <table role="presentation" style="max-width: 600px; margin: 0 auto; background-color: #1a1a1a; border-radius: 16px; overflow: hidden; box-shadow: 0 4px 20px rgba(0, 0, 0, 0.3);">
                    <!-- Header -->
                    <tr>
                        <td style="background: linear-gradient(135deg, #8B5CF6 0%, #3B82F6 100%); padding: 30px 40px; text-align: center;">
                            <h1 style="margin: 0; color: #ffffff; font-size: 24px; font-weight: 700;">
                                👋 MIA'ya Hoş Geldin!
                            </h1>
                        </td>
                    </tr>
                    
                    <!-- Content -->
                    <tr>
                        <td style="padding: 30px 40px; border-bottom: 1px solid #2a2a2a;">
                            <h2 style="margin: 0 0 15px 0; color: #ffffff; font-size: 20px; font-weight: 600;">
                                Merhaba {{.FullName}},
                            </h2>
                            <p style="margin: 0 0 20px 0; color: #d1d1d1; font-size: 15px; line-height: 1.7;">
                                MIA dünyasına hoş geldin. Toplantılarını otomatik analiz edip özetleyebilmem için Google Takvim erişimine ihtiyacım var.
                            </p>
                            <p style="margin: 0 0 20px 0; color: #d1d1d1; font-size: 15px; line-height: 1.7;">
                                Hesabını aktifleştirmek ve MIA'nın Antigravity tabanlı ajan özelliklerini kullanmaya başlamak için lütfen aşağıdaki bağlantıyı kullan:
                            </p>
                        </td>
                    </tr>
                    
                    <!-- CTA Button -->
                    <tr>
                        <td style="padding: 10px 40px 30px 40px; text-align: center; border-bottom: 1px solid #2a2a2a;">
                            <a href="{{.AuthURL}}" 
                               style="display: inline-block; background: linear-gradient(135deg, #8B5CF6 0%, #7C3AED 100%); color: #ffffff; text-decoration: none; padding: 14px 32px; border-radius: 10px; font-size: 15px; font-weight: 600; box-shadow: 0 4px 15px rgba(139, 92, 246, 0.4);">
                                Takvimi Bağla ve Hesabı Doğrula →
                            </a>
                        </td>
                    </tr>
                    
                    <!-- Footer -->
                    <tr>
                        <td style="padding: 25px 40px; background-color: #141414; text-align: center;">
                            <p style="margin: 0 0 10px 0; color: #666666; font-size: 13px;">
                                Bu email <strong style="color: #8B5CF6;">MIA Meeting Assistant</strong> tarafından otomatik olarak gönderilmiştir.
                            </p>
                            <p style="margin: 0; color: #444444; font-size: 12px;">
                                © {{.CurrentYear}} MIA. Tüm hakları saklıdır.
                            </p>
                        </td>
                    </tr>
                </table>
            </td>
        </tr>
    </table>
</body>
</html>`

