import { describe, it, expect } from 'vitest'
import { getFriendlyErrorMessage } from './errorParser'

describe('getFriendlyErrorMessage', () => {
  // ==================== Turkish passthrough ====================

  it('passes through Turkish backend messages', () => {
    const msg = 'Oturum bilgisi bulunamadı. Lütfen tekrar giriş yapın.'
    expect(getFriendlyErrorMessage(msg)).toBe(msg)
  })

  it('passes through Turkish message from Error object', () => {
    const err = new Error('Geçersiz kullanıcı bilgisi.')
    expect(getFriendlyErrorMessage(err)).toBe('Geçersiz kullanıcı bilgisi.')
  })

  it('does NOT passthrough very long Turkish messages', () => {
    const longMsg = 'Ğ'.repeat(201)
    const result = getFriendlyErrorMessage(longMsg)
    // Should hit the long message fallback at the end
    expect(result).not.toBe(longMsg)
  })

  // ==================== HTTP status codes ====================

  it('handles status 401', () => {
    const err = { message: 'something', status: 401 }
    expect(getFriendlyErrorMessage(err)).toContain('giriş yapın')
  })

  it('handles status 403', () => {
    const err = { message: 'something', status: 403 }
    expect(getFriendlyErrorMessage(err)).toContain('yetki')
  })

  it('handles status 404', () => {
    const err = { message: 'something', status: 404 }
    expect(getFriendlyErrorMessage(err)).toContain('bulunamadı')
  })

  it('handles status 429', () => {
    const err = { message: 'something', status: 429 }
    expect(getFriendlyErrorMessage(err)).toContain('fazla istek')
  })

  it('handles status 500', () => {
    const err = { message: 'something', status: 500 }
    expect(getFriendlyErrorMessage(err)).toContain('hata oluştu')
  })

  it('returns null for unknown status', () => {
    const err = { message: 'some error', status: 418 }
    // Should fall through to message-based matching
    expect(getFriendlyErrorMessage(err)).toBeTruthy()
  })

  // ==================== Transcript errors ====================

  it('handles no transcript', () => {
    expect(getFriendlyErrorMessage('No transcript found')).toContain('transkript')
  })

  it('handles transcript not found', () => {
    expect(getFriendlyErrorMessage('transcript data not found')).toContain('transkript')
  })

  // ==================== Auth errors ====================

  it('handles google access token not found', () => {
    expect(getFriendlyErrorMessage('Google access token not found')).toContain('Google')
  })

  it('handles invalid_grant', () => {
    expect(getFriendlyErrorMessage('invalid_grant')).toContain('tekrar giriş')
  })

  it('handles token expired', () => {
    expect(getFriendlyErrorMessage('token expired')).toContain('tekrar giriş')
  })

  it('handles insufficient permission', () => {
    expect(getFriendlyErrorMessage('insufficient permission')).toContain('izin')
  })

  it('handles unauthorized', () => {
    expect(getFriendlyErrorMessage('unauthorized')).toContain('giriş yapın')
  })

  it('handles invalid credentials', () => {
    expect(getFriendlyErrorMessage('invalid credentials')).toContain('hatalı')
  })

  it('handles already exists', () => {
    expect(getFriendlyErrorMessage('user already exists')).toContain('zaten kayıtlı')
  })

  // ==================== Network errors ====================

  it('handles failed to fetch', () => {
    expect(getFriendlyErrorMessage('Failed to fetch')).toContain('bağlantı')
  })

  it('handles timeout', () => {
    expect(getFriendlyErrorMessage('request timed out')).toContain('zaman aşımı')
  })

  it('handles 502 bad gateway', () => {
    expect(getFriendlyErrorMessage('bad gateway')).toContain('ulaşılamıyor')
  })

  it('handles rate limit', () => {
    expect(getFriendlyErrorMessage('rate limit exceeded')).toContain('fazla istek')
  })

  // ==================== Meeting errors ====================

  it('handles meeting not found', () => {
    expect(getFriendlyErrorMessage('meeting not found')).toContain('bulunamadı')
  })

  it('handles bot join failed', () => {
    expect(getFriendlyErrorMessage('bot failed to join')).toContain('katılamadı')
  })

  it('handles bot already active', () => {
    expect(getFriendlyErrorMessage('bot already in meeting')).toContain('aktif')
  })

  // ==================== Edge cases ====================

  it('handles null error', () => {
    expect(getFriendlyErrorMessage(null)).toBeTruthy()
  })

  it('handles undefined error', () => {
    expect(getFriendlyErrorMessage(undefined)).toBeTruthy()
  })

  it('handles empty string gracefully', () => {
    const result = getFriendlyErrorMessage('')
    expect(result).toBe('')  // empty in, empty out — caller should handle
  })

  it('handles error with map[] (Go debug output)', () => {
    expect(getFriendlyErrorMessage('map[error:something went wrong]')).toContain('Beklenmeyen')
  })

  it('handles error with JSON object string', () => {
    expect(getFriendlyErrorMessage('{"error": "internal"}')).toContain('Beklenmeyen')
  })

  it('returns original clean message as fallback', () => {
    const msg = 'Something specific happened'
    expect(getFriendlyErrorMessage(msg)).toBe(msg)
  })
})
