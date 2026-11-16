package handlers

// User-friendly error messages in Turkish

const (
	// Authentication & Authorization Errors
	ErrUnauthorized        = "Oturumunuz sonlandı. Lütfen tekrar giriş yapın."
	ErrInvalidToken        = "Geçersiz veya süresi dolmuş oturum. Lütfen tekrar giriş yapın."
	ErrAccessDenied        = "Bu işlem için yetkiniz bulunmuyor."
	ErrMeetingAccessDenied = "Bu toplantıya erişim izniniz yok."

	// Validation Errors
	ErrInvalidRequest   = "Geçersiz istek. Lütfen bilgileri kontrol edip tekrar deneyin."
	ErrRequiredField    = "Lütfen tüm gerekli alanları doldurun."
	ErrInvalidMeetingID = "Geçersiz toplantı bilgisi."
	ErrInvalidEmail     = "Geçersiz e-posta adresi."

	// Resource Not Found Errors
	ErrMeetingNotFound    = "Toplantı bulunamadı."
	ErrProfileNotFound    = "Kullanıcı profili bulunamadı."
	ErrTranscriptNotFound = "Bu toplantı için henüz transkript oluşturulmamış."

	// Calendar Errors
	ErrCalendarNotConnected = "Google Takvim bağlantısı gerekiyor. Lütfen Ayarlar sayfasından Google Takvimi bağlayın."
	ErrCalendarSyncFailed   = "Takvim senkronizasyonu başarısız oldu. Lütfen daha sonra tekrar deneyin."
	ErrCalendarEventFailed  = "Takvim etkinliği oluşturulamadı."

	// Bot Errors
	ErrBotNotConfigured    = "Yapay zeka asistanı şu anda kullanılamıyor."
	ErrBotJoinFailed       = "Asistan toplantıya katılamadı. Lütfen daha sonra tekrar deneyin."
	ErrBotConnectionFailed = "Asistan servisiyle bağlantı kurulamadı."
	ErrMeetingURLRequired  = "Toplantı bağlantısı gerekli."

	// Summary Errors
	ErrSummaryFailed        = "Özet oluşturulamadı. Lütfen daha sonra tekrar deneyin."
	ErrNoTranscriptData     = "Bu toplantı için transkript verisi bulunamadı."
	ErrAIServiceUnavailable = "Yapay zeka servisi şu anda kullanılamıyor."

	// General Errors
	ErrServerError  = "Bir hata oluştu. Lütfen daha sonra tekrar deneyin."
	ErrUpdateFailed = "Güncelleme başarısız oldu. Lütfen tekrar deneyin."
	ErrSaveFailed   = "Kaydetme başarısız oldu. Lütfen tekrar deneyin."
	ErrDeleteFailed = "Silme işlemi başarısız oldu. Lütfen tekrar deneyin."
)
