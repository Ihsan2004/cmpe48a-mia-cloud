/**
 * Translates technical error messages into user-friendly Turkish messages.
 * Backend now returns contextual Turkish messages for most errors.
 * This parser serves as a safety net for legacy English messages and network errors.
 * @param {Error|string} error - The error object or message string
 * @returns {string} User-friendly error message
 */
export function getFriendlyErrorMessage(error) {
    const raw = typeof error === 'string' ? error : error?.message || 'Bilinmeyen bir hata oluştu'
    const message = raw.toLowerCase()

    // Pass through backend Turkish messages directly.
    // Turkish-specific characters (ğ, ü, ş, ö, ç, ı, İ, Ğ, Ü, Ş, Ö, Ç) indicate
    // the backend already sent a localized, user-friendly message.
    if (/[ğüşöçıİĞÜŞÖÇ]/.test(raw) && raw.length <= 200) {
        return raw
    }

    // HTTP status code based errors (check status property on error object)
    const status = typeof error === 'object' ? error?.status : null
    if (status) {
        const statusMessage = getMessageByStatus(status)
        if (statusMessage) return statusMessage
    }

    // Transcript / Search Errors
    if (message.includes('no transcript') ||
        (message.includes('transcript') && message.includes('not found')) ||
        (message.includes('search service error') && (message.includes('404') || message.includes('not found')))) {
        return 'Bu toplantıya ait transkript verisi bulunamadı. Bot toplantıya katılmamış olabilir.'
    }

    // Google Calendar / Auth Errors
    if (message.includes('google access token not found')) {
        return 'Google Takvim erişimi gerekiyor. Lütfen Google ile giriş yapın veya takviminizi bağlayın.'
    }
    if (message.includes('invalid_grant') || message.includes('token expired') || message.includes('revoked')) {
        return 'Google oturumunuzun süresi dolmuş. Lütfen tekrar giriş yapın.'
    }
    if (message.includes('insufficient permission') || message.includes('scope')) {
        return 'Bu işlem için gerekli izinler eksik. Lütfen tekrar yetki verin.'
    }

    // General Auth Errors
    if (message.includes('unauthorized') || message.includes('session expired')) {
        return 'Oturumunuzun süresi dolmuş. Lütfen tekrar giriş yapın.'
    }
    if (message.includes('forbidden')) {
        return 'Bu işlem için yetkiniz bulunmuyor.'
    }
    if (message.includes('invalid credentials') || message.includes('invalid login') || message.includes('wrong password')) {
        return 'E-posta veya şifre hatalı. Lütfen bilgilerinizi kontrol edin.'
    }
    if (message.includes('already exists') || message.includes('already registered')) {
        return 'Bu e-posta adresi zaten kayıtlı. Lütfen giriş yapın.'
    }

    // Network / Server Errors
    if (message.includes('failed to fetch') || message.includes('network error') || message.includes('networkerror')) {
        return 'Sunucuya erişilemiyor. İnternet bağlantınızı kontrol edin veya daha sonra tekrar deneyin.'
    }
    if (message.includes('timeout') || message.includes('timed out') || message.includes('aborted')) {
        return 'İstek zaman aşımına uğradı. Lütfen daha sonra tekrar deneyin.'
    }
    if (message.includes('internal server error') || message.includes('500')) {
        return 'Sunucu tarafında bir hata oluştu. Lütfen daha sonra tekrar deneyin.'
    }
    if (message.includes('bad gateway') || message.includes('502')) {
        return 'Sunucuya şu an ulaşılamıyor. Lütfen birkaç dakika sonra tekrar deneyin.'
    }
    if (message.includes('service unavailable') || message.includes('503')) {
        return 'Servis geçici olarak kullanım dışı. Lütfen birkaç dakika sonra tekrar deneyin.'
    }
    if (message.includes('429') || message.includes('too many requests') || message.includes('rate limit')) {
        return 'Çok fazla istek gönderildi. Lütfen biraz bekleyip tekrar deneyin.'
    }

    // Meeting Specific
    if (message.includes('meeting not found')) {
        return 'Toplantı bulunamadı veya silinmiş.'
    }
    if (message.includes('url') && message.includes('missing')) {
        return 'Toplantı linki bulunamadı. Lütfen toplantı URL bilgisini kontrol edin.'
    }
    if (message.includes('bot') && (message.includes('join') || message.includes('failed'))) {
        return 'Bot toplantıya katılamadı. Lütfen toplantı linkini kontrol edip tekrar deneyin.'
    }
    if (message.includes('bot') && message.includes('already')) {
        return 'Bot zaten bu toplantıda aktif.'
    }

    // Jira Errors
    if (message.includes('jira') && (message.includes('connect') || message.includes('auth'))) {
        return 'Jira bağlantısı kurulamadı. Lütfen tekrar deneyin.'
    }
    if (message.includes('jira') && message.includes('project')) {
        return 'Jira projesi bulunamadı. Lütfen proje ayarlarını kontrol edin.'
    }

    // Validation Errors
    if (message.includes('required') || message.includes('missing')) {
        return 'Lütfen tüm zorunlu alanları doldurun.'
    }
    if (message.includes('invalid email') || message.includes('invalid e-mail')) {
        return 'Geçerli bir e-posta adresi girin.'
    }

    // File/Upload Errors
    if (message.includes('file') && message.includes('size')) {
        return 'Dosya boyutu çok büyük. Lütfen daha küçük bir dosya seçin.'
    }
    if (message.includes('file') && (message.includes('format') || message.includes('type'))) {
        return 'Desteklenmeyen dosya formatı.'
    }

    // Default: Return the original message if it looks like a clean sentence, otherwise generic
    if (message.length > 100 || message.includes('map[') || message.includes('{')) {
        return 'Beklenmeyen bir hata oluştu. Lütfen daha sonra tekrar deneyin.'
    }

    // Capitalize first letter of original message if we fallback
    return (typeof error === 'string' ? error : error.message)
}

function getMessageByStatus(status) {
    switch (status) {
        case 400: return 'Gönderilen bilgilerde bir hata var. Lütfen kontrol edip tekrar deneyin.'
        case 401: return 'Oturumunuzun süresi dolmuş. Lütfen tekrar giriş yapın.'
        case 403: return 'Bu işlemi yapmaya yetkiniz yok.'
        case 404: return 'Aradığınız içerik bulunamadı.'
        case 409: return 'Bu işlem zaten yapılmış veya çakışma var.'
        case 429: return 'Çok fazla istek gönderildi. Lütfen biraz bekleyip tekrar deneyin.'
        case 500: return 'Sunucuda beklenmedik bir hata oluştu. Lütfen daha sonra tekrar deneyin.'
        case 502:
        case 503: return 'Sunucuya şu an ulaşılamıyor. Lütfen birkaç dakika sonra tekrar deneyin.'
        default: return null
    }
}
